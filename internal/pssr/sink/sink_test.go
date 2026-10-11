package sink_test

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
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
const (
	t0  = int64(1_781_053_200_000_000_000)
	sec = int64(1_000_000_000)
)

func located(ts int64, squawk uint16, track int64, seq int) tracking.Fix {
	return tracking.Fix{
		Timestamp: ts, Squawk: squawk, AltitudeFt: 8100, Replies: 20, TauNs: 457365, Azimuth: 2.2105,
		AzimuthFirst: 2.2, AzimuthLast: 2.221, ModeAReplies: 10, ModeCReplies: 9, AltitudeSpreadFt: 100, Siblings: 1,
		Located: true,
		Position: tracking.Position{Lat: 34.1234567, Lon: 136.7654321, Alt: 2468.88,
			ENU:   geodesy.ENU{E: 1000.5, N: -2000.25, U: 2400},
			Cov:   [3][3]float64{{100, -12.5, 0.5}, {-12.5, 2.25, 0.25}, {0.5, 0.25, 77.44}},
			CovEN: [2][2]float64{{101, -10.5}, {-10.5, 1.25}}},
		Measure: tracking.Measurement{SigmaBistaticM: 91.7, SigmaAzimuthRad: 0.0034906585, SigmaAltitudeM: 8.8,
			BistaticRangeM: 136200.4, GroundRangeM: 68000.2, GeometryFactor: 1.987654, ResidualM: 0.000012, Iterations: 3},
		Track: track, TrackSeq: seq, TrackStartNs: t0, TrackStartTauNs: 457000,
	}
}

var testCtx = sink.Context{SSR: "KX90S", Station: "KX90", Source: sink.SourceID("KX90S", "KX90"), Run: "run1"}

// TestCSVSink は列の並びと書式（UTC の時刻、機体の位置の軸の共分散、航跡片 ID、
// 処理の ID、位置の無い行の空欄）を固定する。
func TestCSVSink(t *testing.T) {
	var buf bytes.Buffer
	s, err := sink.NewCSVSink(&buf, nil, testCtx)
	if err != nil {
		t.Fatal(err)
	}
	dropped := tracking.Fix{Timestamp: t0 + 5*sec, Squawk: 0o3534, AltitudeFt: 8100, Replies: 4, TauNs: 30000,
		Azimuth: 1.5, AzimuthFirst: 1.49, AzimuthLast: 1.51, ModeAReplies: 2, ModeCReplies: 2, Drop: "locate_baseline"}
	if err := s.Write([]tracking.Fix{located(t0+1_646_578_950, 0o3534, 42, 3), dropped}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"time_utc,ssr,station,squawk,pressure_alt_ft,track,track_seq,lat,lon,height_m,e_m,n_m,u_m," +
			"cov_ee,cov_en,cov_nn,tau_ns,azimuth_rad,azimuth_first_rad,azimuth_last_rad," +
			"replies,mode_a_replies,mode_c_replies,altitude_spread_ft,siblings," +
			"sigma_bistatic_m,sigma_azimuth_rad,sigma_altitude_m,bistatic_range_m,ground_range_m,geometry_factor,residual_m,iterations,drop,run",
		"2026-06-10T01:00:01.646578950Z,KX90S,KX90,3534,8100,pssr1:KX90S:KX90:20260610T010000.000000000Z:457000,3," +
			"34.1234567,136.7654321,2468.880,1000.500,-2000.250,2400.000," +
			"101.0,-10.5,1.2,457365,2.210500000,2.200000000,2.221000000," +
			"20,10,9,100,1," +
			"91.7,0.003490658,8.80,136200.4,68000.2,1.987654,0.000012,3,,run1",
		"2026-06-10T01:00:05.000000000Z,KX90S,KX90,3534,8100,,0,,,,,,,,,,30000,1.500000000,1.490000000,1.510000000," +
			"4,2,2,0,0,,,,,,,,,locate_baseline,run1",
		"",
	}, "\n")
	if got := buf.String(); got != want {
		t.Errorf("CSV が違う\n got:\n%s\nwant:\n%s", got, want)
	}
}

