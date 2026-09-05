// Command interrogator は qpkx（質問データ）を読み、ドウェルを検出して
// intg（質問予定表）を書き出す。移植元の main.py の test() に対応する。
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"time"

	"pssrx/internal/analyze"
	"pssrx/internal/config"
	"pssrx/internal/nanotime"
	"pssrx/internal/pipeline"
)

// timeLayout は -from / -to の書式。ファイル名と同じ 12 桁の数字列は
// 誤りに気づきにくいので ISO 風に取る。タイムゾーンは JST 固定で、
// qpkx / intg のファイル名がその前提で組まれているためオフセット指定は許さない。
const timeLayout = "2006-01-02T15:04"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		cfgPath   = flag.String("config", "", "SSR・測定局の設定 YAML (必須)")
		qpkxRoot  = flag.String("qpkx-root", "", "qpkx のルートディレクトリ (必須)")
		intgRoot  = flag.String("intg-root", "", "intg の出力先ルートディレクトリ (必須)")
		fromStr   = flag.String("from", "", "開始時刻 JST, 例 2026-06-10T00:00 (必須)")
		toStr     = flag.String("to", "", "終了時刻 JST, この時刻は含まない (必須)")
		sortInput = flag.Bool("sort-input", true, "qpkx 読み込み後にタイムスタンプで安定ソートする")
		appendOut = flag.Bool("append", false, "既存の intg を切り詰めず追記する（移植元と同じ挙動）")
		showStats = flag.Bool("stats", false, "棄却理由別の件数と処理時間を出力する")
		verbose   = flag.Bool("v", false, "棄却の詳細をログに出す")
	)
	flag.Parse()

	for name, v := range map[string]*string{
		"-config": cfgPath, "-qpkx-root": qpkxRoot, "-intg-root": intgRoot,
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

	res, err := pipeline.Run(pipeline.Options{
		Config:    cfg,
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
		printStats(res.Stats, res.Elapsed)
	}
	return nil
}

func printStats(s analyze.Stats, elapsed []float64) {
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

	if len(elapsed) == 0 {
		return
	}
	sorted := append([]float64(nil), elapsed...)
	sort.Float64s(sorted)
	var sum float64
	for _, v := range elapsed {
		sum += v
	}
	fmt.Printf("\n--- 処理時間 (analyze のみ) ---\n")
	fmt.Printf("mean %.6f sec, min %.6f, max %.6f, 合計 %.3f sec (%d ブロック)\n",
		sum/float64(len(elapsed)), sorted[0], sorted[len(sorted)-1], sum, len(elapsed))
}
