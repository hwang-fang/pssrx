package record

import (
	"fmt"
	"math"
)

// ReceivedInterrogation は測定局で受信した質問 1 件（qpkx のレコード）。
// interrogator 段の入力で、振幅列からドウェルを検出する。
type ReceivedInterrogation struct {
	Timestamp int64 // Unix ナノ秒（受信時刻）
	WH        uint16
	Mode      uint8
}

// minWaveheightDbm は波高値として表現できる最小値（-255 - 255/256）。
var minWaveheightDbm = -(255.0 + 255.0/256.0)

// EncodeWaveheight は dBm を波高値の生値へ変換する。
// 生値は 1/256 dB 刻みで、0 dBm が 0xFFFF、値が小さいほど弱い。
func EncodeWaveheight(dbm float64) (uint16, error) {
	if !(minWaveheightDbm <= dbm && dbm <= 0.0) {
		return 0, fmt.Errorf("波高値は %g ~ 0 の必要があります: %g", minWaveheightDbm, dbm)
	}
	// 0 方向へ切り捨てる。四捨五入すると 1 刻みずれた生値になる。
	return uint16(0xFFFF + int64(math.Trunc(dbm*256))), nil
}

// DecodeWaveheight は波高値の生値を dBm へ戻す。EncodeWaveheight の逆。
func DecodeWaveheight(v uint16) float64 { return -float64(0xFFFF-v) / 256 }
