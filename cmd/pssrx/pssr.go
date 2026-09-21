package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"pssrx/internal/archive"
	"pssrx/internal/pipeline"
	"pssrx/internal/pssr/sink"
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
		outPath   = fs.String("out", "", "位置を 1 つの CSV で書き出すパス。省略時は書かない")
		outDir    = fs.String("out-dir", "", "位置をフライトごとの CSV で書き出すディレクトリ。省略時は書かない。-out と併用できる")
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
	var sinks sink.Multi
	if *outPath != "" {
		f, err := os.Create(*outPath)
		if err != nil {
			return err
		}
		cs, err := sink.NewCSVSink(f, f, ssr.ID, replyStation.ID)
		if err != nil {
			f.Close()
			return err
		}
		sinks = append(sinks, cs)
	}
	if *outDir != "" {
		fsk, err := sink.NewFlightSink(*outDir, ssr.ID, replyStation.ID)
		if err != nil {
			return err
		}
		sinks = append(sinks, fsk)
	}
	if len(sinks) > 0 {
		defer sinks.Close()
		stage.Sink = sinks
	}
	src := archive.FileSource{
		IntgRoot: *intgRoot, IntgSSR: ssr.ID, IntgLeadNs: stage.Params.Plot.TauMaxNs,
		ApkxRoot: *dataRoot, ApkxStation: replyStation.ID,
		From: from, To: to,
	}
	res, err := pipeline.RunPSSR(src.Blocks(), stage)
	if err != nil {
		return err
	}
	if *showStats {
		printPSSRStats(res)
	}
	return nil
}

func printPSSRStats(res *pipeline.PSSRResult) {
	s, t := res.Plot, res.Timing
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
	b := res.Bistatic
	fmt.Printf("  双基地距離が不正      %d\n", b.Inconsistent)
	fmt.Printf("  基線特異点            %d\n", b.Baseline)
	fmt.Printf("  解が 2 つで曖昧       %d\n", b.Ambiguous)
	fmt.Printf("  高さに解が無い        %d\n", b.NoSolution)
	fmt.Printf("  曲率反復が非収束      %d\n", b.NonConvergent)
	fmt.Printf("位置                    %d\n", b.Fixes)
	k := res.Tracking
	fmt.Printf("便                      %d (確定 %d)\n", k.Tracks, k.TracksConfirmed)
	fmt.Printf("  確定した便の点        %d\n", k.FixesOK)
	fmt.Printf("  確定しなかった便の点  %d\n", k.FixesUnconfirmed)
	fmt.Printf("  保留の最大            %d\n", k.TrackHeldMax)
	fmt.Printf("フライト                %d (連結した便 %d、連結で採用した点 %d、同時最大 %d)\n", k.Flights, k.Links, k.LinkedRescued, k.FlightsOpenMax)
	fmt.Printf("同じ機体の組            %d (確定 %d: 存在区間で解決 %d、決められず %d)\n", k.ResolvePairs, k.ResolveConfirmed, k.ResolvedByContinuity, k.ResolveAmbiguous)
	fmt.Printf("  像の点                %d\n", k.FixesEcho)
	fmt.Printf("  決められない点        %d\n", k.FixesAmbiguous)
	fmt.Printf("  保留の最大            %d\n", k.ResolveHeldMax)
	fmt.Printf("平滑化                  フライト %d、点 %d、更新 %d (NIS 平均 %.2f、99%% 点超 %d、共分散を膨らませた %d)\n",
		k.SmoothedFlights, k.SmoothedFixes, k.SmoothUpdates, k.SmoothNISSum/float64(max(k.SmoothUpdates, 1)), k.SmoothNISOver99, k.SmoothInflated)
	fmt.Printf("  保留の最大            %d\n", k.SmoothHeldMax)

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
