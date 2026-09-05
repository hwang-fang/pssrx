// Package pipeline は 1 分ブロック単位の解析ループをまとめる。
//
// CLI と回帰テストが同じ経路を通るようにパッケージへ切り出してある。
// テストが CLI の内部を再実装すると、両者がずれたときに検出できない。
package pipeline

import (
	"fmt"
	"log/slog"
	"time"

	"pssrx/internal/analyze"
	"pssrx/internal/config"
	"pssrx/internal/store"
)

// Options は 1 回の実行に必要な入出力設定。
type Options struct {
	Config    *config.File
	QpkxRoot  string
	IntgRoot  string
	From      time.Time
	To        time.Time
	SortInput bool
	Append    bool
	Log       *slog.Logger
}

// Result は実行結果の要約。
type Result struct {
	Stats   analyze.Stats
	Timing  Timing
	Dist    float64
	Azimuth float64
}

// Timing はブロックあたりの解析所要時間の集計。I/O は含まない。
//
// 標本を全部持たずに集計値だけを更新する。1 分 1 ブロックなので、
// 標本を溜めると長時間の実行でそのぶん増え続けてしまう。
type Timing struct {
	Blocks   int
	Total    time.Duration
	Min, Max time.Duration
}

func (t *Timing) add(d time.Duration) {
	if t.Blocks == 0 || d < t.Min {
		t.Min = d
	}
	if d > t.Max {
		t.Max = d
	}
	t.Blocks++
	t.Total += d
}

// Mean は 1 ブロックあたりの平均所要時間。
func (t Timing) Mean() time.Duration {
	if t.Blocks == 0 {
		return 0
	}
	return t.Total / time.Duration(t.Blocks)
}

// Run は From から To まで 1 分刻みで解析し、intg を書き出す。
func Run(o Options) (*Result, error) {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if !o.To.After(o.From) {
		return nil, fmt.Errorf("to (%s) は from (%s) より後である必要があります", o.To, o.From)
	}
	params, err := o.Config.Params()
	if err != nil {
		return nil, err
	}
	dist, azimuth := o.Config.Geometry()

	an, err := analyze.New(params, analyze.DefaultConfig(), dist, azimuth, o.Log)
	if err != nil {
		return nil, err
	}
	qRepo := &store.QdataRepository{Root: o.QpkxRoot, SortInput: o.SortInput}
	iRepo := &store.IntgRepository{Root: o.IntgRoot, Append: o.Append, Log: o.Log}

	o.Log.Info("解析開始",
		"ssr", o.Config.SSR.ID, "station", o.Config.Station.ID,
		"from", o.From, "to", o.To,
		"pattern_length", params.Pattern.Length(),
		"pattern_period_ns", params.Pattern.Period(),
		"around_time_ns", params.AroundTimeNs,
		"dist_m", dist, "azimuth_rad", azimuth,
		"sort_input", o.SortInput)

	res := &Result{Dist: dist, Azimuth: azimuth}
	for cur := o.From; cur.Before(o.To); cur = cur.Add(time.Minute) {
		next := cur.Add(time.Minute)
		qdata, err := qRepo.Fetch(o.Config.Station.ID, cur.UnixNano(), next.UnixNano())
		if err != nil {
			return nil, err
		}

		t1 := time.Now()
		intg, err := an.Feed(qdata, next.UnixNano(), !next.Before(o.To))
		if err != nil {
			return nil, err
		}
		res.Timing.add(time.Since(t1))

		if err := iRepo.Save(o.Config.SSR.ID, intg); err != nil {
			return nil, err
		}
	}
	res.Stats = an.Stats()
	return res, nil
}
