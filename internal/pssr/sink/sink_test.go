package sink_test

import (
	"bytes"
	"encoding/csv"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"pssrx/internal/geodesy"
	"pssrx/internal/pssr/sink"
	"pssrx/internal/pssr/tracking"
)

// t0 は 2026-06-10T01:00:00Z（JST 10:00）。
const t0 = int64(1_781_053_200_000_000_000)

func located(ts int64, squawk uint16, track int64, seq int) tracking.Fix {
	return tracking.Fix{
		Timestamp: ts, Squawk: squawk, AltitudeFt: 8100, Replies: 20, TauNs: 457365, Azimuth: 2.2105,
		AzimuthFirst: 2.2, AzimuthLast: 2.221, ModeAReplies: 10, ModeCReplies: 9, AltitudeSpreadFt: 100, Siblings: 1,
		Located: true,
		Position: tracking.Position{Lat: 34.1234567, Lon: 136.7654321, Alt: 2468.88,
			ENU: geodesy.ENU{E: 1000.5, N: -2000.25, U: 2400},
			Cov: [3][3]float64{{100, -12.5, 0.5}, {-12.5, 2.25, 0.25}, {0.5, 0.25, 77.44}}},
		Measure: tracking.Measurement{SigmaBistaticM: 91.7, SigmaAzimuthRad: 0.0034906585, SigmaAltitudeM: 8.8,
			BistaticRangeM: 136200.4, GroundRangeM: 68000.2, GeometryFactor: 1.987654, ResidualM: 0.000012, Iterations: 3},
		Track: track, TrackSeq: seq,
	}
}

// TestCSVSink は列の並びと書式（UTC の時刻、共分散の上三角、位置の無い行の
// 空欄）を固定する。
func TestCSVSink(t *testing.T) {
	var buf bytes.Buffer
	s, err := sink.NewCSVSink(&buf, nil, "KX90S", "KX90")
	if err != nil {
		t.Fatal(err)
	}
	dropped := tracking.Fix{Timestamp: t0 + 5_000_000_000, Squawk: 0o3534, AltitudeFt: 8100, Replies: 4, TauNs: 30000,
		Azimuth: 1.5, AzimuthFirst: 1.49, AzimuthLast: 1.51, ModeAReplies: 2, ModeCReplies: 2, Drop: "locate_baseline"}
	if err := s.Write([]tracking.Fix{located(t0+1_646_578_950, 0o3534, 42, 3), dropped}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"time_utc,ssr,station,squawk,pressure_alt_ft,track,track_seq,lat,lon,height_m,e_m,n_m,u_m," +
			"cov_ee,cov_en,cov_eu,cov_nn,cov_nu,cov_uu,tau_ns,azimuth_rad,azimuth_first_rad,azimuth_last_rad," +
			"replies,mode_a_replies,mode_c_replies,altitude_spread_ft,siblings," +
			"sigma_bistatic_m,sigma_azimuth_rad,sigma_altitude_m,bistatic_range_m,ground_range_m,geometry_factor,residual_m,iterations,drop",
		"2026-06-10T01:00:01.646578950Z,KX90S,KX90,3534,8100,42,3,34.1234567,136.7654321,2468.880,1000.500,-2000.250,2400.000," +
			"100.0,-12.5,0.5,2.2,0.2,77.4,457365,2.210500000,2.200000000,2.221000000," +
			"20,10,9,100,1," +
			"91.7,0.003490658,8.80,136200.4,68000.2,1.987654,0.000012,3,",
		"2026-06-10T01:00:05.000000000Z,KX90S,KX90,3534,8100,0,0,,,,,,,,,,,,,30000,1.500000000,1.490000000,1.510000000," +
			"4,2,2,0,0,,,,,,,,,locate_baseline",
		"",
	}, "\n")
	if got := buf.String(); got != want {
		t.Errorf("CSV が違う\n got:\n%s\nwant:\n%s", got, want)
	}
}

func readRows(t *testing.T, path string) [][]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return rows[1:]
}

func files(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	slices.Sort(names)
	return names
}

// TestSquawkSinkSplitsBySquawkAndGap は、スコークごとにファイルが分かれ、
// 同じスコークでも 600 s 以上離れた点は別のファイルになること、ファイルの
// 中は時刻順（受け取りの順によらない）であることを確認する。
func TestSquawkSinkSplitsBySquawkAndGap(t *testing.T) {
	dir := t.TempDir()
	s, err := sink.NewSquawkSink(dir, "KX90S", "KX90")
	if err != nil {
		t.Fatal(err)
	}
	const sec = int64(1_000_000_000)
	batches := [][]tracking.Fix{
		{located(t0+4*sec, 0o1234, 1, 2), located(t0, 0o1234, 1, 1), located(t0+2*sec, 0o7777, 2, 1)},
		{located(t0+599*sec, 0o1234, 1, 3)},  // 前の点から 595 s: 同じファイル
		{located(t0+1199*sec, 0o1234, 3, 1)}, // 前の点から 600 s: 別のファイル
	}
	for _, b := range batches {
		if err := s.Write(b); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	want := []string{"20260610T010000Z_1234.csv", "20260610T010002Z_7777.csv", "20260610T011959Z_1234.csv"}
	if got := files(t, dir); !slices.Equal(got, want) {
		t.Fatalf("ファイル %v, 期待 %v", got, want)
	}
	rows := readRows(t, filepath.Join(dir, want[0]))
	var times []string
	for _, r := range rows {
		times = append(times, r[0][11:19])
	}
	if !slices.Equal(times, []string{"01:00:00", "01:00:04", "01:09:59"}) {
		t.Errorf("1234 の最初のファイルの時刻 %v", times)
	}
}

// TestSquawkSinkFlushesAfterGap は、スコークの最後の点から 600 s 過ぎた点を
// 受け取った時点で（Close を待たずに）そのファイルが書かれることを確認する。
func TestSquawkSinkFlushesAfterGap(t *testing.T) {
	dir := t.TempDir()
	s, err := sink.NewSquawkSink(dir, "KX90S", "KX90")
	if err != nil {
		t.Fatal(err)
	}
	const sec = int64(1_000_000_000)
	if err := s.Write([]tracking.Fix{located(t0, 0o1234, 1, 1)}); err != nil {
		t.Fatal(err)
	}
	if err := s.Write([]tracking.Fix{located(t0+599*sec, 0o7777, 2, 1)}); err != nil {
		t.Fatal(err)
	}
	if got := files(t, dir); len(got) != 0 {
		t.Fatalf("600 s 前に書かれた: %v", got)
	}
	if err := s.Write([]tracking.Fix{located(t0+600*sec, 0o7777, 2, 2)}); err != nil {
		t.Fatal(err)
	}
	if got := files(t, dir); !slices.Equal(got, []string{"20260610T010000Z_1234.csv"}) {
		t.Fatalf("600 s 後のファイル %v", got)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if got := files(t, dir); len(got) != 2 {
		t.Errorf("Close 後のファイル %v", got)
	}
}
