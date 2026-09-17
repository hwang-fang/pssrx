package main

import (
	"flag"
	"fmt"
	"time"

	"pssrx/internal/interrogator"
	"pssrx/internal/pipeline"
	"pssrx/internal/store"
)

// runInterrogator は qpkx（受信した質問データ）を読み、SSR のドウェルを
// 検出して intg（質問予定表）を書き出す。
func runInterrogator(args []string) error {
	fs := flag.NewFlagSet("pssrx interrogator", flag.ContinueOnError)
	var c common
	c.register(fs)
	var (
		stationID = fs.String("station", "", "処理対象の測定局 ID (必須)")
		ssrID     = fs.String("ssr", "", "処理対象の SSR ID (必須)")
		qpkxRoot  = fs.String("qpkx-root", "", "qpkx のルートディレクトリ (必須)")
		intgRoot  = fs.String("intg-root", "", "intg の出力先ルートディレクトリ (必須)")
		appendOut = fs.Bool("append", false, "既存の intg を切り詰めず常に追記する（別々に解析した期間を継ぎ足す用）")
		showStats = fs.Bool("stats", false, "棄却理由別の件数と処理時間を出力する")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, from, to, log, err := c.parse(fs, map[string]*string{
		"-station": stationID, "-ssr": ssrID, "-qpkx-root": qpkxRoot, "-intg-root": intgRoot,
	})
	if err != nil {
		return err
	}
	station, err := cfg.Station(*stationID)
	if err != nil {
		return err
	}
	ssr, err := cfg.SSR(*ssrID)
	if err != nil {
		return err
	}

	job, err := pipeline.Options{SSR: ssr, Station: station, Log: log}.Job()
	if err != nil {
		return err
	}
	job.Intg = &store.IntgRepository{Root: *intgRoot, Append: *appendOut, Log: log}
	src := store.FileSource{
		QpkxRoot: *qpkxRoot, QpkxStation: station.ID,
		From: from, To: to,
	}
	res, err := pipeline.RunJob(src.Blocks(), job)
	if err != nil {
		return err
	}
	if *showStats {
		printInterrogatorStats(res.Stats, res.Timing)
	}
	return nil
}

func printInterrogatorStats(s interrogator.Stats, t pipeline.Timing) {
	fmt.Printf("\n--- 解析結果 ---\n")
	fmt.Printf("ブロック数              %d\n", s.Blocks)
	fmt.Printf("セグメント数            %d\n", s.Segments)
	fmt.Printf("  一周より長く棄却      %d\n", s.SegmentsTooLong)
	fmt.Printf("  連鎖が見つからず棄却  %d\n", s.ChainNotFound)
	fmt.Printf("  連鎖長不足で棄却      %d\n", s.ChainTooShort)
	fmt.Printf("  放物線フィット失敗    %d\n", s.ParabolaFailed)
	fmt.Printf("検出ドウェル            %d\n", s.DwellsDetected)
	fmt.Printf("  引き渡し順序異常      %d\n", s.LastDwellOutOfOrder)
	fmt.Printf("ドウェル対の棄却\n")
	fmt.Printf("  走査周期と不整合      %d\n", s.RotationMismatch)
	fmt.Printf("  連結失敗              %d\n", s.BracketFailed)
	fmt.Printf("出力ブラケット          %d\n", s.BracketsEmitted)
	fmt.Printf("出力レコード            %d\n", s.RecordsEmitted)
	printTiming(t)
}

func printTiming(t pipeline.Timing) {
	if t.Blocks == 0 {
		return
	}
	fmt.Printf("\n--- 処理時間 (解析のみ) ---\n")
	fmt.Printf("mean %v, min %v, max %v, 合計 %v (%d ブロック)\n",
		t.Mean().Round(time.Microsecond), t.Min.Round(time.Microsecond),
		t.Max.Round(time.Microsecond), t.Total.Round(time.Millisecond), t.Blocks)
}
