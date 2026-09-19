package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"pssrx/internal/archive"
	"pssrx/internal/pipeline"
	"pssrx/internal/pssr"
)

// runPSSR は intg（質問予定表）と apkx（応答データ）を読み、応答を質問に
// 対応づけてプロットを作る。ファイル経由の暫定実装。
func runPSSR(args []string) error {
	fs := flag.NewFlagSet("pssrx pssr", flag.ContinueOnError)
	var c common
	c.register(fs)
	var (
		ssrID     = fs.String("ssr", "", "処理対象の SSR ID (必須)")
		stationID = fs.String("station", "", "質問解析局の ID。intg を作った局 (必須)")
		replySt   = fs.String("reply-stations", "", "応答局の ID。省略時は質問解析局と同じ局の単局計算")
		intgRoot  = fs.String("intg-root", "", "intg のルートディレクトリ (必須)")
		dataRoot  = fs.String("data-root", "", "局データ（apkx）のルートディレクトリ。qpkx と同じ (必須)")
		showStats = fs.Bool("stats", false, "対応づけ・抑圧・位置推定の件数と τ の分布を出力する")
		outPath   = fs.String("out", "", "位置を CSV で書き出すパス。省略時は出力しない")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, from, to, log, err := c.parse(fs, map[string]*string{
		"-ssr": ssrID, "-station": stationID, "-intg-root": intgRoot, "-data-root": dataRoot,
	})
	if err != nil {
		return err
	}
	ssr, err := cfg.SSR(*ssrID)
	if err != nil {
		return err
	}
	station, err := cfg.Station(*stationID)
	if err != nil {
		return err
	}
	replyStation := station
	if *replySt != "" {
		ids := strings.Split(*replySt, ",")
		if len(ids) > 1 {
			return fmt.Errorf("-reply-stations に複数の局は指定できません（多局の統合は未実装）: %s", *replySt)
		}
		if replyStation, err = cfg.Station(ids[0]); err != nil {
			return err
		}
	}

	stage, err := pipeline.NewPSSRStage(ssr, replyStation, cfg.Analysis.PSSR)
	if err != nil {
		return err
	}
	stage.Log = log
	if *outPath != "" {
		f, err := os.Create(*outPath)
		if err != nil {
			return err
		}
		cs, err := pssr.NewCSVSink(f, f, ssr.ID, replyStation.ID)
		if err != nil {
			f.Close()
			return err
		}
		defer cs.Close()
		stage.Sink = cs
	}
	src := archive.FileSource{
		IntgRoot: *intgRoot, IntgSSR: ssr.ID, IntgLeadNs: stage.Params.TauMaxNs,
		ApkxRoot: *dataRoot, ApkxStation: replyStation.ID,
		From: from, To: to,
	}
	res, err := pipeline.RunPSSR(src.Blocks(), stage)
	if err != nil {
		return err
	}
	if *showStats {
		printPSSRStats(res.Stats, res.Timing)
	}
	return nil
}

func printPSSRStats(s pssr.Stats, t pipeline.Timing) {
	fmt.Printf("\n--- 対応づけ結果 ---\n")
	fmt.Printf("応答                    %d\n", s.Replies)
	fmt.Printf("  投入時に捨てた        %d (質問予定 %d)\n", s.DroppedReplies, s.DroppedIntg)
	fmt.Printf("  対にならず            %d\n", s.Unpaired)
	fmt.Printf("  質問と対応            %d\n", s.Paired)
	fmt.Printf("列                      %d\n", s.Runs)
	fmt.Printf("  応答 1 件だけ         %d (%.1f%%)\n", s.RunsSingle, 100*float64(s.RunsSingle)/float64(max(s.Runs, 1)))
	fmt.Printf("  短く棄却              %d\n", s.RunsTooShort)
	fmt.Printf("  Mode A 無しで棄却     %d\n", s.NoModeA)
	fmt.Printf("  高度無しで棄却        %d\n", s.NoAltitude)
	fmt.Printf("  高度が散って棄却      %d\n", s.AltitudeSpread)
	fmt.Printf("  高度が上限超で棄却    %d\n", s.AltitudeTooHigh)
	fmt.Printf("プロット                %d\n", s.Plots)
	fmt.Printf("開いている列の平均      %.1f (割り当て %d 回)\n", float64(s.OpenRunsTotal)/float64(max(s.Assignments, 1)), s.Assignments)
	fmt.Printf("  サイドローブとして抑圧 %d\n", s.Sidelobe)
	fmt.Printf("  反射として抑圧        %d\n", s.Multipath)
	fmt.Printf("残ったプロット          %d\n", s.Kept)
	fmt.Printf("  双基地距離が不正      %d\n", s.Inconsistent)
	fmt.Printf("  基線特異点            %d\n", s.Baseline)
	fmt.Printf("  解が 2 つで曖昧       %d\n", s.Ambiguous)
	fmt.Printf("  高さに解が無い        %d\n", s.NoSolution)
	fmt.Printf("  曲率反復が非収束      %d\n", s.NonConvergent)
	fmt.Printf("位置                    %d\n", s.Fixes)

	fmt.Printf("\n--- τ の分布 (bin = %d ns) ---\n", s.Tau.BinNs)
	total := s.Tau.Over
	peak := 0
	for _, c := range s.Tau.Counts {
		total += c
		peak = max(peak, c)
	}
	for k, c := range s.Tau.Counts {
		if c == 0 {
			continue
		}
		bar := strings.Repeat("#", c*50/max(peak, 1))
		fmt.Printf("%8d µs %8d %s\n", int64(k)*s.Tau.BinNs/1000, c, bar)
	}
	if s.Tau.Over > 0 {
		fmt.Printf("    上限超 %8d\n", s.Tau.Over)
	}
	printTiming(t)
}
