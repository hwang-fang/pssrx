// Package tracking は位置の列を航跡にする。位置がどの幾何で解かれたかは
// 知らず、Fix（位置・共分散・スコーク・高度と、主となる測定点の観測量）
// だけを見る。
//
//	Track    位置を便（連続する点の列）に繋ぎ、確定しなかった点を棄却する
//	Link     便の断片をフライトに連結する
//	Resolve  同じ機体のフライトの重複（像）を存在区間で解消する
//	Smooth   フライトごとにカルマンフィルタで平滑化し、速度と旋回率を出す
//
// 持ち越す記録は各段の State で、手続きはそれらを引数に取る関数として
// 書いてある。件数の集計は呼び出し側が渡す Stats に足す。
package tracking

import (
	"fmt"
	"math"
)

// Params は局と SSR の組に固有の値。設定からの導出は pipeline が担う。
type Params struct {
	// AroundTimeNs は SSR の走査周期 [ns]。点の間隔の単位。
	AroundTimeNs int64
}

// Config は手続きの定数。局や SSR によらない。
type Config struct {
	// ResolveAzimuthSeparationRad は同じ機体のフライトの組を作る方位差の
	// 下限 [rad]。以内は同じ機体の直接波の断片（Link の対象）で、超えれば
	// 像の候補。plot.Config.ImageAzimuthSeparationRad と同じ値にする。
	ResolveAzimuthSeparationRad float64

	// 以下は連続性の判定（Track）の定数。
	//
	// TrackMaxSpeedMps は門の速度上限 [m/s]。前の点からの移動がこれ × Δt に
	// 位置の誤差を足した幅を超えたら別の便。
	TrackMaxSpeedMps float64
	// TrackMaxClimbFtps は門の高度変化率の上限 [ft/s]。6,000 ft/min。
	TrackMaxClimbFtps float64
	// TrackGateSigmas は門に足す位置の標準偏差の倍率。
	TrackGateSigmas float64
	// TrackMaxMissedScans は便を打ち切らずに許す欠測の走査数。
	TrackMaxMissedScans int
	// TrackConfirmHits は便を確定するのに要る点数。確定しなかった便の点は
	// 棄却する。
	TrackConfirmHits int

	// 以下は断片の連結（Link）の定数。
	//
	// LinkMaxGapNs はフライトの末尾から次の断片の先頭までに許す切れ目 [ns]。
	// 下限は Track の打ち切り幅（それより短い切れ目は Track が繋がなかった
	// もの）。
	LinkMaxGapNs int64
	// LinkVelocityToleranceMps は末尾の速度で外挿した位置に持たせる幅の
	// 速度換算 [m/s]。切れ目 × これ + 3σ が門になる。
	LinkVelocityToleranceMps float64
	// LinkClimbToleranceFtps は高度の外挿に持たせる幅の変化率換算 [ft/s]。
	LinkClimbToleranceFtps float64

	// 以下は同一機体のフライトの重複解消（Resolve）の定数。
	// 方位差は ResolveAzimuthSeparationRad（抑圧と共用）。
	//
	// ResolveTauToleranceNs は同じ機体とみなす τ の差の上限 [ns]。近傍反射の
	// 経路差（実測 p90 で 5 µs 未満）。
	ResolveTauToleranceNs int64
	// ResolveAltitudeToleranceFt は同じ機体とみなす高度差の上限 [ft]。
	ResolveAltitudeToleranceFt int
	// ResolveConfirmScans は同じ機体と決めるのに要る一致した走査数。
	ResolveConfirmScans int
	// ResolveMaxHoldScans は組の判定を待って点を保留する上限の
	// 走査数。超えた点は ambiguous として先に出す。出力の遅れの上限になる
	// ので、遅れを抑えたいときに小さくする（判定の材料が減り ambiguous が
	// 増える）。
	ResolveMaxHoldScans int

	// 以下は平滑化（Smooth）の定数。
	//
	// SmoothAccelSigmaMps2 は等速モデルの水平の加速度雑音 [m/s²]。旅客機の
	// 巡航・緩い旋回の目安。旋回中（5〜7 m/s²）は追従が遅れる。
	SmoothAccelSigmaMps2 float64
	// SmoothVerticalAccelSigmaMps2 は鉛直の加速度雑音 [m/s²]。
	SmoothVerticalAccelSigmaMps2 float64
	// SmoothInitialVelocitySigmaMps は最初の点で速度 0 に置く標準偏差 [m/s]。
	SmoothInitialVelocitySigmaMps float64
	// SmoothLagScans は固定遅延平滑化で後ろに見る走査数。点はこの幅だけ
	// 保留してから出す。
	SmoothLagScans int
	// SmoothTurnRateSigmaDps は協調旋回モデル（CT）の旋回率の白色雑音
	// [deg/s/√s]。旋回の始まり・終わりへの追従の速さ。0 なら等速モデル（CV）。
	SmoothTurnRateSigmaDps float64
	// SmoothTurnMaxRangeM は旋回を回す SSR からの距離の上限 [m]。遠方では
	// 方位の雑音で旋回率が決まらず、回すと誤る。
	SmoothTurnMaxRangeM float64
}

