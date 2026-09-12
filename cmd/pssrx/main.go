// Command pssrx は測定局の受信データから SSR の質問予定表を作り（interrogator）、
// 応答信号から機体の位置を推定する（pssr）。段ごとにサブコマンドを持つ。
//
//	pssrx interrogator  qpkx -> intg
//	pssrx pssr          intg + apkx -> プロット（ファイル経由の暫定）
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"pssrx/internal/config"
	"pssrx/internal/nanotime"
)

// timeLayout は -from / -to の書式。ファイル名と同じ 12 桁の数字列だと
// 打ち間違いに気づきにくいので ISO 風に取る。タイムゾーンは JST 固定。
// 入出力のファイル名が JST 前提で組まれているため、オフセット付きの
// 指定を許すと混乱するだけになる。
const timeLayout = "2006-01-02T15:04"

var subcommands = map[string]func(args []string) error{
	"interrogator": runInterrogator,
	"pssr":         runPSSR,
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	run, ok := subcommands[os.Args[1]]
	if !ok {
		fmt.Fprintf(os.Stderr, "不明なサブコマンド: %s\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err := run(os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "使い方: pssrx <interrogator|pssr> [オプション]")
	fmt.Fprintln(os.Stderr, "  interrogator  qpkx から SSR の質問予定表 intg を作る")
	fmt.Fprintln(os.Stderr, "  pssr          intg と apkx から応答を対応づけてプロットを作る")
	fmt.Fprintln(os.Stderr, "各サブコマンドのオプションは pssrx <サブコマンド> -h で見る")
}

// common はサブコマンドが共有するオプション。
type common struct {
	cfgPath string
	fromStr string
	toStr   string
	verbose bool
}

func (c *common) register(fs *flag.FlagSet) {
	fs.StringVar(&c.cfgPath, "config", "", "SSR・測定局のマスタ YAML (必須)")
	fs.StringVar(&c.fromStr, "from", "", "開始時刻 JST, 例 2026-06-10T00:00 (必須)")
	fs.StringVar(&c.toStr, "to", "", "終了時刻 JST, この時刻は含まない (必須)")
	fs.BoolVar(&c.verbose, "v", false, "棄却の詳細をログに出す")
}

// parse は共通オプションを検証し、設定・期間・ロガーを返す。
func (c *common) parse(fs *flag.FlagSet, required map[string]*string) (*config.File, time.Time, time.Time, *slog.Logger, error) {
	required["-config"] = &c.cfgPath
	required["-from"] = &c.fromStr
	required["-to"] = &c.toStr
	for name, v := range required {
		if *v == "" {
			fs.Usage()
			return nil, time.Time{}, time.Time{}, nil, fmt.Errorf("%s は必須です", name)
		}
	}
	level := slog.LevelInfo
	if c.verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	from, err := time.ParseInLocation(timeLayout, c.fromStr, nanotime.JST)
	if err != nil {
		return nil, time.Time{}, time.Time{}, nil, fmt.Errorf("-from の解析に失敗（%s 形式で指定）: %w", timeLayout, err)
	}
	to, err := time.ParseInLocation(timeLayout, c.toStr, nanotime.JST)
	if err != nil {
		return nil, time.Time{}, time.Time{}, nil, fmt.Errorf("-to の解析に失敗（%s 形式で指定）: %w", timeLayout, err)
	}
	if !to.After(from) {
		return nil, time.Time{}, time.Time{}, nil, fmt.Errorf("-to (%s) は -from (%s) より後である必要があります", c.toStr, c.fromStr)
	}
	cfg, err := config.Load(c.cfgPath)
	if err != nil {
		return nil, time.Time{}, time.Time{}, nil, err
	}
	return cfg, from, to, log, nil
}
