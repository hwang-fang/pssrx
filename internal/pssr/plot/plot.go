// Package plot は測定局で受信した Mode A/C 応答を、SSR の質問予定と
// 対応づけて 1 機体 × 1 ドウェルのプロットにする。単一測定点の処理で、
// 多点ではこれを測定点の数だけ並べる。
//
//	Synchronizer  質問予定と応答を時刻同期し、閉じた形で処理できる範囲を切り出す
//	Pair          応答を質問予定と対応づけ、列にまとめてプロットにする
//	Suppress      サイドローブと反射による幽霊プロットを落とす
//
// 持ち越す記録は Synchronizer（時刻同期の待ち行列）、RunState（開いている
// 列）、SuppressState（判定待ちのプロット）で、手続きはそれらを引数に取る
// 関数として書いてある。件数の集計は呼び出し側が渡す Stats に足す。
package plot

import (
	"fmt"
	"math"

	"pssrx/internal/record"
	"pssrx/internal/ssr"
)

// 質問種別のデータ上のコード。応答符号の意味（スコークか高度か）を決める。
var (
	ModeA = ssr.ModeCode['A']
	ModeC = ssr.ModeCode['C']
)

// Params は局と SSR の組に固有の値。設定からの導出は pipeline が担う。
type Params struct {
	// TauMinNs は取りうる最小の遅延 [ns]。応答遅延 + 基線長 / c。
	// これより短い経路は無いので、遅延がこれを下回る質問は候補にならない。
	TauMinNs int64
	// TauMaxNs は取りうる最大の遅延 [ns]。応答遅延 + (2·覆域 + 基線長) / c。
	// これを超える応答は捨てる。PRI より小さくなければ前後の質問と取り違える。
	TauMaxNs int64
	// AroundTimeNs は SSR の走査周期 [ns]。同じ走査のプロットの判定に使う。
	AroundTimeNs int64
}

// Config は手続きの定数。局や SSR によらない。
type Config struct {
	// TauToleranceNs は同じ列とみなす τ の差の上限 [ns]。応答遅延
	// （ssr.TransponderDelayNs）の公差 ±0.5 µs が支配的で、1 ドウェル内の
	// 機体の移動は 100 ns に満たない。
	TauToleranceNs int64
	// MaxGap は列の途中で応答の無い質問を何回まで許すか。これを超えて
	// 途切れたら列を閉じる。
	MaxGap int
	// MinReplies は列として残す最小の応答数。これ未満は FRUIT とみなして捨てる。
	MinReplies int
	// MaxAltitudeFt は列の気圧高度として認める上限 [ft]。Mode C の符号は
	// 126,700 ft まで表せるが、民間機は 51,000 ft までしか上がらない。
	// それより上は FRUIT の偶然の一致で組まれた列の無作為な符号とみなして
	// 捨てる。高高度の軍用機（70,000 ft 級）まで拾うなら上げる。
	MaxAltitudeFt int
	// MaxRetentionNs は Synchronizer が溜めておく時間幅の上限 [ns]。質問予定か
	// 応答の片方が止まったとき、他方が溜まり続けないための安全弁。2 分は
	// 投入の刻みの最大（1 分ファイル）の 2 倍で、投入の遅れや乱れがあっても
	// 対になる前に捨てないことがはっきりする。取り出しの後に適用する。
	MaxRetentionNs int64

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
	// ImageAzimuthSeparationRad は、τ の一致する同じ機体のプロットを
	// 「同じドウェルの断片（主ビームとサイドローブ）」と「像（SSR 近傍の
	// 反射体経由）」に分ける方位差 [rad]。以内なら応答数最多を残して併合し、
	// 超えれば両方を通して便の文脈（tracking.Resolve）で決める。
	// 実データでは断片の方位差が 2〜8° に集中し、像は 10° 以上に一様に
	// 分布する（第 1 サイドローブが 3〜6°、反射体の方向は機体と無関係）。
	// tracking.Config.ResolveAzimuthSeparationRad と同じ値にする（設定の
	// キーは 1 つで、pipeline が両方に配る）。
	ImageAzimuthSeparationRad float64
}

