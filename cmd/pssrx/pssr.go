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

// runPSSR は intg ファイル（質問予定表）と apkx（応答データ）を読み、応答を
// 質問に対応づけて位置を出す。intg からの再処理用で、同じ入力なら run と
// バイト単位で同じ位置を出す。
func runPSSR(args []string) (err error) {
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
		outs      outputs
	)
	outs.register(fs)
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
	stage.IncludeDropped = outs.includeDropped
	sinks, err := outs.open(ssr.ID, replyStation.ID)
	if err != nil {
		return err
	}
	if sinks != nil {
		// スコークごとの CSV は Close でまとめて書くので、Close の失敗を返す
		defer func() {
			if cerr := sinks.Close(); err == nil {
				err = cerr
			}
		}()
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
	fmt.Printf("航跡片                  %d (3 点以上 %d)\n", k.Tracks, k.Tracks3)
	fmt.Printf("  保留の最大            %d\n", k.TrackHeldMax)

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

// outputs は位置の出力先のフラグ。pssr と run で共通。
type outputs struct {
	path           string
	dir            string
	includeDropped bool
}

func (o *outputs) register(fs *flag.FlagSet) {
	fs.StringVar(&o.path, "out", "", "位置を 1 つの CSV で書き出すパス（解析用）。省略時は書かない")
	fs.StringVar(&o.dir, "out-dir", "", "位置をスコークごとの CSV で書き出すディレクトリ。同じスコークでも 600 s 以上離れた点は別のファイル。"+
		"空でない既存のディレクトリは拒否する。省略時は書かない。-out と併用できる")
	fs.BoolVar(&o.includeDropped, "include-dropped", false, "抑圧で落としたプロットと位置の解けなかったプロットも、drop 列に理由を入れて CSV に含める（デバッグ用）")
}

// open は指定された出力先を開く。何も指定されていなければ nil。
func (o *outputs) open(ssrID, stationID string) (sink.Sink, error) {
	var sinks sink.Multi
	if o.dir != "" {
		if ents, err := os.ReadDir(o.dir); err == nil && len(ents) > 0 {
			return nil, fmt.Errorf("-out-dir %s が空ではありません（既存のファイルを上書きしないため）", o.dir)
		}
		sq, err := sink.NewSquawkSink(o.dir, ssrID, stationID)
		if err != nil {
			return nil, err
		}
		sinks = append(sinks, sq)
	}
	if o.path != "" {
		f, err := os.Create(o.path)
		if err != nil {
			return nil, err
		}
		cs, err := sink.NewCSVSink(f, f, ssrID, stationID)
		if err != nil {
			f.Close()
			return nil, err
		}
		sinks = append(sinks, cs)
	}
	if len(sinks) == 0 {
		return nil, nil
	}
	return sinks, nil
}
