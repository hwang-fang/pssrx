package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"time"

	"pssrx/internal/config"
	"pssrx/internal/pipeline"
	"pssrx/internal/pssr/sink"
)

// schemaVersion は出力（CSV の列と処理の記録）の版。列や意味を変えたら上げる。
const schemaVersion = 1

// outputs は位置の出力先のフラグ。pssr と run で共通。
type outputs struct {
	path           string
	dir            string
	minPoints      int
	includeDropped bool
}

func (o *outputs) register(fs *flag.FlagSet) {
	fs.StringVar(&o.dir, "out-dir", "", "スコークセッションごとの CSV を書く保管庫のディレクトリ。{ソース構成}/ の下に書き、"+
		"同じ期間・隣の期間の既存のファイルと統合する。省略時は書かない")
	fs.IntVar(&o.minPoints, "min-session-points", 3, "-out-dir で書くスコークセッションの点数の下限。1 なら全部書く")
	fs.StringVar(&o.path, "out", "", "全点を 1 つの CSV で書き出すパス（解析・デバッグ用）。省略時は書かない。-out-dir と併用できる")
	fs.BoolVar(&o.includeDropped, "include-dropped", false, "-out に、抑圧で落としたプロットと位置の解けなかったプロットも drop 列に理由を入れて含める（デバッグ用）")
}

// runContext は出力先を開くのに要る、この処理の文脈。
type runContext struct {
	ssr          config.SSR
	intgStation  config.Station // 質問予定表を作った質問解析局
	replyStation config.Station // 応答を受信した局（ソース構成の受信点）
	from, to     time.Time      // 処理の期間 [from, to)
	pssr         pipeline.PSSRStage
	interrogator *pipeline.InterrogatorStage // run のときだけ
}

// open は指定された出力先を開く。何も指定されていなければ nil。
func (o *outputs) open(rc runContext) (sink.Sink, error) {
	source := sink.SourceID(rc.ssr.ID, rc.replyStation.ID)
	ctx := sink.Context{SSR: rc.ssr.ID, Station: rc.replyStation.ID, Source: source}
	var sinks sink.Multi
	if o.dir != "" {
		dir := filepath.Join(o.dir, sink.SourceDir(source))
		run, err := newRunID(filepath.Join(dir, "runs"))
		if err != nil {
			return nil, err
		}
		ctx.Run = run
		st, err := sink.NewSessionStore(dir, ctx, rc.from.UnixNano(), rc.to.UnixNano(), o.minPoints, runMeta(rc, source))
		if err != nil {
			return nil, err
		}
		sinks = append(sinks, st)
	}
	if o.path != "" {
		if ctx.Run == "" {
			ctx.Run = time.Now().UTC().Format("20060102T150405Z")
		}
		f, err := os.Create(o.path)
		if err != nil {
			return nil, err
		}
		cs, err := sink.NewCSVSink(f, f, ctx)
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

// newRunID は処理の ID（開始時刻 UTC の秒）を作る。同じ秒の記録があれば
// 末尾に番号を付ける。
func newRunID(runsDir string) (string, error) {
	base := time.Now().UTC().Format("20060102T150405Z")
	for k := 1; k < 100; k++ {
		id := base
		if k > 1 {
			id = fmt.Sprintf("%s-%d", base, k)
		}
		if _, err := os.Stat(filepath.Join(runsDir, id+".json")); errors.Is(err, os.ErrNotExist) {
			return id, nil
		}
	}
	return "", fmt.Errorf("処理の ID を決められない（%s に同じ秒の記録が多すぎる）", runsDir)
}

// runMeta は処理の記録に書く項目。ソース構成（座標を含む）、版、引数、
// 段の定数を残し、どの処理の結果かを後から再現できるようにする。
func runMeta(rc runContext, source string) map[string]any {
	pos := func(id string, p config.Position) map[string]any {
		return map[string]any{"id": id, "lat": *p.Lat, "lon": *p.Lon, "alt_m": *p.Alt}
	}
	ssr := pos(rc.ssr.ID, rc.ssr.Position)
	ssr["max_range_m"] = rc.ssr.MaxRangeM
	ssr["around_time_sec"] = rc.ssr.Interrogation.AroundTimeSec
	meta := map[string]any{
		"schema_version": schemaVersion,
		"software":       software(),
		"args":           os.Args,
		"started_at":     time.Now().UTC().Format(time.RFC3339),
		"source_config": map[string]any{
			"id": source, "method": "pssr1（単局 PSSR: 1 つの SSR と 1 つの受信点）",
			"ssr": ssr, "receiver": pos(rc.replyStation.ID, rc.replyStation.Position),
			"interrogation_station": pos(rc.intgStation.ID, rc.intgStation.Position),
		},
		"pssr": map[string]any{"params": rc.pssr.Params, "config": rc.pssr.Config},
	}
	if is := rc.interrogator; is != nil {
		meta["interrogator"] = map[string]any{"params": is.Params, "config": is.Config, "dist_m": is.Dist, "azimuth_rad": is.Azimuth}
	}
	return meta
}

// software は実行中の pssrx の版（ビルド時の Git のリビジョンと未コミットの変更の有無）。
func software() map[string]any {
	out := map[string]any{"name": "pssrx", "go": runtime.Version()}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, kv := range bi.Settings {
			switch kv.Key {
			case "vcs.revision":
				out["revision"] = kv.Value
			case "vcs.modified":
				out["modified"] = kv.Value == "true"
			case "vcs.time":
				out["revision_time"] = kv.Value
			}
		}
	}
	return out
}
