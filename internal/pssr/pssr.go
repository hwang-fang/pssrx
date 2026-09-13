// Package pssr は測定局で受信した Mode A/C 応答から機体の位置を推定する。
//
// 処理は決まった手続きの直列で、pipeline が 1 分ブロックごとに順に呼ぶ。
//
//	Pair      応答を質問予定と対応づけ、機体ごと・ドウェルごとのプロットにする
//	Suppress  サイドローブと反射による幽霊プロットを落とす
//	Locate    双基地距離・ビーム方位・気圧高度から位置を解く
//	Sink      位置を書く
//
// ブロックをまたいで持ち越す記録は PairState（開いている列と保留中の応答）
// と SuppressState（判定待ちのプロット）だけで、手続きはそれらを引数に
// 取る関数として書いてある。件数の集計は呼び出し側が渡す Stats に足す。
package pssr

import (
	"fmt"

	"pssrx/internal/pattern"
	"pssrx/internal/store"
)

// 質問種別のデータ上のコード。応答符号の意味（スコークか高度か）を決める。
var (
	ModeA = pattern.ModeCode['A']
	ModeC = pattern.ModeCode['C']
)

// Params は局と SSR の組に固有の値。設定からの導出は pipeline が担う。
type Params struct {
	SSRID     string
	StationID string // 応答を受信した局
	// TauMinNs は取りうる最小の遅延 [ns]。応答遅延 + 基線長 / c。
	// これより短い経路は無いので、遅延がこれを下回る質問は候補にならない。
	TauMinNs int64
	// TauMaxNs は取りうる最大の遅延 [ns]。応答遅延 + (2·覆域 + 基線長) / c。
	// これを超える応答は捨てる。PRI より小さくなければ前後の質問と取り違える。
	TauMaxNs int64
	// AroundTimeNs は SSR の走査周期 [ns]。同じ走査のプロットの判定に使う。
	AroundTimeNs int64
	// MaxRangeM は SSR の覆域 [m]。位置の探索範囲の上限に使う。
	MaxRangeM float64
}

// Config は手続きの定数。局や SSR によらない。
type Config struct {
	// TauToleranceNs は同じ列とみなす τ の差の上限 [ns]。応答遅延
	// （config.TransponderDelayNs）の公差 ±0.5 µs が支配的で、1 ドウェル内の
	// 機体の移動は 100 ns に満たない。
	TauToleranceNs int64
	// MaxGap は列の途中で応答の無い質問を何回まで許すか。これを超えて
	// 途切れたら列を閉じる。
	MaxGap int
	// MinReplies は列として残す最小の応答数。これ未満は FRUIT とみなして捨てる。
	MinReplies int

	// 以下は幽霊抑圧（Suppress）の閾値。
	//
	// SameScanFraction は同じ走査とみなす時刻差の上限を走査周期に対する
	// 割合で表す。反射体の方向は機体と無関係なので方位では絞らない。
	SameScanFraction float64
	// AltitudeToleranceFt は同じ機体とみなす高度差の上限 [ft]。同じ走査の
	// 中でも最大 2 秒ずれるので、上昇・降下中は 100 ft の境界をまたぐ。
	AltitudeToleranceFt int
	// DirectTauToleranceNs は直接照射（主ビーム・サイドローブ・ガーブルで
	// 割れた列）とみなす τ の幅 [ns]。同じ走査内の機体の移動（2 秒 ×
	// 250 m/s ≈ 1.7 µs）を吸収し、反射（経路差 6〜30 km ≈ 20〜100 µs）
	// とは十分離れる値にする。
	DirectTauToleranceNs int64
}

// DefaultConfig は既定の定数。実データの分布を見て調整する。
func DefaultConfig() Config {
	return Config{
		TauToleranceNs: 1000, MaxGap: 2, MinReplies: 3,
		SameScanFraction: 0.75, AltitudeToleranceFt: 200, DirectTauToleranceNs: 5000,
	}
}

