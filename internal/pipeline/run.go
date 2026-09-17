package pipeline

import (
	"fmt"
	"log/slog"
	"time"

	"pssrx/internal/archive"
	"pssrx/internal/geodesy/geoid"
	"pssrx/internal/interrogator"
	"pssrx/internal/pssr"
)

// Result は 2 段を直列に流した結果。
type Result struct {
	Interrogator InterrogatorResult
	PSSR         PSSRResult
}

// Run は interrogator 段と pssr 段を同じブロックのループでメモリ
// 直列に流す。本番の形。intg はファイルを経由せず pssr 段へ渡し、ブロックの
// Interrogations は使わない。
//
// 質問予定はブロック N の分が N+1 で確定する（ドウェル対が閉じてから
// 出る）。pssr 段は出た質問予定と応答を Synchronizer に投入し、処理できる
// 範囲だけを対応づけるので、遅れは自然に吸収される。
//
// is.Intg が nil でなければ intg も書く。質問予定表はそれ自体が成果物
// なので、位置と一緒に残せるようにしてある。
//
// pssr 段へ渡す intg はファイル形式と同じに量子化する（時刻 100 ns、
// 方位 32 ビット）。ファイルから再処理した結果（pssrx pssr）と一致させる
// ため。精度の損失は双基地和で 30 m 以下で、ビーム中心の方位誤差より
// 十分小さい。
func Run(src Source, is InterrogatorStage, ps PSSRStage) (*Result, error) {
	if ps.Log == nil {
		ps.Log = is.Log
	}
	if is.SSRID != ps.Params.SSRID {
		return nil, fmt.Errorf("2 段の SSR が違う: %s / %s", is.SSRID, ps.Params.SSRID)
	}
	return run(src, &is, &ps)
}

// run はブロックごとに段を順に当てる唯一のループ。is / ps は nil なら
// その段を走らせない。
//
//	Received       -> interrogator.Feed -> IntgSink / 量子化して次の段へ
//	Interrogations -> （interrogator 段が無いとき）そのまま次の段へ
//	Replies        -> pssrStep.step    -> pssr.Sink
func run(src Source, is *InterrogatorStage, ps *PSSRStage) (*Result, error) {
	log := slog.Default()
	if is != nil && is.Log != nil {
		log = is.Log
	} else if ps != nil && ps.Log != nil {
		log = ps.Log
	}

	res := &Result{}
	var an *interrogator.Analyzer
	if is != nil {
		var err error
		an, err = interrogator.New(is.Params, interrogator.DefaultConfig(), is.Dist, is.Azimuth, log)
		if err != nil {
			return nil, err
		}
		res.Interrogator = InterrogatorResult{Dist: is.Dist, Azimuth: is.Azimuth}
		log.Info("解析開始",
			"ssr", is.SSRID, "station", is.StationID,
			"pattern_length", is.Params.Pattern.Length(),
			"pattern_period_ns", is.Params.Pattern.Period(),
			"around_time_ns", is.Params.AroundTimeNs,
			"dist_m", is.Dist, "azimuth_rad", is.Azimuth)
	}
	var st *pssrStep
	if ps != nil {
		if err := pssr.Validate(ps.Params, ps.Config); err != nil {
			return nil, err
		}
		gm, err := geoid.Load()
		if err != nil {
			return nil, err
		}
		geom, err := pssr.NewGeometry(ps.SSR, ps.Station, gm)
		if err != nil {
			return nil, err
		}
		st = newPSSRStep(ps.Params, ps.Config, geom, log)
		log.Info("対応づけ開始",
			"ssr", ps.Params.SSRID, "reply_station", ps.Params.StationID,
			"tau_min_ns", ps.Params.TauMinNs, "tau_max_ns", ps.Params.TauMaxNs)
	}

	for blk, err := range src {
		if err != nil {
			return nil, err
		}
		intg := blk.Interrogations
		if an != nil {
			t1 := time.Now()
			out, err := an.Feed(blk.Received, blk.End, blk.Last)
			if err != nil {
				return nil, err
			}
			res.Interrogator.Timing.add(time.Since(t1))
			if is.Intg != nil {
				if err := is.Intg.Save(is.SSRID, out); err != nil {
					return nil, err
				}
			}
			if st != nil {
				intg = archive.QuantizeIntg(out)
			}
		}
		if st != nil {
			t2 := time.Now()
			fixes := st.step(intg, blk.Replies, blk.Last)
			res.PSSR.Timing.add(time.Since(t2))
			if ps.Sink != nil && len(fixes) > 0 {
				if err := ps.Sink.Write(fixes); err != nil {
					return nil, err
				}
			}
		}
	}
	if an != nil {
		res.Interrogator.Stats = an.Stats()
	}
	if st != nil {
		res.PSSR.Stats = st.stats
	}
	return res, nil
}
