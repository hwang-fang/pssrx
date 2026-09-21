package pipeline

import (
	"fmt"
	"log/slog"
	"math"
	"slices"

	"pssrx/internal/config"
	"pssrx/internal/geodesy"
	"pssrx/internal/geodesy/geoid"
	"pssrx/internal/pssr/bistatic"
	"pssrx/internal/pssr/plot"
	"pssrx/internal/pssr/sink"
	"pssrx/internal/pssr/tracking"
	"pssrx/internal/record"
	"pssrx/internal/ssr"
)

// PSSRStage は pssr 段を 1 つ組み立てるのに要るもの。
//
// 質問予定表（intg）は SSR ごとのファイルで、どの局の受信から作ったかは
// 残らない。そのため応答局は質問解析局と別に指定する（CLI では省略時に
// 同じ局の単局計算）。
type PSSRStage struct {
	Params PSSRParams
	Config PSSRConfig
	Log    *slog.Logger
	// SSR / Station は位置推定の原点と応答局の位置。
	SSR     geodesy.OrthometricLLA
	Station geodesy.OrthometricLLA
	// Sink は位置の出力先。nil なら捨てる。
	Sink sink.Sink
}

// PSSRParams は局と SSR の組に固有の値を、pssr 段の各パッケージぶん
// まとめたもの。設定からの導出は NewPSSRParams が担う。
type PSSRParams struct {
	SSRID     string
	StationID string // 応答を受信した局
	Plot      plot.Params
	Bistatic  bistatic.Params
	Tracking  tracking.Params
}

// PSSRConfig は pssr 段の各パッケージの手続きの定数。
type PSSRConfig struct {
	Plot     plot.Config
	Bistatic bistatic.Config
	Tracking tracking.Config
}

// DefaultPSSRConfig は各パッケージの既定の定数。
func DefaultPSSRConfig() PSSRConfig {
	return PSSRConfig{Plot: plot.DefaultConfig(), Bistatic: bistatic.DefaultConfig(), Tracking: tracking.DefaultConfig()}
}

// Validate は各パッケージの Params と Config の整合を検査する。
func (p PSSRParams) Validate(cfg PSSRConfig) error {
	if err := plot.Validate(p.Plot, cfg.Plot); err != nil {
		return err
	}
	if err := bistatic.Validate(p.Bistatic, cfg.Bistatic); err != nil {
		return err
	}
	return tracking.Validate(p.Tracking, cfg.Tracking)
}

// NewPSSRStage は SSR と応答局の設定から対応づけの窓と幾何を導き、
// analysis 節で既定の定数を上書きする。
func NewPSSRStage(ssr config.SSR, replyStation config.Station, analysis config.PSSRAnalysis) (PSSRStage, error) {
	cfg := PSSRConfigFromAnalysis(analysis)
	params, err := NewPSSRParams(ssr, replyStation, cfg)
	if err != nil {
		return PSSRStage{}, fmt.Errorf("analysis.pssr: %w", err)
	}
	return PSSRStage{
		Params: params, Config: cfg,
		SSR: ssr.LLA(), Station: replyStation.LLA(),
	}, nil
}

// PSSRConfigFromAnalysis は既定の定数に analysis 節の項目を重ねる。
// 鏡像の 1 項目は 3 つの Config のうち同じ名前を持つものへ入る。方位差
// resolve_azimuth_separation_rad は抑圧（plot）と重複解消（tracking）が
// 共用するので両方に配る。
func PSSRConfigFromAnalysis(analysis config.PSSRAnalysis) PSSRConfig {
	cfg := DefaultPSSRConfig()
	applyAnalysis(analysis, &cfg.Plot, &cfg.Bistatic, &cfg.Tracking)
	cfg.Plot.ImageAzimuthSeparationRad = cfg.Tracking.ResolveAzimuthSeparationRad
	return cfg
}

// PSSRResult は pssr 段の実行結果の要約。
type PSSRResult struct {
	Plot     plot.Stats
	Bistatic bistatic.Stats
	Tracking tracking.Stats
	Timing   Timing
}

