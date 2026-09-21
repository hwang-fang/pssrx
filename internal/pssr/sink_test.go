package pssr_test

import (
	"bytes"
	"strings"
	"testing"

	"pssrx/internal/geodesy"
	"pssrx/internal/pssr"
)

// TestCSVSink は列の並びと書式を固定する。
func TestCSVSink(t *testing.T) {
	var buf bytes.Buffer
	s, err := pssr.NewCSVSink(&buf, nil, "KX90S", "KX90")
	if err != nil {
		t.Fatal(err)
	}
	fix := pssr.Fix{
		Plot: pssr.Plot{
			Timestamp: start,
			Azimuth:   2.2105, TauNs: 457365, Squawk: 0o3534, AltitudeFt: 8100,
			Replies: make([]pssr.PairedReply, 20),
		},
		Position: pssr.Position{Lat: 34.1234567, Lon: 136.7654321, Alt: 2468.88,
			Cov: [3][3]float64{{100, 0, 0}, {0, 2.25, 0}, {0, 0, 77.44}}},
		Track: 42, Flight: 7, Status: pssr.FixUnconfirmed,
	}
	smoothed := fix
	smoothed.Status = pssr.FixOK
	smoothed.Smoothed = &pssr.Kinematics{
		Lat: 34.1234000, Lon: 136.7654000, Alt: 2470.5,
		Velocity: geodesy.ENU{E: -120.25, N: 200.5, U: 2.126},
		Cov:      [6][6]float64{{25}, {0, 4}, {0, 0, 49}},
	}
	if err := s.Write([]pssr.Fix{fix, smoothed}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("行数 %d: %q", len(lines), buf.String())
	}
	wantHeader := "time_jst,ssr,station,squawk,pressure_alt_ft,lat,lon,alt_m,azimuth_rad,tau_ns,replies,sigma_e_m,sigma_n_m,sigma_u_m,track,flight,status," +
		"sm_lat,sm_lon,sm_alt_m,sm_sigma_e_m,sm_sigma_n_m,sm_sigma_u_m,vel_e_mps,vel_n_mps,vel_u_mps"
	if lines[0] != wantHeader {
		t.Errorf("ヘッダ %q, 期待 %q", lines[0], wantHeader)
	}
	want := "2026-06-09T19:13:20.000000000,KX90S,KX90,3534,8100,34.1234567,136.7654321,2468.880,2.210500000,457365,20,10.0,1.5,8.8,42,7,unconfirmed,,,,,,,,,"
	if lines[1] != want {
		t.Errorf("行 %q, 期待 %q", lines[1], want)
	}
	want = "2026-06-09T19:13:20.000000000,KX90S,KX90,3534,8100,34.1234567,136.7654321,2468.880,2.210500000,457365,20,10.0,1.5,8.8,42,7,ok," +
		"34.1234000,136.7654000,2470.500,5.0,2.0,7.0,-120.2,200.5,2.13"
	if lines[2] != want {
		t.Errorf("行 %q, 期待 %q", lines[2], want)
	}
}
