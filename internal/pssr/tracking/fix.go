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
	// 連鎖どうしの比較、平滑化に使う。
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
	// Track は航跡片（点の連続性で繋いだ列）の ID。処理の開始からの連番で、
	// 打ち切った航跡片の ID は再利用しない。0 は未付与。
	Track int64
	// Chain は航跡片を運動学的に連結した連鎖の ID（Link が付ける）。0 は未付与。
	// 平滑化と像の判定はこの単位で行う。
	Chain int64
	// Flight は便（同じ機体の 1 回の飛行）の ID（Flight が付ける）。0 は未付与。
	// 個別スコークでは同じスコークの連鎖を切れ目 FlightMaxGapNs まで束ね、
	// 非個別スコークでは連鎖そのもの。出力の分割単位。
	Flight int64
	// Status は連続性と像の判定。
	Status FixStatus
	// Smoothed は平滑化した位置と速度（Smooth が付ける）。nil なら
	// 平滑化していない（unconfirmed の点）。
	Smoothed *Kinematics
}

// FixStatus は位置の判定。連続性（Track）、像（Resolve）、便の点数（Flight）。
type FixStatus uint8

const (
	FixOK          FixStatus = iota // 確定した航跡片の点
	FixUnconfirmed                  // 航跡片が確定に届かず棄却
	FixEcho                         // 同じ機体の別のフライトが実位置で、こちらは像
	FixAmbiguous                    // 同じ機体の連鎖が重なり、どちらが実位置か決められない
	FixNoise                        // 点数が FlightMinPoints に満たない便の点
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
	case FixNoise:
		return "noise"
	}
	return fmt.Sprintf("status(%d)", uint8(s))
}