// DefaultConfig は既定の定数。実データの分布を見て調整する。
func DefaultConfig() Config {
	return Config{
		TauToleranceNs: 1000, MaxGap: 2, MinReplies: 3, MaxAltitudeFt: 60_000, MaxRetentionNs: 2 * 60_000_000_000,
		SameScanFraction: 0.75, AltitudeToleranceFt: 200, DirectTauToleranceNs: 5000, ImageAzimuthSeparationRad: 10 * math.Pi / 180,
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
	if cfg.TauToleranceNs <= 0 || cfg.MaxGap < 0 || cfg.MinReplies < 1 || cfg.MaxAltitudeFt <= 0 || cfg.MaxRetentionNs <= 0 {
		return fmt.Errorf("対応づけの定数が不正: %+v", cfg)
	}
	if !(cfg.SameScanFraction > 0 && cfg.SameScanFraction < 1) || cfg.AltitudeToleranceFt < 0 || cfg.DirectTauToleranceNs <= 0 ||
		cfg.ImageAzimuthSeparationRad <= 0 || cfg.ImageAzimuthSeparationRad > math.Pi {
		return fmt.Errorf("抑圧の定数が不正: %+v", cfg)
	}
	return nil
}

// PairedReply は質問と対応づいた応答 1 件。
type PairedReply struct {
	Interrogation record.Interrogation
	Reply         record.Reply
	TauNs         int64 // 受信時刻 − 質問時刻
}

// Plot は 1 機体 × 1 ドウェルの要約。どの SSR・局の処理かはプロットごとの
// 情報ではなく処理の文脈（Params）なので持たない。
type Plot struct {
	Timestamp int64   // 列の最初と最後の質問時刻の中点 [ns]
	Azimuth   float64 // 列の最初と最後の質問方位の中点 [rad], [0, 2pi)
	TauNs     int64   // τ の平均（偶数丸め）
	Squawk    uint16  // 列の Mode A 応答を復号したスコーク（8 進 4 桁）
	// AltitudeFt は列の Mode C 応答から決めた気圧高度 [ft]。
	// 復号できる符号の高度が 100 ft 以内に収まるとき、プロット時刻に
	// 最も近い応答の高度をとる。決まらない列はプロットにしない。
	AltitudeFt int
	// Replies は元の応答列。生の応答符号はここから復号し直せる。
	Replies []PairedReply
}

// Stats は件数。呼び出し側が持ち、各手続きに渡して足し込む。
type Stats struct {
	// Synchronizer
	Replies        int // 受け取った応答
	DroppedIntg    int // 取り出し済みより古い、または保持幅を超えて捨てた質問予定
	DroppedReplies int // 同じく応答

	// Pair
	Unpaired        int // どの質問とも対にならない（TauMax 超、または遡る質問が無い）
	Paired          int // 質問と対応づいた
	Runs            int // 閉じた列
	RunsSingle      int // 閉じた列のうち応答が 1 件だけのもの（FRUIT の孤立応答）
	RunsTooShort    int // 閉じたが MinReplies 未満で捨てた
	NoModeA         int // Mode A 応答が無い（スコークが決まらない）
	NoAltitude      int // Mode C 応答が無い、または全部復号できない
	AltitudeSpread  int // 復号した高度が 100 ft を超えて散っている（ガーブル）
	AltitudeTooHigh int // 高度が MaxAltitudeFt を超える（FRUIT の偶然の一致）
	Plots           int
	Tau             Histogram // τ の分布。窓（TauMin, TauMax）の妥当性を見る
	// Assignments と OpenRunsTotal は列の割り当ての負荷を見る。割り当て 1 回に
	// つき、そのとき開いていた列の数を OpenRunsTotal に足す。平均の開いている
	// 列数 OpenRunsTotal / Assignments と、孤立応答の割合 RunsSingle / Runs が
	// 対応づけの性能の前提（開いている列は数十本、閉じた列の大半は孤立）を
	// 満たしているかの目安になる。
	Assignments   int
	OpenRunsTotal int64

	// Suppress
	Sidelobe  int // 直接照射の候補のうち応答数で負けた
	Multipath int // τ が直接照射より大きい
	Kept      int
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
