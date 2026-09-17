package pipeline

import (
	"fmt"
	"log/slog"
	"time"

	"pssrx/internal/geodesy/geoid"
	"pssrx/internal/interrogator"
	"pssrx/internal/pssr"
	"pssrx/internal/store"
)

// BothResult は 2 段を直列に流した結果。
type BothResult struct {
	Interrogator Result
	PSSR         PSSRResult
}

// RunBoth は interrogator 段と pssr 段を同じブロックのループでメモリ
// 直列に流す。intg はファイルを経由せず pssr 段へ渡し、ブロックの Intg は
// 使わない。
//
// 質問予定はブロック N の分が N+1 で確定する（ドウェル対が閉じてから
// 出る）。pssr 段は出た質問予定と応答を PairManager に投入し、処理できる
// 範囲だけを対応づけるので、遅れは自然に吸収される。
//
// ij.Intg が nil でなければ intg も書く。質問予定表はそれ自体が成果物
// なので、位置と一緒に残せるようにしてある。
//
// pssr 段へ渡す intg はファイル形式と同じに量子化する（時刻 100 ns、
// 方位 32 ビット）。ファイルから再処理した結果（pssrx pssr）と一致させる
// ため。精度の損失は双基地和で 30 m 以下で、ビーム中心の方位誤差より
// 十分小さい。
func RunBoth(src Source, ij Job, pj PSSRJob) (*BothResult, error) {
	if pj.Log == nil {
		pj.Log = ij.Log
	}
	if ij.SSRID != pj.Params.SSRID {
		return nil, fmt.Errorf("2 段の SSR が違う: %s / %s", ij.SSRID, pj.Params.SSRID)
	}
	return run(src, &ij, &pj)
}

// run はブロックごとに段を順に当てる唯一のループ。ij / pj は nil なら
// その段を走らせない。
//
//	QData   -> interrogator.Feed -> IntgSink / 量子化して次の段へ
//	Intg    -> （interrogator 段が無いとき）そのまま次の段へ
//	Replies -> pssrStep.step    -> pssr.Sink
func run(src Source, ij *Job, pj *PSSRJob) (*BothResult, error) {
	log := slog.Default()
	if ij != nil && ij.Log != nil {
		log = ij.Log
	} else if pj != nil && pj.Log != nil {
		log = pj.Log
	}

	res := &BothResult{}
	var an *interrogator.Analyzer
	if ij != nil {
		var err error
		an, err = interrogator.New(ij.Params, interrogator.DefaultConfig(), ij.Dist, ij.Azimuth, log)
		if err != nil {
			return nil, err
		}
		res.Interrogator = Result{Dist: ij.Dist, Azimuth: ij.Azimuth}
		log.Info("解析開始",
			"ssr", ij.SSRID, "station", ij.StationID,
			"pattern_length", ij.Params.Pattern.Length(),
			"pattern_period_ns", ij.Params.Pattern.Period(),
			"around_time_ns", ij.Params.AroundTimeNs,
			"dist_m", ij.Dist, "azimuth_rad", ij.Azimuth)
	}
	var st *pssrStep
	if pj != nil {
		if err := pssr.Validate(pj.Params, pj.Config); err != nil {
			return nil, err
		}
		gm, err := geoid.Load()
		if err != nil {
			return nil, err
		}
		geom, err := pssr.NewGeometry(pj.SSR, pj.Station, gm)
		if err != nil {
			return nil, err
		}
		st = newPSSRStep(pj.Params, pj.Config, geom, log)
		log.Info("対応づけ開始",
			"ssr", pj.Params.SSRID, "reply_station", pj.Params.StationID,
			"tau_min_ns", pj.Params.TauMinNs, "tau_max_ns", pj.Params.TauMaxNs)
	}

	for blk, err := range src {
		if err != nil {
			return nil, err
		}
		intg := blk.Intg
		if an != nil {
			t1 := time.Now()
			out, err := an.Feed(blk.QData, blk.End, blk.Last)
			if err != nil {
				return nil, err
			}
			res.Interrogator.Timing.add(time.Since(t1))
			if ij.Intg != nil {
				if err := ij.Intg.Save(ij.SSRID, out); err != nil {
					return nil, err
				}
			}
			if st != nil {
				intg = store.QuantizeIntg(out)
			}
		}
		if st != nil {
			t2 := time.Now()
			fixes := st.step(intg, blk.Replies, blk.Last)
			res.PSSR.Timing.add(time.Since(t2))
			if pj.Sink != nil && len(fixes) > 0 {
				if err := pj.Sink.Write(fixes); err != nil {
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
