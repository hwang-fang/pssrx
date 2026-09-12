// Package analyze は受信した質問データから SSR のドウェル（ビームが
// 測定局を向いていた区間）を検出し、ドウェルとドウェルの間の質問時刻を
// 内挿して質問予定表を作る。
package analyze

import (
	"math"

	"pssrx/internal/pattern"
)

// cMPerNs は光速 [m/ns]。
const cMPerNs = 0.299792458

// Config は検出パラメータ。手順の順序どおりに並べてある。
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
	DwellGapPeriods float64 // 間隙分割の閾値（1回転の割合）

	// --- 手順 4: 放物線フィット検証 ---
	ParabolaMinSamples    int
	RobustIters           int
	ParabolaOutlierK      float64 // 外れ値判定（MAD の sigma 倍）
	ParabolaSigmaFloorDb  float64 // 振幅雑音の下限
	VertexMarginFrac      float64 // 頂点がドウェル外に出る許容量
	ParabolaMaxResidualDb float64 // 残差 RMS の上限
	MinPeakDropDb         float64 // 端でこれ以上落ちていること
}

// DefaultConfig は運用で使っている既定値を返す。
func DefaultConfig() Config {
	return Config{
		AmplitudeGateDbm:      -35.0,
		GateNs:                20_000,
		MaxSkip:               3,
		MinChainLength:        8,
		SkipPenalty:           0.30,
		ResidualWeight:        0.05,
		DwellGapPeriods:       0.2,
		ParabolaMinSamples:    6,
		RobustIters:           3,
		ParabolaOutlierK:      4.5,
		ParabolaSigmaFloorDb:  0.3,
		VertexMarginFrac:      0.25,
		ParabolaMaxResidualDb: 1.0,
		MinPeakDropDb:         3.0,
	}
}

// Params は解析対象の SSR そのものの性質。設定ファイルから組み立てる。
type Params struct {
	AroundTimeNs int64
	Pattern      *pattern.Pattern
	Clockwise    bool
}

// StationGeometry は SSR を原点にした ENU 座標 [m] にある測定局への
// 距離 [m] と方位 [rad] を求める。
//
// 緯度経度から ENU への変換は geodesy が担い、ここは座標差だけを扱う。
// 距離は伝搬遅延の補正に使うので斜距離（U を含む）、方位はビーム中心
// 通過時刻の基準に使うので真北を 0 とし東回り。戻り値は [0, 2pi)。
func StationGeometry(e, n, u float64) (dist, azimuth float64) {
	dist = math.Sqrt(e*e + n*n + u*u)
	azimuth = math.Atan2(e, n)
	if azimuth < 0 {
		azimuth += 2 * math.Pi
	}
	return dist, azimuth
}
