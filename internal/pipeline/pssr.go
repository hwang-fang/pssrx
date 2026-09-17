package pipeline

import (
	"fmt"
	"log/slog"
	"math"
	"slices"

	"pssrx/internal/config"
	"pssrx/internal/geodesy"
	"pssrx/internal/geodesy/geoid"
	"pssrx/internal/pssr"
	"pssrx/internal/store"
)

// PSSRStage は pssr 段を 1 つ組み立てるのに要るもの。
//
// 質問予定表（intg）は SSR ごとのファイルで、どの局の受信から作ったかは
// 残らない。そのため応答局は質問解析局と別に指定する（CLI では省略時に
// 同じ局の単局計算）。
type PSSRStage struct {
	Params pssr.Params
	Config pssr.Config
	Log    *slog.Logger
	// SSR / Station は位置推定の原点と応答局の位置。
	SSR     geodesy.OrthometricLLA
	Station geodesy.OrthometricLLA
	// Sink は位置の出力先。nil なら捨てる。
	Sink pssr.Sink
}

// NewPSSRStage は SSR と応答局の設定から対応づけの窓と幾何を導く。
func NewPSSRStage(ssr config.SSR, replyStation config.Station) (PSSRStage, error) {
	cfg := pssr.DefaultConfig()
	params, err := PSSRParams(ssr, replyStation, cfg)
	if err != nil {
		return PSSRStage{}, err
	}
	return PSSRStage{
		Params: params, Config: cfg,
		SSR: ssr.LLA(), Station: replyStation.LLA(),
	}, nil
}

// PSSRResult は pssr 段の実行結果の要約。
type PSSRResult struct {
	Stats  pssr.Stats
	Timing Timing
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
	c := config.SpeedOfLightMPerNs
	p := pssr.Params{
		SSRID:        ssr.ID,
		StationID:    reply.ID,
		TauMinNs:     config.TransponderDelayNs + int64(math.Ceil(d/c)),
		TauMaxNs:     config.TransponderDelayNs + int64(math.Ceil((2*ssr.MaxRangeM+d)/c)),
		AroundTimeNs: params.AroundTimeNs,
		MaxRangeM:    ssr.MaxRangeM,
	}
	if minPRI := slices.Min(params.Pattern.Intervals()); p.TauMaxNs >= minPRI {
		return pssr.Params{}, fmt.Errorf(
			"SSR %s の覆域 %g m では遅延の上限 %d ns が最短 PRI %d ns 以上になり、応答がどの質問へのものか決まりません",
			ssr.ID, ssr.MaxRangeM, p.TauMaxNs, minPRI)
	}
	if err := pssr.Validate(p, cfg); err != nil {
		return pssr.Params{}, err
	}
	return p, nil
}

// RunPSSR は src のブロックの Intg と Replies を対応づけ、幽霊を落とし、
// 位置を求めて Sink へ渡す。
//
// intg からの再処理に使う。期間の先頭の応答は期間より前の質問に属しうる
// ので、ファイルから読むときは store.FileSource.IntgLeadNs に TauMax を渡す。
func RunPSSR(src Source, st PSSRStage) (*PSSRResult, error) {
	res, err := run(src, nil, &st)
	if err != nil {
		return nil, err
	}
	return &res.PSSR, nil
}

// pssrStep は PSSR の段が持ち越すものをまとめ、1 ステップぶんを位置にする。
// 対応づけ → 幽霊抑圧 → 位置推定の順で、ファイル経由でもメモリ直列でも同じ。
type pssrStep struct {
	params pssr.Params
	cfg    pssr.Config
	geom   pssr.Geometry
	log    *slog.Logger
	mgr    *pssr.PairManager
	pairSt pssr.PairState
	supSt  pssr.SuppressState
	stats  pssr.Stats
}

func newPSSRStep(params pssr.Params, cfg pssr.Config, geom pssr.Geometry, log *slog.Logger) *pssrStep {
	return &pssrStep{
		params: params, cfg: cfg, geom: geom, log: log,
		mgr:   pssr.NewPairManager(params, cfg),
		stats: pssr.NewStats(params),
	}
}

// step は質問予定と応答を投入し、処理できる範囲を対応づけて位置にする。
// last が真なら溜まっているものをすべて処理する。
func (s *pssrStep) step(intg []store.Intg, replies []store.AData, last bool) []pssr.Fix {
	s.mgr.PushIntg(&s.stats, intg)
	s.mgr.PushReplies(&s.stats, replies)
	qs, rs := s.mgr.Extract(last)
	plots := pssr.Pair(&s.pairSt, &s.stats, s.params, s.cfg, qs, rs)
	if last {
		plots = append(plots, pssr.Flush(&s.pairSt, &s.stats, s.cfg)...)
	}
	plots = pssr.Suppress(&s.supSt, &s.stats, s.params, s.cfg, plots, last)
	var fixes []pssr.Fix
	for _, p := range plots {
		if fix, ok := pssr.Locate(s.geom, &s.stats, s.params, s.cfg, p); ok {
			fixes = append(fixes, fix)
		}
	}
	return fixes
}
