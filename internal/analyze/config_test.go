package analyze

import (
	"math"
	"testing"
)

// TestStationGeometryKnownPair は実運用と同じ桁の座標に対する
// 距離と方位を固定する。ここがずれると全レコードの時刻と方位が
// まとめてずれるので、値そのものをビット単位で押さえておく。
func TestStationGeometryKnownPair(t *testing.T) {
	dist, az := StationGeometry(
		-127458.67663663127, -31615.025566053235,
		-126591.43986481673, -32549.562701800554)
	if want := 1274.935008727154; dist != want {
		t.Errorf("dist = %x, 期待 %x", dist, want)
	}
	if want := 5.460452220221545; az != want {
		t.Errorf("azimuth = %x, 期待 %x", az, want)
	}
}

// TestStationGeometryQuadrants は平面直角座標の軸の向き（X が北、Y が東）に
// 対して方位が [0, 2pi) で北基準・東回りになることを確認する。
func TestStationGeometryQuadrants(t *testing.T) {
	cases := []struct {
		name    string
		dx, dy  float64
		wantDeg float64
	}{
		{"真北", 1, 0, 0},
		{"真東", 0, 1, 90},
		{"真南", -1, 0, 180},
		{"真西", 0, -1, 270},
		{"北東", 1, 1, 45},
		{"北西", 1, -1, 315},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, az := StationGeometry(0, 0, c.dx, c.dy)
			if got := az * 180 / math.Pi; math.Abs(got-c.wantDeg) > 1e-9 {
				t.Errorf("方位 = %g 度, 期待 %g 度", got, c.wantDeg)
			}
			if az < 0 || az >= 2*math.Pi {
				t.Errorf("方位 %g が [0, 2pi) の外", az)
			}
		})
	}
}

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
