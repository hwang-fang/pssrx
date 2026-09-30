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

// Fix は 1 機体 × 1 走査の位置と、それを解いた観測の要約。出力の 1 行。
// Track / TrackSeq は Track が付ける。
type Fix struct {
	Timestamp  int64   // 観測時刻 [ns, Unix]（ドウェルの中心）
	Squawk     uint16  // Mode A のスコーク（8 進 4 桁）
	AltitudeFt int     // 気圧高度 [ft]
	Replies    int     // 位置を解いた応答列の応答数。方位の不確かさの目安
	TauNs      int64   // 主となる測定点の遅延（双基地距離）[ns]
	Azimuth    float64 // SSR のビーム方位 [rad], [0, 2pi)。列の最初と最後の中点

	Position Position

	// Track は航跡片（点の連続性で繋いだ列）の ID。処理の開始からの連番で、
	// 打ち切った航跡片の ID は再利用しない。0 は未付与。
	Track int64
	// TrackSeq は航跡片の中での順番（1 始まり）。判定は付けない。3 点目が
	// 来れば FRUIT の偶然の一致ではないといった判断は、後続が行う。
	TrackSeq int
}
