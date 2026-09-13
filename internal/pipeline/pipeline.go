// Package pipeline は 1 分ブロック単位の解析ループをまとめる。
//
// CLI と回帰テストが同じ経路を通るようにパッケージへ切り出してある。
// テストが CLI の内部を再実装すると、両者がずれたときに検出できない。
//
// 入口は 2 段になっている。
//
//	Options -> Job   設定から解析パラメータと局の幾何を導く（変わりうる I/F）
//	Job -> Result    qpkx を読み、解析し、intg を書く（固定した解析本体）
//
// ゴールデンテストは Job から入る。設定の書式や座標の扱いが変わっても、
// 解析本体が同じ値を受け取る限りゴールデンは変えずに済む。
package pipeline

import (
	"fmt"
	"log/slog"
	"time"

	"pssrx/internal/config"
	"pssrx/internal/geodesy/geoid"
	"pssrx/internal/interrogator"
	"pssrx/internal/store"
)

// Options は設定から見た 1 回の実行。Job へ変換してから走らせる。
type Options struct {
	SSR       config.SSR
	Station   config.Station
	QpkxRoot  string
	IntgRoot  string
	From      time.Time
	To        time.Time
	SortInput bool
	Append    bool
	Log       *slog.Logger
}

// Job は解析ループへの入力そのもの。設定や幾何の計算はここに含まない。
type Job struct {
	SSRID     string // intg の出力先を決める
	StationID string // qpkx の読み込み元を決める
	Params    interrogator.Params
	Dist      float64 // SSR から測定局への距離 [m]
	Azimuth   float64 // SSR から見た測定局の方位 [rad]
	QpkxRoot  string
	IntgRoot  string
	From      time.Time
	To        time.Time
	SortInput bool
	Append    bool
	Log       *slog.Logger
}

// Job は設定から解析パラメータと局の幾何を導く。
func (o Options) Job() (Job, error) {
	params, err := InterrogatorParams(o.SSR.Interrogation)
	if err != nil {
		return Job{}, err
	}
	gm, err := geoid.Load()
	if err != nil {
		return Job{}, err
	}
	dist, azimuth, err := config.Geometry(o.SSR, o.Station, gm)
	if err != nil {
		return Job{}, err
	}
	return Job{
		SSRID:     o.SSR.ID,
		StationID: o.Station.ID,
		Params:    params,
		Dist:      dist,
		Azimuth:   azimuth,
		QpkxRoot:  o.QpkxRoot,
		IntgRoot:  o.IntgRoot,
		From:      o.From,
		To:        o.To,
		SortInput: o.SortInput,
		Append:    o.Append,
		Log:       o.Log,
	}, nil
}

// Result は実行結果の要約。
type Result struct {
	Stats   interrogator.Stats
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

// Run は設定から Job を組み立てて RunJob を呼ぶ。
func Run(o Options) (*Result, error) {
	job, err := o.Job()
	if err != nil {
		return nil, err
	}
	return RunJob(job)
}

// RunJob は From から To まで 1 分刻みで解析し、intg を書き出す。
func RunJob(j Job) (*Result, error) {
	if j.Log == nil {
		j.Log = slog.Default()
	}
	if !j.To.After(j.From) {
		return nil, fmt.Errorf("to (%s) は from (%s) より後である必要があります", j.To, j.From)
	}

	an, err := interrogator.New(j.Params, interrogator.DefaultConfig(), j.Dist, j.Azimuth, j.Log)
	if err != nil {
		return nil, err
	}
	qRepo := &store.QdataRepository{Root: j.QpkxRoot, SortInput: j.SortInput}
	iRepo := &store.IntgRepository{Root: j.IntgRoot, Append: j.Append, Log: j.Log}

	j.Log.Info("解析開始",
		"ssr", j.SSRID, "station", j.StationID,
		"from", j.From, "to", j.To,
		"pattern_length", j.Params.Pattern.Length(),
		"pattern_period_ns", j.Params.Pattern.Period(),
		"around_time_ns", j.Params.AroundTimeNs,
		"dist_m", j.Dist, "azimuth_rad", j.Azimuth,
		"sort_input", j.SortInput)

	res := &Result{Dist: j.Dist, Azimuth: j.Azimuth}
	for cur := j.From; cur.Before(j.To); cur = cur.Add(time.Minute) {
		next := cur.Add(time.Minute)
		qdata, err := qRepo.Fetch(j.StationID, cur.UnixNano(), next.UnixNano())
		if err != nil {
			return nil, err
		}

		t1 := time.Now()
		intg, err := an.Feed(qdata, next.UnixNano(), !next.Before(j.To))
		if err != nil {
			return nil, err
		}
		res.Timing.add(time.Since(t1))

		if err := iRepo.Save(j.SSRID, intg); err != nil {
			return nil, err
		}
	}
	res.Stats = an.Stats()
	return res, nil
}
