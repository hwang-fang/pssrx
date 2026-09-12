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
