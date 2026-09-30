package tracking

import "pssrx/internal/geodesy"

// Position は推定した機体の位置。
type Position struct {
	Lat float64 // WGS84 [deg]
	Lon float64 // WGS84 [deg]
	Alt float64 // 標高 [m]。気圧高度から換算したもの
	// ENU は SSR を原点にした ENU 座標 [m]。連続性の門の距離計算に使う。
	ENU geodesy.ENU
	// Cov は SSR の ENU 系での位置の共分散 [m²]。添字は E, N, U の順。
	// 観測量の分散を線形伝播したもの。水平は動径方向（τ）と接線方向（方位）の
	// 2 成分で、遠方では方位方向に伸びた楕円になる。
	Cov [3][3]float64
}

// Measurement は位置を解いたときの観測の不確かさと検算値。位置を解いた
// 時点でしか得られないので、後続の処理（平滑化・較正）のために残す。
type Measurement struct {
	// SigmaBistaticM / SigmaAzimuthRad / SigmaAltitudeM は観測量（双基地距離・
	// ビーム方位・高さ）の標準偏差。Position.Cov はこれを伝播したもの。
	// 方位の標準偏差は応答数で決まる（bistatic.Config）。
	SigmaBistaticM  float64
	SigmaAzimuthRad float64
	SigmaAltitudeM  float64
	// BistaticRangeM は双基地距離 L [m]、GroundRangeM は SSR からの地上距離 ρ [m]。
	BistaticRangeM float64
	GroundRangeM   float64
	// GeometryFactor は ∂L/∂ρ（cos ε₁ + cos ξ₂）。遠方で 2、基線に近づくと 0 に
	// 向かい、距離方向の誤差が 1 / これ で膨らむ。
	GeometryFactor float64
	// ResidualM は検算値 | |P| + |P−R| − L | [m]。Iterations は曲率の反復回数。
	ResidualM  float64
	Iterations int
}

// Fix は 1 機体 × 1 走査の位置と、それを解いた観測の要約。出力の 1 行。
// Track / TrackSeq は Track が付ける。
type Fix struct {
	Timestamp  int64   // 観測時刻 [ns, Unix]（ドウェルの中心）
	Squawk     uint16  // Mode A のスコーク（8 進 4 桁）
	AltitudeFt int     // 気圧高度 [ft]
	Replies    int     // 位置を解いた応答列の応答数。方位の不確かさの目安
	TauNs      int64   // 主となる測定点の遅延（双基地距離）[ns]
	Azimuth    float64 // SSR のビーム方位 [rad], [0, 2pi)。列の最初と最後の中点

	// 列の形。短い列はドウェルの断片で、方位は欠けた側と反対に寄る。
	// AzimuthFirst / AzimuthLast は列の最初と最後の質問の方位 [rad]。
	AzimuthFirst float64
	AzimuthLast  float64
	// ModeAReplies / ModeCReplies は列の Mode A 応答と、復号できた Mode C
	// 応答の数。AltitudeSpreadFt は復号できた高度の最大と最小の差 [ft]。
	ModeAReplies     int
	ModeCReplies     int
	AltitudeSpreadFt int

	// Siblings は同じ走査・同じスコーク・同じ高度で τ が一致し（直接照射の
	// 候補）、方位が 10°（plot.Config.ImageAzimuthSeparationRad）を超えて
	// 離れた他のプロットの数。SSR 近傍の反射体経由の像の候補で、0 でなければ
	// この点か相手のどちらかが像。判定は後続が便の文脈で行う。
	Siblings int

	// Located は位置を解けたか。偽なら Position と Measure は空（Drop の
	// ある点だけ）。
	Located  bool
	Position Position
	Measure  Measurement

	// Track は航跡片（点の連続性で繋いだ列）の ID。処理の開始からの連番で、
	// 打ち切った航跡片の ID は再利用しない。0 は未付与（Drop のある点）。
	Track int64
	// TrackSeq は航跡片の中での順番（1 始まり）。判定は付けない。3 点目が
	// 来れば FRUIT の偶然の一致ではないといった判断は、後続が行う。
	TrackSeq int

	// Drop は棄却した理由。通常の出力では空で、デバッグ用に棄却した
	// プロットも出すとき（抑圧・位置推定の失敗）にだけ入る。
	Drop string
}
