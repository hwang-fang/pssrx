// Command interrogator は qpkx（受信した質問データ）を読み、SSR のドウェルを
// 検出して intg（質問予定表）を書き出す。
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"pssrx/internal/config"
	"pssrx/internal/interrogator/analyze"
	"pssrx/internal/nanotime"
	"pssrx/internal/pipeline"
)

// timeLayout は -from / -to の書式。ファイル名と同じ 12 桁の数字列だと
// 打ち間違いに気づきにくいので ISO 風に取る。タイムゾーンは JST 固定。
// 入出力のファイル名が JST 前提で組まれているため、オフセット付きの
// 指定を許すと混乱するだけになる。
const timeLayout = "2006-01-02T15:04"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		cfgPath   = flag.String("config", "", "SSR・測定局のマスタ YAML (必須)")
		stationID = flag.String("station", "", "処理対象の測定局 ID (必須)")
		ssrID     = flag.String("ssr", "", "処理対象の SSR ID (必須)")
		qpkxRoot  = flag.String("qpkx-root", "", "qpkx のルートディレクトリ (必須)")
		intgRoot  = flag.String("intg-root", "", "intg の出力先ルートディレクトリ (必須)")
		fromStr   = flag.String("from", "", "開始時刻 JST, 例 2026-06-10T00:00 (必須)")
		toStr     = flag.String("to", "", "終了時刻 JST, この時刻は含まない (必須)")
		sortInput = flag.Bool("sort-input", true, "qpkx 読み込み後にタイムスタンプで安定ソートする")
		appendOut = flag.Bool("append", false, "既存の intg を切り詰めず常に追記する（別々に解析した期間を継ぎ足す用）")
		showStats = flag.Bool("stats", false, "棄却理由別の件数と処理時間を出力する")
		verbose   = flag.Bool("v", false, "棄却の詳細をログに出す")
	)
	flag.Parse()

	for name, v := range map[string]*string{
		"-config": cfgPath, "-station": stationID, "-ssr": ssrID,
		"-qpkx-root": qpkxRoot, "-intg-root": intgRoot,
		"-from": fromStr, "-to": toStr,
	} {
		if *v == "" {
			flag.Usage()
			return fmt.Errorf("%s は必須です", name)
		}
	}

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	from, err := time.ParseInLocation(timeLayout, *fromStr, nanotime.JST)
	if err != nil {
		return fmt.Errorf("-from の解析に失敗（%s 形式で指定）: %w", timeLayout, err)
	}
	to, err := time.ParseInLocation(timeLayout, *toStr, nanotime.JST)
	if err != nil {
		return fmt.Errorf("-to の解析に失敗（%s 形式で指定）: %w", timeLayout, err)
	}
	if !to.After(from) {
		return fmt.Errorf("-to (%s) は -from (%s) より後である必要があります", *toStr, *fromStr)
	}

	cfg, err := config.Load(*cfgPath)
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

	res, err := pipeline.Run(pipeline.Options{
		SSR:       ssr,
		Station:   station,
		QpkxRoot:  *qpkxRoot,
		IntgRoot:  *intgRoot,
		From:      from,
		To:        to,
		SortInput: *sortInput,
		Append:    *appendOut,
		Log:       log,
	})
	if err != nil {
		return err
	}

	if *showStats {
		printStats(res.Stats, res.Timing)
	}
	return nil
}

func printStats(s analyze.Stats, t pipeline.Timing) {
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

	if t.Blocks == 0 {
		return
	}
	fmt.Printf("\n--- 処理時間 (analyze のみ) ---\n")
	fmt.Printf("mean %v, min %v, max %v, 合計 %v (%d ブロック)\n",
		t.Mean().Round(time.Microsecond), t.Min.Round(time.Microsecond),
		t.Max.Round(time.Microsecond), t.Total.Round(time.Millisecond), t.Blocks)
}
