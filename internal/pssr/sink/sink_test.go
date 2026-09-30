package sink_test

import (
	"bytes"
	"testing"

	"pssrx/internal/pssr/sink"
	"pssrx/internal/pssr/tracking"
)

// TestCSVSink は列の並びと書式を固定する。
func TestCSVSink(t *testing.T) {
	var buf bytes.Buffer
	s, err := sink.NewCSVSink(&buf, nil, "KX90S", "KX90")
	if err != nil {
		t.Fatal(err)
	}
	fix := tracking.Fix{
		Timestamp: 1_781_000_000_000_000_000,
		Azimuth:   2.2105, TauNs: 457365, Squawk: 0o3534, AltitudeFt: 8100, Replies: 20,
		Position: tracking.Position{Lat: 34.1234567, Lon: 136.7654321, Alt: 2468.88,
			Cov: [3][3]float64{{100, 0, 0}, {0, 2.25, 0}, {0, 0, 77.44}}},
		Track: 42, TrackSeq: 3,
	}
	if err := s.Write([]tracking.Fix{fix}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	want := "time_jst,ssr,station,squawk,pressure_alt_ft,lat,lon,alt_m,azimuth_rad,tau_ns,replies,sigma_e_m,sigma_n_m,sigma_u_m,track,track_seq\n" +
		"2026-06-09T19:13:20.000000000,KX90S,KX90,3534,8100,34.1234567,136.7654321,2468.880,2.210500000,457365,20,10.0,1.5,8.8,42,3\n"
	if got := buf.String(); got != want {
		t.Errorf("CSV\n got %q\nwant %q", got, want)
	}
}
