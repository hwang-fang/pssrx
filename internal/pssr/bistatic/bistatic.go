// Package bistatic は単一測定点の双基地幾何で機体の位置を解く。
// SSR の質問と測定局の受信の遅延（双基地距離）、ビーム方位、気圧高度の
// 交点を求め、観測量の分散を伝播した共分散を付けて tracking.Fix にする。
//
// 多点の座標算出は別のパッケージで実装し、ここは単一測定点専用に保つ。
package bistatic

import (
	"fmt"
	"math"
)

// Params は局と SSR の組に固有の値。設定からの導出は pipeline が担う。
type Params struct {
	// AroundTimeNs は SSR の走査周期 [ns]。
	AroundTimeNs int64
	// MeanPRINs は質問 1 発あたりの平均間隔 [ns]。走査周期との比が 1 質問
	// あたりのビームの回転角で、応答列の欠けを方位誤差に換算するのに使う。
	MeanPRINs float64
	// MaxRangeM は SSR の覆域 [m]。位置の探索範囲の上限に使う。
	MaxRangeM float64
}

// Config は手続きの定数。局や SSR によらない。
type Config struct {
	// ZMarginM は解の一意性判定 |z| < ℓ に持たせる余裕 [m]。SSR 直上の
	// 除外円錐の広さを決める。精度のためではなく曖昧さを確実に避けるための
	// 余裕で、数百 m あれば足りる。
	ZMarginM float64
	// BaselineMarginM は L − B（双基地距離 − 基線の水平成分）の下限 [m]。
	// 機体が基線上に近いと解が発散する。z > 0 なら一意性判定が弾くが、
	// z ≈ 0 では効かないので、その安全弁
	BaselineMarginM float64
	// CurvatureTolM は曲率の反復の収束判定 [m]。収縮率が 1/500 程度なので、
	// ここで止めても z の誤差はこの 1/500 に収まる。
	CurvatureTolM float64
	// CurvatureMaxIter は曲率の反復の上限回数。通常 2〜3 回で収束する。
	CurvatureMaxIter int
	// SigmaTimingNs は質問時刻と受信時刻のジッタを合成した標準偏差 [ns]。
	// intg の 100 ns 量子化と応答のジッタ（実測 RMS 60 ns）。
	SigmaTimingNs float64
	// SigmaTransponderNs は応答遅延の公差 ±0.5 µs を一様分布とみなした
	// 標準偏差 [ns]（0.5 / √3）。機体ごとの系統誤差として残る。
	SigmaTransponderNs float64
	// SigmaAzimuthRad は応答列が完全なとき（応答数が DwellFullReplies 以上）
	// のビーム中心の方位の標準偏差 [rad]。ADS-B の真値との比較（KX00、
	// 応答 14 件以上）で誤差の中央値 0.11°、p90 0.33°。裾が正規分布より重い
	// ので、p90 が 1.64σ に収まる 0.20° にする（1 質問あたりのビームの回転
	// 約 0.26° と同程度）。
	SigmaAzimuthRad float64
	// DwellFullReplies は応答列を完全とみなす応答数。これに満たない列は
	// ドウェルの断片で、最初と最後の中点がビームの片側に寄る。方位の標準
	// 偏差は欠けた応答 1 件あたり AzimuthFragmentFactor × 1 質問あたりの
	// 回転角だけ増す（azimuthSigma）。真値との比較では欠けに対してほぼ
	// 線形で、14 件を境に 0.13° から 3 件で約 2° まで増えた。
	DwellFullReplies int
	// AzimuthFragmentFactor は欠けた応答 1 件あたりに増す方位の標準偏差を、
	// 1 質問あたりの回転角に対する倍率で表す。
	AzimuthFragmentFactor float64
	// SigmaAltitudeM は高さの標準偏差 [m]。Mode C の 100 ft 量子化（30.48 / √12）。
	SigmaAltitudeM float64
}

// DefaultConfig は既定の定数。実データの分布を見て調整する。
func DefaultConfig() Config {
	return Config{
		ZMarginM: 200, BaselineMarginM: 500, CurvatureTolM: 0.05, CurvatureMaxIter: 5,
		SigmaTimingNs: 100, SigmaTransponderNs: 500 / math.Sqrt(3),
		SigmaAzimuthRad: 0.20 * math.Pi / 180, DwellFullReplies: 14, AzimuthFragmentFactor: 0.77, SigmaAltitudeM: 30.48 / math.Sqrt(12),
	}
}

// Validate は Params と Config の整合を検査する。手続きに入る前に 1 度呼ぶ。
func Validate(params Params, cfg Config) error {
	if params.AroundTimeNs <= 0 {
		return fmt.Errorf("走査周期が不正: %d", params.AroundTimeNs)
	}
	if params.MeanPRINs <= 0 {
		return fmt.Errorf("質問間隔が不正: %g", params.MeanPRINs)
	}
	if params.MaxRangeM <= 0 {
		return fmt.Errorf("覆域が不正: %g", params.MaxRangeM)
	}
	if cfg.ZMarginM < 0 || cfg.BaselineMarginM < 0 || cfg.CurvatureTolM <= 0 || cfg.CurvatureMaxIter < 1 || cfg.SigmaTimingNs < 0 ||
		cfg.SigmaTransponderNs < 0 || cfg.SigmaAzimuthRad < 0 || cfg.SigmaAltitudeM < 0 ||
		cfg.DwellFullReplies < 0 || cfg.AzimuthFragmentFactor < 0 {
		return fmt.Errorf("位置推定の定数が不正: %+v", cfg)
	}
	return nil
}

// Stats は件数。呼び出し側が持ち、Locate に渡して足し込む。
type Stats struct {
	Inconsistent  int // 双基地距離が 0 以下、または座標変換の失敗
	Baseline      int // 双基地距離が基線の水平成分 + 余裕 以下（基線特異点）
	Ambiguous     int // 正根が 2 つ（機体が基線の近傍）
	NoSolution    int // 与えた高さに解が無い
	NonConvergent int // 曲率の反復が収束しない
	Fixes         int
}