// NewPSSRParams は SSR と応答局の設定から対応づけの窓を導く。
//
//	TauMin = 応答遅延 + d / c                 d は SSR–応答局の基線長
//	TauMax = 応答遅延 + (2·R_max + d) / c     機体は覆域 R_max の内側
//
// TauMax が最短の PRI 以上だと、応答がどの質問へのものか一意に決まらない
// ので拒否する。
func NewPSSRParams(s config.SSR, reply config.Station, cfg PSSRConfig) (PSSRParams, error) {
	gm, err := geoid.Load()
	if err != nil {
		return PSSRParams{}, err
	}
	d, _, err := config.Baseline(s, reply, gm)
	if err != nil {
		return PSSRParams{}, err
	}
	params, err := InterrogatorParams(s.Interrogation)
	if err != nil {
		return PSSRParams{}, err
	}
	c := ssr.SpeedOfLightMPerNs
	p := PSSRParams{
		SSRID:     s.ID,
		StationID: reply.ID,
		Plot: plot.Params{
			TauMinNs:     ssr.TransponderDelayNs + int64(math.Ceil(d/c)),
			TauMaxNs:     ssr.TransponderDelayNs + int64(math.Ceil((2*s.MaxRangeM+d)/c)),
			AroundTimeNs: params.AroundTimeNs,
		},
		Bistatic: bistatic.Params{AroundTimeNs: params.AroundTimeNs, MeanPRINs: params.Pattern.MeanPRI(), MaxRangeM: s.MaxRangeM},
		Tracking: tracking.Params{AroundTimeNs: params.AroundTimeNs},
	}
	if minPRI := slices.Min(params.Pattern.Intervals()); p.Plot.TauMaxNs >= minPRI {
		return PSSRParams{}, fmt.Errorf(
			"SSR %s の覆域 %g m では遅延の上限 %d ns が最短 PRI %d ns 以上になり、応答がどの質問へのものか決まりません",
			s.ID, s.MaxRangeM, p.Plot.TauMaxNs, minPRI)
	}
	if err := p.Validate(cfg); err != nil {
		return PSSRParams{}, err
	}
	return p, nil
}

// RunPSSR は src のブロックの Interrogations と Replies を対応づけ、幽霊を落とし、
// 位置を求めて Sink へ渡す。
//
// intg からの再処理に使う。期間の先頭の応答は期間より前の質問に属しうる
// ので、ファイルから読むときは archive.FileSource.IntgLeadNs に TauMax を渡す。
func RunPSSR(src Source, st PSSRStage) (*PSSRResult, error) {
	res, err := run(src, nil, &st)
	if err != nil {
		return nil, err
	}
	return &res.PSSR, nil
}

// pssrStep は PSSR の段が持ち越すものをまとめ、1 ステップぶんを位置にする。
// 対応づけ → 幽霊抑圧 → 位置推定 → 連続性の判定 → 断片の連結 → 重複解消 →
// 平滑化の順で、ファイル経由でもメモリ直列でも同じ。
type pssrStep struct {
	params  PSSRParams
	cfg     PSSRConfig
	geom    bistatic.Geometry
	log     *slog.Logger
	mgr     *plot.Synchronizer
	pairSt  plot.RunState
	supSt   plot.SuppressState
	trackSt tracking.TrackState
	linkSt  tracking.LinkState
	resSt   tracking.ResolveState
	smSt    tracking.SmoothState
	res     PSSRResult
}

func newPSSRStep(params PSSRParams, cfg PSSRConfig, geom bistatic.Geometry, log *slog.Logger) *pssrStep {
	return &pssrStep{
		params: params, cfg: cfg, geom: geom, log: log,
		mgr: plot.NewSynchronizer(params.Plot, cfg.Plot),
		res: PSSRResult{Plot: plot.NewStats(params.Plot)},
	}
}

// step は質問予定と応答を投入し、処理できる範囲を対応づけて位置にする。
// last が真なら溜まっているものをすべて処理する。
func (s *pssrStep) step(intg []record.Interrogation, replies []record.Reply, last bool) []tracking.Fix {
	ps, pc, st := s.params.Plot, s.cfg.Plot, &s.res.Plot
	s.mgr.PushInterrogations(st, intg)
	s.mgr.PushReplies(st, replies)
	qs, rs := s.mgr.Extract(st, last)
	plots := plot.Pair(&s.pairSt, st, ps, pc, qs, rs)
	if last {
		plots = append(plots, plot.CloseRuns(&s.pairSt, st, pc)...)
	}
	plots = plot.Suppress(&s.supSt, st, ps, pc, plots, last)
	var fixes []tracking.Fix
	for _, p := range plots {
		if fix, ok := bistatic.Locate(s.geom, &s.res.Bistatic, s.params.Bistatic, s.cfg.Bistatic, p); ok {
			fixes = append(fixes, fix)
		}
	}
	ts, tc, tst := s.params.Tracking, s.cfg.Tracking, &s.res.Tracking
	fixes = tracking.Track(&s.trackSt, tst, ts, tc, fixes, last)
	fixes = tracking.Link(&s.linkSt, tst, ts, tc, fixes, last)
	fixes = tracking.Resolve(&s.resSt, tst, ts, tc, fixes, last)
	return tracking.Smooth(&s.smSt, tst, s.geom.Converter(), ts, tc, fixes, last)
}
