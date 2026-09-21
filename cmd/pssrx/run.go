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

// runBoth は interrogator 段と pssr 段をメモリで直列に流す。
func runBoth(args []string) error {
	fs := flag.NewFlagSet("pssrx run", flag.ContinueOnError)
	var c common
	c.register(fs)
	var (
		ssrID     = fs.String("ssr", "", "処理対象の SSR ID (必須)")
		stationID = fs.String("station", "", "質問解析局の ID (必須)")
		replySt   = fs.String("reply-stations", "", "応答局の ID。省略時は質問解析局と同じ局の単局計算")
		dataRoot  = fs.String("data-root", "", "局データ（qpkx / apkx）のルートディレクトリ (必須)")
		intgRoot  = fs.String("intg-root", "", "intg の出力先ルートディレクトリ。省略時は intg を書かない")
		outPath   = fs.String("out", "", "位置を 1 つの CSV で書き出すパス。省略時は書かない")
		outDir    = fs.String("out-dir", "", "位置をフライトごとの CSV で書き出すディレクトリ。省略時は書かない。-out と併用できる")
		appendOut = fs.Bool("append", false, "既存の intg を切り詰めず常に追記する")
		showStats = fs.Bool("stats", false, "両段の件数と処理時間を出力する")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, from, to, log, err := c.parse(fs, map[string]*string{
		"-ssr": ssrID, "-station": stationID, "-data-root": dataRoot,
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

	is, err := pipeline.NewInterrogatorStage(ssr, station, cfg.Analysis.Interrogator)
	if err != nil {
		return err
	}
	is.Log = log
	if *intgRoot != "" {
		is.Intg = &archive.IntgDir{Root: *intgRoot, Append: *appendOut, Log: log}
	}
	ps, err := pipeline.NewPSSRStage(ssr, replyStation, cfg.Analysis.PSSR)
	if err != nil {
		return err
	}
	ps.Log = log
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
		ps.Sink = sinks
	}

	src := archive.FileSource{
		QpkxRoot: *dataRoot, QpkxStation: station.ID,
		ApkxRoot: *dataRoot, ApkxStation: replyStation.ID,
		From: from, To: to,
	}
	res, err := pipeline.Run(src.Blocks(), is, ps)
	if err != nil {
		return err
	}
	if *showStats {
		printInterrogatorStats(res.Interrogator.Stats, res.Interrogator.Timing)
		printPSSRStats(&res.PSSR)
	}
	return nil
}
