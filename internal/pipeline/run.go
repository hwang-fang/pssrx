package pipeline

import (
	"fmt"
	"log/slog"
	"time"

	"pssrx/internal/geodesy/geoid"
	"pssrx/internal/interrogator/analyze"
	"pssrx/internal/pssr"
	"pssrx/internal/store"
)

// BothResult は 2 段を直列に流した結果。
type BothResult struct {
	Interrogator Result
	PSSR         PSSRResult
}

// RunBoth は interrogator 段と pssr 段を同じ 1 分ブロックのループでメモリ
// 直列に流す。intg はファイルを経由せず pssr 段へ渡す。
//
// 質問予定はブロック N の分が N+1 で確定する（ドウェル対が閉じてから
// 出る）ので、pssr 段には「ここまでの質問予定が出揃った」時刻として、
// 直近に出た質問予定の末尾を渡す。対応する質問予定がまだ無い応答は
// pssr 段が保留する。
//
// ij.IntgRoot が空でなければ intg もファイルに書く。質問予定表はそれ
// 自体が成果物なので、位置と一緒に残せるようにしてある。
//
// pssr 段へ渡す intg はファイル形式と同じに量子化する（時刻 100 ns、
// 方位 32 ビット）。ファイルから再処理した結果（pssrx pssr）と一致させる
// ため。精度の損失は双基地和で 30 m 以下で、ビーム中心の方位誤差より
// 十分小さい。
func RunBoth(ij Job, pj PSSRJob) (*BothResult, error) {
	if ij.Log == nil {
		ij.Log = slog.Default()
	}
	if pj.Log == nil {
		pj.Log = ij.Log
	}
	if !ij.To.After(ij.From) {
		return nil, fmt.Errorf("to (%s) は from (%s) より後である必要があります", ij.To, ij.From)
	}
	if ij.SSRID != pj.Params.SSRID {
		return nil, fmt.Errorf("2 段の SSR が違う: %s / %s", ij.SSRID, pj.Params.SSRID)
	}

	an, err := analyze.New(ij.Params, analyze.DefaultConfig(), ij.Dist, ij.Azimuth, ij.Log)
	if err != nil {
		return nil, err
	}
	pr, err := pssr.New(pj.Params, pj.Config, pj.Log)
	if err != nil {
		return nil, err
	}
	sup, err := pssr.NewSuppressor(pj.Params, pj.Config)
	if err != nil {
		return nil, err
	}
	gm, err := geoid.Load()
	if err != nil {
		return nil, err
	}
	loc, err := pssr.NewLocator(pj.SSR, pj.Station, gm, pj.Params, pj.Config)
	if err != nil {
		return nil, err
	}
	qRepo := &store.QdataRepository{Root: ij.QpkxRoot, SortInput: ij.SortInput}
	aRepo := &store.AdataRepository{Root: pj.DataRoot, SortInput: pj.SortInput}
	var iRepo *store.IntgRepository
	if ij.IntgRoot != "" {
		iRepo = &store.IntgRepository{Root: ij.IntgRoot, Append: ij.Append, Log: ij.Log}
	}

	ij.Log.Info("直列解析開始",
		"ssr", ij.SSRID, "station", ij.StationID, "reply_station", pj.Params.StationID,
		"from", ij.From, "to", ij.To)

	res := &BothResult{Interrogator: Result{Dist: ij.Dist, Azimuth: ij.Azimuth}}
	var intgFinalUpTo int64
	for cur := ij.From; cur.Before(ij.To); cur = cur.Add(time.Minute) {
		next := cur.Add(time.Minute)
		last := !next.Before(ij.To)

		qdata, err := qRepo.Fetch(ij.StationID, cur.UnixNano(), next.UnixNano())
		if err != nil {
			return nil, err
		}
		t1 := time.Now()
		intg, err := an.Feed(qdata, next.UnixNano(), last)
		if err != nil {
			return nil, err
		}
		res.Interrogator.Timing.add(time.Since(t1))
		if iRepo != nil {
			if err := iRepo.Save(ij.SSRID, intg); err != nil {
				return nil, err
			}
		}
		intg = store.QuantizeIntg(intg)
		if len(intg) > 0 {
			intgFinalUpTo = intg[len(intg)-1].Timestamp + 1
		}

		replies, err := aRepo.Fetch(pj.Params.StationID, cur.UnixNano(), next.UnixNano())
		if err != nil {
			return nil, err
		}
		t2 := time.Now()
		plots, err := pr.Feed(replies, intg, intgFinalUpTo, last)
		if err != nil {
			return nil, err
		}
		plots = sup.Push(plots, last)
		var fixes []pssr.Fix
		for _, p := range plots {
			if fix, ok := loc.Locate(p); ok {
				fixes = append(fixes, fix)
			}
		}
		res.PSSR.Timing.add(time.Since(t2))
		if pj.Sink != nil && len(fixes) > 0 {
			if err := pj.Sink.Write(fixes); err != nil {
				return nil, err
			}
		}
	}
	res.Interrogator.Stats = an.Stats()
	res.PSSR.Stats = pr.Stats()
	res.PSSR.Suppress = sup.Stats()
	res.PSSR.Locate = loc.Stats()
	return res, nil
}
