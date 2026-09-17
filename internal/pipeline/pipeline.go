// Package pipeline はブロック単位の解析ループをまとめる。
//
// CLI と回帰テストが同じ経路を通るようにパッケージへ切り出してある。
// テストが CLI の内部を再実装すると、両者がずれたときに検出できない。
//
// 入口は 2 段になっている。
//
//	config -> Stage   設定から解析パラメータと局の幾何を導く（変わりうる I/F）
//	Stage  -> Result  ブロックを受け取り、解析し、書き出す（固定した解析本体）
//
// ゴールデンテストは Stage から入る。設定の書式や座標の扱いが変わっても、
// 解析本体が同じ値を受け取る限りゴールデンは変えずに済む。
//
// 入力は Source（ブロックの列）で、出力は IntgSink / pssr.Sink。どこから
// 読みどこへ書くかは呼び出し側が決め、pipeline はブロックに段を当てる
// だけ。ファイルからは store.FileSource が 1 分 1 ファイルを 1 ブロックにする。
// 実時間化では受信側が「ここまで揃った」ブロックを作って渡せばよく、
// 段の呼び出し順はこのパッケージの 1 箇所に残る。
package pipeline

import (
	"iter"
	"time"

	"pssrx/internal/store"
)

// Source はブロックの列。[Start, End) のデータが揃ったブロックを時刻順に
// yield する。エラーを yield したら解析は止まる。
type Source = iter.Seq2[store.Block, error]

// IntgSink は質問予定表の出力先。store.IntgDir が満たす。
type IntgSink interface {
	Save(ssrID string, data []store.Intg) error
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
