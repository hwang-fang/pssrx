package interrogator

import (
	"math"
	"testing"
)

// TestWrapAngle は方位角が必ず [0, 2pi) に収まることを確認する。
//
// 負のまま通すと、ファイル書き出しの uint32 変換が Go の仕様上「実装依存」に
// なる（amd64 では 2^32 で巻き戻り、arm64 では 0 に飽和する）。ドウェル先頭では
// 内挿の重みが負になるため、局方位が小さい配置では実際に負の値が来る。
func TestWrapAngle(t *testing.T) {
	const twoPi = 2 * math.Pi
	cases := []float64{
		0, 0.1, math.Pi, twoPi - 1e-9, twoPi, twoPi + 0.1,
		-0.1, -twoPi, -twoPi - 0.1, -1e-15, 3 * twoPi, -3 * twoPi,
		// 局方位 0.225 度で内挿の重みが -0.0057 のときに実際に出る値
		0.003922 - twoPi*0.0057,
	}
	for _, x := range cases {
		got := wrapAngle(x)
		if !(got >= 0 && got < twoPi) {
			t.Errorf("wrapAngle(%v) = %v が [0, 2pi) の外", x, got)
		}
		// 2pi の整数倍だけずらしても同じ結果になること
		if other := wrapAngle(x + 4*twoPi); math.Abs(other-got) > 1e-9 && math.Abs(other-got) < twoPi-1e-9 {
			t.Errorf("wrapAngle(%v) = %v だが 2pi 4 周ぶん足すと %v", x, got, other)
		}
		// uint32 へ落としても飽和・巻き戻りが起きないこと
		if u := uint32(got / twoPi * 0xFFFFFFFF); float64(u) > 0xFFFFFFFF {
			t.Errorf("wrapAngle(%v) = %v の uint32 変換が範囲外 %d", x, got, u)
		}
	}
}