// DefaultConfig は既定の定数。実データの分布を見て調整する。
func DefaultConfig() Config {
	return Config{
		ResolveAzimuthSeparationRad: 10 * math.Pi / 180, TrackMaxSpeedMps: 350, TrackMaxClimbFtps: 100,
		TrackGateSigmas: 3, TrackMaxMissedScans: 2, TrackConfirmHits: 3,
		LinkMaxGapNs: 60_000_000_000, LinkVelocityToleranceMps: 60, LinkClimbToleranceFtps: 50,
		ResolveTauToleranceNs: 5000, ResolveAltitudeToleranceFt: 300, ResolveConfirmScans: 3, ResolveMaxHoldScans: 150,
		SmoothAccelSigmaMps2: 2, SmoothVerticalAccelSigmaMps2: 0.5, SmoothInitialVelocitySigmaMps: 300, SmoothLagScans: 5,
		SmoothTurnRateSigmaDps: 0.25, SmoothTurnMaxRangeM: 60_000,
	}
}

// Validate は Params と Config の整合を検査する。手続きに入る前に 1 度呼ぶ。
func Validate(params Params, cfg Config) error {
	if params.AroundTimeNs <= 0 {
		return fmt.Errorf("走査周期が不正: %d", params.AroundTimeNs)
	}
	if cfg.ResolveAzimuthSeparationRad <= 0 || cfg.ResolveAzimuthSeparationRad > math.Pi {
		return fmt.Errorf("方位差の定数が不正: %+v", cfg)
	}
	if cfg.TrackMaxSpeedMps <= 0 || cfg.TrackMaxClimbFtps < 0 || cfg.TrackGateSigmas < 0 ||
		cfg.TrackMaxMissedScans < 0 || cfg.TrackConfirmHits < 1 {
		return fmt.Errorf("連続性の定数が不正: %+v", cfg)
	}
	if cfg.LinkMaxGapNs <= 0 || cfg.LinkVelocityToleranceMps <= 0 || cfg.LinkClimbToleranceFtps < 0 {
		return fmt.Errorf("連結の定数が不正: %+v", cfg)
	}
	if cfg.ResolveTauToleranceNs <= 0 || cfg.ResolveAltitudeToleranceFt < 0 || cfg.ResolveConfirmScans < 1 || cfg.ResolveMaxHoldScans < 1 {
		return fmt.Errorf("重複解消の定数が不正: %+v", cfg)
	}
	if cfg.SmoothAccelSigmaMps2 <= 0 || cfg.SmoothVerticalAccelSigmaMps2 <= 0 || cfg.SmoothInitialVelocitySigmaMps <= 0 ||
		cfg.SmoothLagScans < 0 || cfg.SmoothTurnRateSigmaDps < 0 || cfg.SmoothTurnMaxRangeM <= 0 {
		return fmt.Errorf("平滑化の定数が不正: %+v", cfg)
	}
	return nil
}

// Stats は件数。呼び出し側が持ち、各手続きに渡して足し込む。
type Stats struct {
	// Track
	Tracks           int // 開いた便
	TracksConfirmed  int // 確定した便
	FixesOK          int // 確定した便の点
	FixesUnconfirmed int // 確定しなかった便の点（棄却）
	TrackHeldMax     int // 保留した点の最大件数。常駐運転で増え続けないことの確認用

	// Link
	Flights        int // 開いたフライト
	Links          int // 既存のフライトに連結した便
	LinkedRescued  int // 連結で unconfirmed から ok になった点
	FlightsOpenMax int // 開いているフライトの最大数

	// Resolve
	ResolvePairs         int // 同じ機体と疑われた組
	ResolveConfirmed     int // 同じ機体と決めた組
	ResolvedByContinuity int // 存在区間の包含で実位置を決めた組
	ResolveAmbiguous     int // 決められなかった組
	FixesEcho            int // 像の点
	FixesAmbiguous       int // 決められなかった重なりの点
	ResolveHeldMax       int // 保留した点の最大件数

	// Smooth
	SmoothedFlights int     // フィルタを持ったフライト
	SmoothedFixes   int     // 平滑化した点
	SmoothUpdates   int     // 観測で更新した回数（各フライトの 2 点目以降）
	SmoothNISSum    float64 // 正規化残差 (NIS) の合計。平均が 3 なら雑音の設定が観測と整合
	SmoothNISOver99 int     // NIS が χ²(3) の 99% 点を超えた更新
	SmoothInflated  int     // 残差に合わせて予測の共分散を膨らませた更新
	SmoothHeldMax   int     // 保留した点の最大件数
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

// angleDiff は 2 つの方位の差の絶対値 [rad]（0〜π）。
func angleDiff(a, b float64) float64 {
	d := math.Mod(math.Abs(a-b), 2*math.Pi)
	if d > math.Pi {
		d = 2*math.Pi - d
	}
	return d
}
