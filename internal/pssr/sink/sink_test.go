package sink_test

import (
	"bytes"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"pssrx/internal/geodesy"
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
		Track: 42, Chain: 3, Flight: 7, Status: tracking.FixUnconfirmed,
	}
	smoothed := fix
	smoothed.Status = tracking.FixOK
	smoothed.Smoothed = &tracking.Kinematics{
		Lat: 34.1234000, Lon: 136.7654000, Alt: 2470.5,
		Velocity: geodesy.ENU{E: -120.25, N: 200.5, U: 2.126},
		TurnRate: -1.5 * math.Pi / 180, HasTurnRate: true,
		Cov: [7][7]float64{{25}, {0, 4}, {0, 0, 49}},
	}
	if err := s.Write([]tracking.Fix{fix, smoothed}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("行数 %d: %q", len(lines), buf.String())
	}
	wantHeader := "time_jst,ssr,station,squawk,pressure_alt_ft,lat,lon,alt_m,azimuth_rad,tau_ns,replies,sigma_e_m,sigma_n_m,sigma_u_m,track,chain,flight,status," +
		"sm_lat,sm_lon,sm_alt_m,sm_sigma_e_m,sm_sigma_n_m,sm_sigma_u_m,vel_e_mps,vel_n_mps,vel_u_mps,turn_rate_dps"
	if lines[0] != wantHeader {
		t.Errorf("ヘッダ %q, 期待 %q", lines[0], wantHeader)
	}
	want := "2026-06-09T19:13:20.000000000,KX90S,KX90,3534,8100,34.1234567,136.7654321,2468.880,2.210500000,457365,20,10.0,1.5,8.8,42,3,7,unconfirmed,,,,,,,,,,"
	if lines[1] != want {
		t.Errorf("行 %q, 期待 %q", lines[1], want)
	}
	want = "2026-06-09T19:13:20.000000000,KX90S,KX90,3534,8100,34.1234567,136.7654321,2468.880,2.210500000,457365,20,10.0,1.5,8.8,42,3,7,ok," +
		"34.1234000,136.7654000,2470.500,5.0,2.0,7.0,-120.2,200.5,2.13,1.50"
	if lines[2] != want {
		t.Errorf("行 %q, 期待 %q", lines[2], want)
	}
}

// TestFlightSink は便ごとに 1 ファイルになり、ブロックをまたいで追記され、
// unconfirmed と noise が便のファイルに同梱されるか、デバッグ用の
// noise ファイルに行くかを確認する。
func TestFlightSink(t *testing.T) {
	at := func(sec int64, flight int64, squawk uint16, status tracking.FixStatus) tracking.Fix {
		return tracking.Fix{Timestamp: 1_781_000_000_000_000_000 + sec*1e9, Squawk: squawk, Chain: flight, Flight: flight, Status: status}
	}
	feed := func(s *sink.FlightSink) {
		t.Helper()
		// 便 1（個別 1234）、便 2（個別 7700 は非個別扱い）、便 3（1200）
		if err := s.Write([]tracking.Fix{at(0, 1, 0o1234, tracking.FixOK), at(1, 3, 0o1200, tracking.FixOK), at(2, 4, 0o1200, tracking.FixUnconfirmed)}); err != nil {
			t.Fatal(err)
		}
		// 便 1 の続き（echo）と unconfirmed、個別 1234 の小さい便 5 の noise、1200 の noise 便 6
		if err := s.Write([]tracking.Fix{at(4, 1, 0o1234, tracking.FixEcho), at(3, 1, 0o1234, tracking.FixUnconfirmed),
			at(900, 5, 0o1234, tracking.FixNoise), at(10, 6, 0o1200, tracking.FixNoise)}); err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
	nonUnique := []string{"1200", "7700"}
	names := func(dir string) []string {
		entries, _ := os.ReadDir(dir)
		var v []string
		for _, e := range entries {
			v = append(v, e.Name())
		}
		return v
	}
	lines := func(path string) []string {
		b, _ := os.ReadFile(path)
		return strings.Split(strings.TrimSpace(string(b)), "\n")
	}

	// 既定: noise を書かない
	dir := t.TempDir()
	s, err := sink.NewFlightSink(dir, "KX90S", "KX90", nonUnique, false)
	if err != nil {
		t.Fatal(err)
	}
	feed(s)
	want := []string{"20260609T191320_1234_1.csv", "20260609T191321_1200_3.csv"}
	if got := names(dir); !slices.Equal(got, want) {
		t.Fatalf("ファイル %v, 期待 %v", got, want)
	}
	// 便 1: ok, unconfirmed(3 s), echo(4 s), noise(900 s) が時刻順
	got := lines(filepath.Join(dir, want[0]))
	if len(got) != 5 {
		t.Fatalf("便 1 の行数 %d: %v", len(got), got)
	}
	for i, st := range []string{",ok,", ",unconfirmed,", ",echo,", ",noise,"} {
		if !strings.Contains(got[i+1], st) {
			t.Errorf("便 1 の %d 行目に %s が無い: %s", i+1, st, got[i+1])
		}
	}

	// noise を書く
	dir = t.TempDir()
	s, _ = sink.NewFlightSink(dir, "KX90S", "KX90", nonUnique, true)
	feed(s)
	want = []string{"20260609T191320_1234_1.csv", "20260609T191321_1200_3.csv", "20260609T191322_1200_noise.csv", "20260609T191330_1200_noise.csv"}
	if got := names(dir); !slices.Equal(got, want) {
		t.Fatalf("ファイル %v, 期待 %v", got, want)
	}
}