// store は期間 [from, to) の処理を 1 回流す。
func store(t *testing.T, root, run string, from, to int64, fixes []tracking.Fix) {
	t.Helper()
	ctx := testCtx
	ctx.Run = run
	s, err := sink.NewSessionStore(root, ctx, from, to, 3, map[string]any{"schema_version": 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write(fixes); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

// series は squawk の点を from 秒から to 秒まで step 秒ごとに作る。
func series(squawk uint16, from, to, step int64) []tracking.Fix {
	var out []tracking.Fix
	for s := from; s <= to; s += step {
		out = append(out, located(t0+s*sec, squawk, 1, 1))
	}
	return out
}

func sessionFiles(t *testing.T, root string) []string {
	t.Helper()
	ents, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	slices.Sort(names)
	return names
}

// runsOf はファイルの行の run 列を時刻順に返す。
func runsOf(t *testing.T, path string) []string {
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
	var out []string
	for _, r := range rows[1:] {
		out = append(out, r[len(r)-1])
	}
	return out
}

func record(t *testing.T, root, run string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "runs", run+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// TestSessionStoreWritesSessions は、スコークセッションごとに 1 ファイルを書き、
// 点数が下限（3）に満たないセッションは書かないこと、600 s 以上の切れ目で
// ファイルが分かれること、処理の記録に書いたファイルが載ることを確認する。
func TestSessionStoreWritesSessions(t *testing.T) {
	root := t.TempDir()
	fixes := append(series(0o1234, 0, 40, 4), series(0o7777, 10, 14, 4)...) // 7777 は 2 点
	fixes = append(fixes, series(0o1234, 700, 708, 4)...)                   // 前から 660 s 後: 別のセッション
	slices.SortFunc(fixes, func(a, b tracking.Fix) int { return int(a.Timestamp - b.Timestamp) })
	store(t, root, "run1", t0, t0+3600*sec, fixes)
	want := []string{
		"20260610T010000Z_20260610T010040Z_1234.csv",
		"20260610T011140Z_20260610T011148Z_1234.csv",
	}
	if got := sessionFiles(t, root); !slices.Equal(got, want) {
		t.Fatalf("ファイル %v, 期待 %v", got, want)
	}
	rec := record(t, root, "run1")
	if n := len(rec["files_written"].([]any)); n != 2 || rec["schema_version"].(float64) != 1 || rec["source"] != "pssr1:KX90S:KX90" {
		t.Errorf("処理の記録: %v", rec)
	}
}

// TestSessionStoreContinuesPreviousRun は、前の期間の処理のファイルの最後の点から
// 600 s 未満で始まる点が、同じファイルに続けて書かれ、元のファイルが消えて
// 記録に載ることを確認する。
func TestSessionStoreContinuesPreviousRun(t *testing.T) {
	root := t.TempDir()
	store(t, root, "run1", t0, t0+3600*sec, series(0o1234, 3500, 3596, 4))
	store(t, root, "run2", t0+3600*sec, t0+7200*sec, series(0o1234, 3640, 3700, 4))
	want := []string{"20260610T015820Z_20260610T020140Z_1234.csv"}
	got := sessionFiles(t, root)
	if !slices.Equal(got, want) {
		t.Fatalf("ファイル %v, 期待 %v", got, want)
	}
	runs := runsOf(t, filepath.Join(root, got[0]))
	if runs[0] != "run1" || runs[len(runs)-1] != "run2" || len(runs) != 25+16 {
		t.Errorf("行の run %v", runs)
	}
	removed := record(t, root, "run2")["files_removed"].([]any)
	if len(removed) != 1 || removed[0] != "20260610T015820Z_20260610T015956Z_1234.csv" {
		t.Errorf("消したファイル %v", removed)
	}
}

// TestSessionStoreReplacesSamePeriod は、同じ期間を処理し直すと、期間の内側の
// 前の処理の点が置き換わり、外側の点は残ること、今回の処理に出てこない
// スコークの期間の内側の点も消えることを確認する。
func TestSessionStoreReplacesSamePeriod(t *testing.T) {
	root := t.TempDir()
	first := append(series(0o1234, 0, 1196, 4), series(0o7777, 1000, 1100, 4)...)
	slices.SortFunc(first, func(a, b tracking.Fix) int { return int(a.Timestamp - b.Timestamp) })
	store(t, root, "run1", t0, t0+1200*sec, first)
	// [400 s, 800 s) を処理し直す。1234 は点が少し変わり、7777 は出てこない
	store(t, root, "run2", t0+400*sec, t0+800*sec, series(0o1234, 402, 798, 4))
	got := sessionFiles(t, root)
	if len(got) != 2 || !strings.HasSuffix(got[0], "_1234.csv") || !strings.HasSuffix(got[1], "_7777.csv") {
		t.Fatalf("ファイル %v", got)
	}
	runs := runsOf(t, filepath.Join(root, got[0]))
	count := map[string]int{}
	for _, r := range runs {
		count[r]++
	}
	// run1 の [0, 400) と [800, 1196] は残り（100 + 100 点）、run2 が 100 点
	if count["run1"] != 200 || count["run2"] != 100 {
		t.Errorf("1234 の行の内訳 %v", count)
	}
	// 7777 は [1000, 1100] で期間の外なので残る
	if r := runsOf(t, filepath.Join(root, got[1])); len(r) != 26 {
		t.Errorf("7777 の行 %d, 期待 26", len(r))
	}
	// 期間の内側にだけ点のあったスコークは消える
	store(t, root, "run3", t0+1000*sec, t0+1200*sec, nil)
	got = sessionFiles(t, root)
	if len(got) != 1 || !strings.HasSuffix(got[0], "_1234.csv") {
		t.Fatalf("run3 の後のファイル %v", got)
	}
}

// TestSessionStoreSplitsAtNewGap は、置き換えで 600 s 以上の切れ目ができると
// ファイルが 2 つに分かれることを確認する。
func TestSessionStoreSplitsAtNewGap(t *testing.T) {
	root := t.TempDir()
	store(t, root, "run1", t0, t0+3600*sec, series(0o1234, 0, 2000, 4))
	// [500 s, 1500 s) を処理し直し、そこに点が無い（機体が覆域の外だった）
	store(t, root, "run2", t0+500*sec, t0+1500*sec, nil)
	want := []string{
		"20260610T010000Z_20260610T010816Z_1234.csv",
		"20260610T012500Z_20260610T013320Z_1234.csv",
	}
	if got := sessionFiles(t, root); !slices.Equal(got, want) {
		t.Fatalf("ファイル %v, 期待 %v", got, want)
	}
}

// TestSessionStorePrependsToLaterRun は、後ろの期間を先に処理したあとで前の
// 期間を処理すると、後ろのファイルの先頭に 600 s 未満でつながる点が同じ
// ファイルに入ることを確認する（処理の順序によらない）。
func TestSessionStorePrependsToLaterRun(t *testing.T) {
	root := t.TempDir()
	store(t, root, "run2", t0+3600*sec, t0+7200*sec, series(0o1234, 3640, 3700, 4))
	store(t, root, "run1", t0, t0+3600*sec, series(0o1234, 3500, 3596, 4))
	want := []string{"20260610T015820Z_20260610T020140Z_1234.csv"}
	if got := sessionFiles(t, root); !slices.Equal(got, want) {
		t.Fatalf("ファイル %v, 期待 %v", got, want)
	}
}

// TestSessionStoreAbortKeepsOtherRuns は、失敗した処理（Abort）が、途中で書いた
// ファイルは残しても、期間の内側のほかの処理のファイルを消さず、処理の記録を
// 書かないことを確認する。
func TestSessionStoreAbortKeepsOtherRuns(t *testing.T) {
	root := t.TempDir()
	store(t, root, "run1", t0, t0+3600*sec, append(series(0o1234, 0, 40, 4), series(0o7777, 2000, 2040, 4)...))

	ctx := testCtx
	ctx.Run = "run2"
	s, err := sink.NewSessionStore(root, ctx, t0, t0+3600*sec, 3, nil)
	if err != nil {
		t.Fatal(err)
	}
	// 1234 は切れ目を過ぎて書かれ、6666 は書き出し待ちのまま失敗する
	if err := s.Write(append(series(0o1234, 0, 40, 4), series(0o6666, 1000, 1040, 4)...)); err != nil {
		t.Fatal(err)
	}
	if err := s.Abort(); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"20260610T010000Z_20260610T010040Z_1234.csv",
		"20260610T013320Z_20260610T013400Z_7777.csv",
	}
	if got := sessionFiles(t, root); !slices.Equal(got, want) {
		t.Fatalf("ファイル %v, 期待 %v", got, want)
	}
	if runs := runsOf(t, filepath.Join(root, want[0])); runs[0] != "run2" {
		t.Errorf("1234 の行の run %v", runs)
	}
	if _, err := os.Stat(filepath.Join(root, "runs", "run2.json")); !os.IsNotExist(err) {
		t.Errorf("失敗した処理の記録がある: %v", err)
	}
}
