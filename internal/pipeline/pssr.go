package pipeline

import (
	"fmt"
	"log/slog"
	"math"
	"slices"
	"time"

	"pssrx/internal/config"
	"pssrx/internal/geodesy/geoid"
	"pssrx/internal/physics"
	"pssrx/internal/pssr"
	"pssrx/internal/store"
)

// PSSROptions は設定から見た PSSR の 1 回の実行。
//
// 質問予定表（intg）は SSR ごとのファイルで、どの局の受信から作ったかは
// 残らない。そのため質問解析局と応答局を別々に受ける。応答局を省略すると
// 質問解析局と同じ局の単局計算になる。
type PSSROptions struct {
	SSR          config.SSR
	Station      config.Station // 質問解析局
	ReplyStation config.Station // 応答局
	IntgRoot     string         // 質問予定表のルート
	DataRoot     string         // 局データ（apkx）のルート。qpkx と同じ
	From         time.Time
	To           time.Time
	SortInput    bool
	Log          *slog.Logger
}

// PSSRJob は PSSR の解析ループへの入力そのもの。
type PSSRJob struct {
	Params    pssr.Params
	Config    pssr.Config
	IntgRoot  string
	DataRoot  string
	From      time.Time
	To        time.Time
	SortInput bool
	Log       *slog.Logger
	// Sink はブロックごとに閉じたプロットを受け取る。nil なら捨てる。
	Sink func([]pssr.Plot) error
}

// PSSRResult は実行結果の要約。
type PSSRResult struct {
	Stats    pssr.Stats
	Suppress pssr.SuppressStats
	Timing   Timing
}

// PSSRParams は SSR と応答局の設定から対応づけの窓を導く。
//
//	TauMin = 応答遅延 + d / c                 d は SSR–応答局の基線長
//	TauMax = 応答遅延 + (2·R_max + d) / c     機体は覆域 R_max の内側
//
// TauMax が最短の PRI 以上だと、応答がどの質問へのものか一意に決まらない
// ので拒否する。
func PSSRParams(ssr config.SSR, reply config.Station, cfg pssr.Config) (pssr.Params, error) {
	gm, err := geoid.Load()
	if err != nil {
		return pssr.Params{}, err
	}
	d, _, err := config.Geometry(ssr, reply, gm)
	if err != nil {
		return pssr.Params{}, err
	}
	params, err := InterrogatorParams(ssr.Interrogation)
	if err != nil {
		return pssr.Params{}, err
	}
	c := physics.SpeedOfLightMPerNs
	p := pssr.Params{
		SSRID:        ssr.ID,
		StationID:    reply.ID,
		TauMinNs:     cfg.TransponderDelayNs + int64(math.Ceil(d/c)),
		TauMaxNs:     cfg.TransponderDelayNs + int64(math.Ceil((2*ssr.MaxRangeM+d)/c)),
		AroundTimeNs: params.AroundTimeNs,
	}
	if minPRI := slices.Min(params.Pattern.Intervals()); p.TauMaxNs >= minPRI {
		return pssr.Params{}, fmt.Errorf(
			"SSR %s の覆域 %g m では遅延の上限 %d ns が最短 PRI %d ns 以上になり、応答がどの質問へのものか決まりません",
			ssr.ID, ssr.MaxRangeM, p.TauMaxNs, minPRI)
	}
	return p, nil
}

// Job は設定から PSSRJob を組み立てる。
func (o PSSROptions) Job() (PSSRJob, error) {
	cfg := pssr.DefaultConfig()
	params, err := PSSRParams(o.SSR, o.ReplyStation, cfg)
	if err != nil {
		return PSSRJob{}, err
	}
	return PSSRJob{
		Params: params, Config: cfg,
		IntgRoot: o.IntgRoot, DataRoot: o.DataRoot,
		From: o.From, To: o.To, SortInput: o.SortInput, Log: o.Log,
	}, nil
}

// RunPSSR は設定から PSSRJob を組み立てて RunPSSRJob を呼ぶ。
func RunPSSR(o PSSROptions, sink func([]pssr.Plot) error) (*PSSRResult, error) {
	job, err := o.Job()
	if err != nil {
		return nil, err
	}
	job.Sink = sink
	return RunPSSRJob(job)
}

// RunPSSRJob は From から To まで 1 分刻みで応答を読み、質問予定表と
// 対応づけてプロットを Sink へ渡す。
//
// ファイル経由の暫定実装。質問予定表は各分のファイルをそのまま
// 「その分まで確定」として渡す。interrogator 段とメモリで直列にする
// 完成形では、pipeline が確定した範囲を渡す点だけが変わる。
func RunPSSRJob(j PSSRJob) (*PSSRResult, error) {
	if j.Log == nil {
		j.Log = slog.Default()
	}
	if !j.To.After(j.From) {
		return nil, fmt.Errorf("to (%s) は from (%s) より後である必要があります", j.To, j.From)
	}
	pr, err := pssr.New(j.Params, j.Config, j.Log)
	if err != nil {
		return nil, err
	}
	sup, err := pssr.NewSuppressor(j.Params, j.Config)
	if err != nil {
		return nil, err
	}
	aRepo := &store.AdataRepository{Root: j.DataRoot, SortInput: j.SortInput}
	iRepo := &store.IntgRepository{Root: j.IntgRoot}

	j.Log.Info("対応づけ開始",
		"ssr", j.Params.SSRID, "reply_station", j.Params.StationID,
		"from", j.From, "to", j.To,
		"tau_min_ns", j.Params.TauMinNs, "tau_max_ns", j.Params.TauMaxNs)

	res := &PSSRResult{}
	// 期間の先頭の応答は前の分の質問へ遡りうるので、その分だけ先に渡す
	from := j.From.UnixNano()
	head, err := iRepo.Fetch(j.Params.SSRID, from-j.Params.TauMaxNs, from)
	if err != nil {
		return nil, err
	}
	if _, err := pr.Feed(nil, head, from, false); err != nil {
		return nil, err
	}
	for cur := j.From; cur.Before(j.To); cur = cur.Add(time.Minute) {
		next := cur.Add(time.Minute)
		last := !next.Before(j.To)
		replies, err := aRepo.Fetch(j.Params.StationID, cur.UnixNano(), next.UnixNano())
		if err != nil {
			return nil, err
		}
		intg, err := iRepo.Fetch(j.Params.SSRID, cur.UnixNano(), next.UnixNano())
		if err != nil {
			return nil, err
		}

		t1 := time.Now()
		plots, err := pr.Feed(replies, intg, next.UnixNano(), last)
		if err != nil {
			return nil, err
		}
		plots = sup.Push(plots, last)
		res.Timing.add(time.Since(t1))

		if j.Sink != nil && len(plots) > 0 {
			if err := j.Sink(plots); err != nil {
				return nil, err
			}
		}
	}
	res.Stats = pr.Stats()
	res.Suppress = sup.Stats()
	return res, nil
}
