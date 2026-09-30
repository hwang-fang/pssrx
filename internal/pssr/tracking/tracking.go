// Package tracking は位置の列を航跡片にまとめる。位置がどの幾何で解かれたかは
// 知らず、Fix（位置・共分散・スコーク・高度と、主となる測定点の観測量）
// だけを見る。
//
//	Track  位置を連続性で航跡片に繋ぎ、航跡片 ID と航跡片の中での順番を付ける
//
// 実時間で行える処理に限る。点を航跡片に繋ぐかは過去の点だけで決まり、
// 点は繋いだら（同じ走査の競合を見る半走査の後に）出す。確定・棄却・像・
// 便・平滑化といった、未来や便の全体を見る判定は後続の仕事で、ここでは
// 行わない。
//
// 持ち越す記録は State で、手続きはそれを引数に取る関数として書いてある。
// 件数の集計は呼び出し側が渡す Stats に足す。
package tracking

import "fmt"

// Params は局と SSR の組に固有の値。設定からの導出は pipeline が担う。
type Params struct {
	// AroundTimeNs は SSR の走査周期 [ns]。点の間隔の単位。
	AroundTimeNs int64
}

// Config は手続きの定数。局や SSR によらない。
type Config struct {
	// TrackMaxSpeedMps は門の速度上限 [m/s]。前の点からの移動がこれ × Δt に
	// 位置の誤差を足した幅を超えたら別の航跡片。
	TrackMaxSpeedMps float64
	// TrackMaxClimbFtps は門の高度変化率の上限 [ft/s]。6,000 ft/min。
	TrackMaxClimbFtps float64
	// TrackGateSigmas は門に足す位置の標準偏差の倍率。
	TrackGateSigmas float64
	// TrackMaxMissedScans は航跡片を打ち切らずに許す欠測の走査数。
	TrackMaxMissedScans int
}

// DefaultConfig は既定の定数。実データの分布を見て調整する。
func DefaultConfig() Config {
	return Config{TrackMaxSpeedMps: 350, TrackMaxClimbFtps: 100, TrackGateSigmas: 3, TrackMaxMissedScans: 2}
}

// Validate は Params と Config の整合を検査する。手続きに入る前に 1 度呼ぶ。
func Validate(params Params, cfg Config) error {
	if params.AroundTimeNs <= 0 {
		return fmt.Errorf("走査周期が不正: %d", params.AroundTimeNs)
	}
	if cfg.TrackMaxSpeedMps <= 0 || cfg.TrackMaxClimbFtps < 0 || cfg.TrackGateSigmas < 0 || cfg.TrackMaxMissedScans < 0 {
		return fmt.Errorf("連続性の定数が不正: %+v", cfg)
	}
	return nil
}

// Stats は件数。呼び出し側が持ち、各手続きに渡して足し込む。
type Stats struct {
	Tracks       int // 開いた航跡片
	Tracks3      int // 打ち切りまでに 3 点以上になった航跡片（FRUIT の偶然の一致との目安）
	TrackHeldMax int // 保留した点の最大件数。常駐運転で増え続けないことの確認用
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
