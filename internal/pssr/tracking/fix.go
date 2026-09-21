package tracking

import (
	"fmt"

	"pssrx/internal/geodesy"
)

// Position は推定した機体の位置。
type Position struct {
	Lat float64 // WGS84 [deg]
	Lon float64 // WGS84 [deg]
	Alt float64 // 標高 [m]。気圧高度から換算したもの
	// ENU は SSR を原点にした ENU 座標 [m]。連続性の門の距離計算と、
	// 便どうしの比較、平滑化に使う。
	ENU geodesy.ENU
	// Cov は SSR の ENU 系での位置の共分散 [m²]。添字は E, N, U の順。
	// 観測量の分散を線形伝播したもの。
	Cov [3][3]float64
}

// Fix は 1 機体 × 1 走査の位置と、それを解いた観測の要約。
// Track / Flight / Status / Smoothed は航跡の各段が付ける。
type Fix struct {
	Timestamp  int64   // 観測時刻 [ns]（ドウェルの中心）
	Squawk     uint16  // Mode A のスコーク（8 進 4 桁）
	AltitudeFt int     // 気圧高度 [ft]
	Replies    int     // 位置を解いた応答列の応答数。方位の不確かさの目安
	TauNs      int64   // 主となる測定点の遅延（双基地距離）[ns]。同じ機体の判定に使う
	Azimuth    float64 // SSR のビーム方位 [rad], [0, 2pi)
	Position   Position
	// Track は便 ID。処理の開始からの連番で、打ち切った便の ID は再利用
	// しない。0 は未付与。
	Track int64
	// Flight は便を連結したフライトの ID（Link が付ける）。0 は未付与。
	Flight int64
	// Status は連続性と像の判定。
	Status FixStatus
	// Smoothed は平滑化した位置と速度（Smooth が付ける）。nil なら
	// 平滑化していない（unconfirmed の点）。
	Smoothed *Kinematics
}

// FixStatus は位置が便として確定したかの判定。
type FixStatus uint8

const (
	FixOK          FixStatus = iota // 確定した便の点
	FixUnconfirmed                  // 便が確定に届かず棄却
	FixEcho                         // 同じ機体の別のフライトが実位置で、こちらは像
	FixAmbiguous                    // 同じ機体のフライトが重なり、どちらが実位置か決められない
)

// String は CSV に書く表記。
func (s FixStatus) String() string {
	switch s {
	case FixOK:
		return "ok"
	case FixUnconfirmed:
		return "unconfirmed"
	case FixEcho:
		return "echo"
	case FixAmbiguous:
		return "ambiguous"
	}
	return fmt.Sprintf("status(%d)", uint8(s))
}
