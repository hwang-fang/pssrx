package geodesy

import (
	"math"
	"testing"
)

// TestRangeAzimuthKnownPair pins the range and azimuth for an ENU offset of
// the same magnitude as in operation (the station in testdata/kx90.yaml seen
// from the SSR). A change here shifts the timestamp and azimuth of every
// output record at once, so the values are pinned bit for bit.
func TestRangeAzimuthKnownPair(t *testing.T) {
	rng, az := ENU{E: -937.6096636162918, N: 864.0894997525627, U: -0.17023163525180962}.RangeAzimuth()
	if want := 1275.0539493951226; rng != want {
		t.Errorf("range = %x, want %x", rng, want)
	}
	if want := 5.4570037548680626; az != want {
		t.Errorf("azimuth = %x, want %x", az, want)
	}
}

// TestRangeAzimuthSlantRange pins that the range includes U. It is used to
// correct propagation delay, and a horizontal distance would underestimate
// the delay by the height difference.
func TestRangeAzimuthSlantRange(t *testing.T) {
	rng, _ := ENU{E: 3, N: 4, U: 12}.RangeAzimuth()
	if rng != 13 {
		t.Errorf("range = %g, want 13 (= sqrt(3^2+4^2+12^2))", rng)
	}
}

// TestRangeAzimuthQuadrants checks that the azimuth is measured from north
// clockwise and lies in [0, 2pi).
func TestRangeAzimuthQuadrants(t *testing.T) {
	cases := []struct {
		name    string
		n, e    float64
		wantDeg float64
	}{
		{"north", 1, 0, 0},
		{"east", 0, 1, 90},
		{"south", -1, 0, 180},
		{"west", 0, -1, 270},
		{"north-east", 1, 1, 45},
		{"north-west", 1, -1, 315},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, az := ENU{E: c.e, N: c.n}.RangeAzimuth()
			if got := az * 180 / math.Pi; math.Abs(got-c.wantDeg) > 1e-9 {
				t.Errorf("azimuth = %g deg, want %g deg", got, c.wantDeg)
			}
			if az < 0 || az >= 2*math.Pi {
				t.Errorf("azimuth %g outside [0, 2pi)", az)
			}
		})
	}
}

// TestENURotation は、同じ地点では単位行列、回転行列は直交で、東西に離れた
// 地点では北の軸が子午線の収束（経度差 × sin 緯度）だけ回ることを確認する。
func TestENURotation(t *testing.T) {
	id := ENURotation(34.85, 136.82, 34.85, 136.82)
	for i := range 3 {
		for j := range 3 {
			want := 0.0
			if i == j {
				want = 1
			}
			if math.Abs(id[i][j]-want) > 1e-15 {
				t.Fatalf("同じ地点で単位行列にならない: %v", id)
			}
		}
	}
	m := ENURotation(34.85, 136.82, 34.85, 137.92)
	for i := range 3 {
		for j := range 3 {
			var dot float64
			for k := range 3 {
				dot += m[i][k] * m[j][k]
			}
			want := 0.0
			if i == j {
				want = 1
			}
			if math.Abs(dot-want) > 1e-14 {
				t.Fatalf("直交でない: M Mᵀ[%d][%d] = %g", i, j, dot)
			}
		}
	}
	// 東へ 1.1° 離れた地点の北の軸は、元の東・北で見て東向きに sin(34.85°) × 1.1° ほど回る
	got := math.Atan2(m[1][0], m[1][1]) * 180 / math.Pi
	want := -1.1 * math.Sin(34.85*math.Pi/180)
	if math.Abs(got-want) > 0.01 {
		t.Errorf("北の軸の回転 %.4f°, 期待 約 %.4f°", got, want)
	}
}