// Validate は Params と Config の整合を検査する。手続きに入る前に 1 度呼ぶ。
func Validate(params Params, cfg Config) error {
	if params.TauMinNs < 0 || params.TauMaxNs <= params.TauMinNs {
		return fmt.Errorf("遅延の窓が不正: TauMin=%d TauMax=%d", params.TauMinNs, params.TauMaxNs)
	}
	if params.AroundTimeNs <= 0 {
		return fmt.Errorf("走査周期が不正: %d", params.AroundTimeNs)
	}
	if params.MaxRangeM <= 0 {
		return fmt.Errorf("覆域が不正: %g", params.MaxRangeM)
	}
	if cfg.TauToleranceNs <= 0 || cfg.MaxGap < 0 || cfg.MinReplies < 1 {
		return fmt.Errorf("対応づけの定数が不正: %+v", cfg)
	}
	if !(cfg.SameScanFraction > 0 && cfg.SameScanFraction < 1) || cfg.AltitudeToleranceFt < 0 || cfg.DirectTauToleranceNs <= 0 {
		return fmt.Errorf("抑圧の定数が不正: %+v", cfg)
	}
	return nil
}

// PairedReply は質問と対応づいた応答 1 件。
type PairedReply struct {
	Interrogation store.Intg
	Reply         store.AData
	TauNs         int64 // 受信時刻 − 質問時刻
}

// Plot は 1 機体 × 1 ドウェルの要約。
type Plot struct {
	SSRID     string
	StationID string
	Timestamp int64   // 列の最初と最後の質問時刻の中点 [ns]
	Azimuth   float64 // 列の最初と最後の質問方位の中点 [rad], [0, 2pi)
	TauNs     int64   // τ の平均（偶数丸め）
	ModeA     uint16  // Mode A 応答の生符号。HasModeA が偽なら無効
	HasModeA  bool
	Squawk    uint16   // ModeA を復号したスコーク（8 進 4 桁）
	ModeC     []uint16 // Mode C 応答の生符号。出現順。列の中で変わりうる
	// AltitudeFt は列の Mode C 応答から決めた気圧高度 [ft]。
	// 復号できる符号の高度が 100 ft 以内に収まるとき、プロット時刻に
	// 最も近い応答の高度をとる。決まらない列はプロットにしない。
	AltitudeFt int
	Replies    []PairedReply
}

// Stats は全段の件数。呼び出し側が持ち、各手続きに渡して足し込む。
type Stats struct {
	// Pair
	Replies         int // 受け取った応答
	NoInterrogation int // 遡れる質問が無い（期間の先頭や質問予定の欠け）
	AboveMax        int // 遅延が TauMax を超えた
	Paired          int // 質問と対応づいた
	Runs            int // 閉じた列
	RunsTooShort    int // 閉じたが MinReplies 未満で捨てた
	NoAltitude      int // Mode C 応答が無い、または全部復号できない
	AltitudeSpread  int // 復号した高度が 100 ft を超えて散っている（ガーブル）
	Plots           int
	Tau             Histogram // τ の分布。窓（TauMin, TauMax）の妥当性を見る

	// Suppress
	Sidelobe  int // 直接照射の候補のうち応答数で負けた
	Multipath int // τ が直接照射より大きい
	Kept      int

	// Locate
	TooClose   int // 双基地和が基線の内側の最小値より小さい
	OutOfRange int // 覆域の外まで探しても双基地和に届かない
	Fixes      int
}

// NewStats は τ の分布のビンを窓に合わせて用意した Stats を返す。
func NewStats(params Params) Stats {
	const bins = 64
	return Stats{Tau: Histogram{
		// 上限の 1.25 倍までを見る。窓のすぐ外に何があるかを確かめるため
		BinNs:  (params.TauMaxNs*5/4 + bins - 1) / bins,
		Counts: make([]int, bins),
	}}
}

// Histogram は固定幅のビンの度数分布。
type Histogram struct {
	BinNs  int64
	Counts []int // [k*BinNs, (k+1)*BinNs)
	Over   int
}

func histogramAdd(h *Histogram, v int64) {
	if h.BinNs <= 0 {
		return
	}
	k := v / h.BinNs
	if k < 0 || k >= int64(len(h.Counts)) {
		h.Over++
		return
	}
	h.Counts[k]++
}

func absInt64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func compareInt64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
