// Package interrogator は受信した質問データから SSR のドウェル（ビームが
// 測定局を向いていた区間）を検出し、ドウェルとドウェルの間の質問時刻を
// 内挿して質問予定表を作る。
package interrogator

import (
	"fmt"

	"pssrx/internal/record"
	"pssrx/internal/ssr"
)

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

// Validate は定数の整合を検査する。振幅ゲートは波高値として表せる範囲、
// 間隙は走査周期に対する割合として (0, 1)、件数と反復回数は非負。
func Validate(cfg Config) error {
	if _, err := record.EncodeWaveheight(cfg.AmplitudeGateDbm); err != nil {
		return fmt.Errorf("振幅ゲートが不正: %w", err)
	}
	if cfg.GateNs <= 0 || cfg.MaxSkip < 0 || cfg.MinChainLength < 1 || cfg.SkipPenalty < 0 || cfg.ResidualWeight < 0 {
		return fmt.Errorf("連鎖検出の定数が不正: %+v", cfg)
	}
	if !(cfg.DwellGapPeriods > 0 && cfg.DwellGapPeriods < 1) {
		return fmt.Errorf("ドウェル分割の間隙は走査周期に対する割合で (0, 1): %g", cfg.DwellGapPeriods)
	}
	if cfg.ParabolaMinSamples < 3 || cfg.RobustIters < 0 || cfg.ParabolaOutlierK <= 0 || cfg.ParabolaSigmaFloorDb < 0 ||
		cfg.VertexMarginFrac < 0 || cfg.ParabolaMaxResidualDb <= 0 || cfg.MinPeakDropDb < 0 {
		return fmt.Errorf("放物線フィットの定数が不正: %+v", cfg)
	}
	return nil
}

// Params は解析対象の SSR そのものの性質。設定からの組み立ては pipeline が担う。
type Params struct {
	AroundTimeNs int64
	Pattern      *ssr.Pattern
	Clockwise    bool
}
