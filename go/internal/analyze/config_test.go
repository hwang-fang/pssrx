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
