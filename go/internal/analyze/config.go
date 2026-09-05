// Package analyze は質問データからドウェルを検出し、その間を内挿した
// 質問予定表を作る。移植元の interrogator/analyze.py に対応する。
package analyze

import (
	"math"

	"pssrx/internal/pattern"
)

// cMPerNs は光速 [m/ns]。
const cMPerNs = 0.299792458

// Config は検出パラメータ。domain.py の ChainConfig に対応する。
type Config struct {
	// --- 手順 1: 振幅ゲート ---
	AmplitudeGateDbm float64 // ドウェル端より十分低い粗いゲート

	// --- 手順 2: 連鎖検出 (DP) ---
	GateNs         int64   // 連鎖ゲート
	MaxSkip        int64   // D : 欠測許容段数
	MinChainLength int     // 連鎖長の下限
	SkipPenalty    float64 // lambda : 段飛ばし 1 段あたりのペナルティ
	ResidualWeight float64 // mu : 正規化残差^2 の重み（同点解消用）

	// --- 手順 3: ドウェル分割 ---
	DwellGapPeriods float64 // 間隙分割の閾値（パターン周期の倍数）

	// --- 手順 4: 放物線フィット検証 ---
	ParabolaMinSamples    int
	RobustIters           int
	ParabolaOutlierK      float64 // 外れ値判定（MAD の sigma 倍）
	ParabolaSigmaFloorDb  float64 // 振幅雑音の下限
	VertexMarginFrac      float64 // 頂点がドウェル外に出る許容量
	ParabolaMaxResidualDb float64 // 残差 RMS の上限
	MinPeakDropDb         float64 // 端でこれ以上落ちていること
}

// DefaultConfig は ChainConfig の既定値と同じ設定を返す。
func DefaultConfig() Config {
	return Config{
		AmplitudeGateDbm:      -35.0,
		GateNs:                20_000,
		MaxSkip:               3,
		MinChainLength:        8,
		SkipPenalty:           0.30,
		ResidualWeight:        0.05,
		DwellGapPeriods:       20.0,
		ParabolaMinSamples:    6,
		RobustIters:           3,
		ParabolaOutlierK:      4.5,
		ParabolaSigmaFloorDb:  0.3,
		VertexMarginFrac:      0.25,
		ParabolaMaxResidualDb: 1.0,
		MinPeakDropDb:         3.0,
	}
}

// Params は SSR の質問パラメータ。domain.py の InterrogationParameter に対応する。
//
// Python 側は pattern を property で毎回組み立て直していたが、ここでは
// 構築時に 1 度だけ作って保持する。パターンは不変なので結果は変わらない。
type Params struct {
	AroundTimeNs int64
	Pattern      *pattern.Pattern
	Clockwise    bool
}

// StationGeometry は SSR から測定局への距離と方位を直交座標から求める。
//
// 緯度経度から直交座標への投影変換は本実装の範囲外で、呼び出し側が
// 変換済みの座標を渡す。main.py の get_station_info のうち、投影より
// 後ろの部分だけがここに対応する。
//
// 平面直角座標は X 軸が北向き、Y 軸が東向きなので atan2(dy, dx) で
// 北向きが 0 になる。戻り値の方位は [0, 2pi)。
func StationGeometry(ssrX, ssrY, stX, stY float64) (dist, azimuth float64) {
	dx := stX - ssrX
	dy := stY - ssrY
	dist = math.Sqrt(dx*dx + dy*dy)
	azimuth = math.Atan2(dy, dx)
	if azimuth < 0 {
		azimuth += 2 * math.Pi
	}
	return dist, azimuth
}
