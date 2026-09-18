package config

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pssrx/internal/geodesy/geoid"
)

const sample = `
ssrs:
  KX90S:
    name: KX90_SSR
    lat: 34.85058333
    lon: 136.82093888
    alt: 0
    max_range_m: 400000
    interrogation:
      around_time_sec: 4.05
      mode_pattern: ACAC
      interval_pattern_ns: [2906500]
      clockwise: true
stations:
  KX90:
    name: KX90_STATION
    lat: 34.8583717981495
    lon: 136.810685698149
    alt: 0
`

// load は sample 相当の YAML を読み、KX90S / KX90 の組を取り出す。
func load(t *testing.T, body string) (*File, SSR, Station) {
	t.Helper()
	f, err := Load(write(t, body))
	if err != nil {
		t.Fatal(err)
	}
	ssr, err := f.SSR("KX90S")
	if err != nil {
		t.Fatal(err)
	}
	st, err := f.Station("KX90")
	if err != nil {
		t.Fatal(err)
	}
	return f, ssr, st
}

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestBaseline は名古屋の SSR・測定局に対する距離と方位を固定する。
//
// 移植元は平面直角座標（EPSG:6675）に投影した座標差から
// dist = 1274.935008727154, azimuth = 5.460452220221545 を出していた。
// ENU では投影の縮尺係数（約 0.9999）と子午線収差（約 0.2 度）のぶん
// 値が変わる。ここではその差が想定の範囲であることも確かめる。
func TestBaseline(t *testing.T) {
	_, ssr, st := load(t, sample)
	gm, err := geoid.Load()
	if err != nil {
		t.Fatal(err)
	}
	dist, az, err := Baseline(ssr, st, gm)
	if err != nil {
		t.Fatal(err)
	}
	wantDist, wantAz := 1275.0539493951226, 5.4570037548680626
	if dist != wantDist {
		t.Errorf("dist = %x, 期待 %x", dist, wantDist)
	}
	if az != wantAz {
		t.Errorf("azimuth = %x, 期待 %x", az, wantAz)
	}
	// 投影版との差: 距離は縮尺係数ぶん（0.5 m 以内）、方位は子午線収差ぶん（0.3 度以内）
	if d := math.Abs(dist - 1274.935008727154); d > 0.5 {
		t.Errorf("投影版との距離差 %g m が想定より大きい", d)
	}
	if d := math.Abs(az-5.460452220221545) * 180 / math.Pi; d > 0.3 {
		t.Errorf("投影版との方位差 %g 度が想定より大きい", d)
	}
}

// TestBaselineRejectsOutsideGeoid はジオイドモデルの範囲外（日本国外）を
// 黙って通さないことを確認する。
func TestBaselineRejectsOutsideGeoid(t *testing.T) {
	_, ssr, st := load(t, sample)
	gm, err := geoid.Load()
	if err != nil {
		t.Fatal(err)
	}
	lat, lon := 51.5, -0.1
	st.Lat, st.Lon = &lat, &lon
	if _, _, err := Baseline(ssr, st, gm); err == nil {
		t.Error("ジオイド範囲外の測定局がエラーにならない")
	}
}

func TestRejectsInvalidConfig(t *testing.T) {
	bad := map[string]string{
		"不正な質問種別": "mode_pattern: AXC",
		"間隔が空":    "interval_pattern_ns: []",
		"間隔が 0":   "interval_pattern_ns: [2906500, 0]",
		"走査周期が 0": "around_time_sec: 0",
		"覆域が 0":   "max_range_m: 0",
	}
	for name, repl := range bad {
		t.Run(name, func(t *testing.T) {
			body := sample
			switch {
			case repl == "mode_pattern: AXC":
				body = replaceLine(body, "      mode_pattern: ACAC", "      mode_pattern: AXC")
			case repl == "interval_pattern_ns: []":
				body = replaceLine(body, "      interval_pattern_ns: [2906500]", "      interval_pattern_ns: []")
			case repl == "interval_pattern_ns: [2906500, 0]":
				body = replaceLine(body, "      interval_pattern_ns: [2906500]", "      interval_pattern_ns: [2906500, 0]")
			case repl == "around_time_sec: 0":
				body = replaceLine(body, "      around_time_sec: 4.05", "      around_time_sec: 0")
			case repl == "max_range_m: 0":
				body = replaceLine(body, "    max_range_m: 400000", "    max_range_m: 0")
			}
			if _, err := Load(write(t, body)); err == nil {
				t.Errorf("%s がエラーにならない", name)
			}
		})
	}
	// 未知のキーは黙って無視せず弾く（単位付きフィールド名の打ち間違い対策）
	if _, err := Load(write(t, sample+"unknown_key: 1\n")); err == nil {
		t.Error("未知のトップレベルキーがエラーにならない")
	}
}

// TestLookupByID はマスタから ID で引けること、未登録 ID のエラーに
// 登録済み ID が列挙されることを確認する。
func TestLookupByID(t *testing.T) {
	const multi = `
ssrs:
  B:
    lat: 35
    lon: 137
    alt: 0
    max_range_m: 400000
    interrogation: {around_time_sec: 4, mode_pattern: AC, interval_pattern_ns: [2906500]}
  A:
    lat: 36
    lon: 137
    alt: 0
    max_range_m: 400000
    interrogation: {around_time_sec: 4, mode_pattern: AC, interval_pattern_ns: [2906500]}
stations:
  T2: {lat: 35.1, lon: 137, alt: 0}
  T1: {lat: 35.2, lon: 137, alt: 0}
`
	f, err := Load(write(t, multi))
	if err != nil {
		t.Fatal(err)
	}
	if got := f.SSRIDs(); len(got) != 2 || got[0] != "A" || got[1] != "B" {
		t.Errorf("SSRIDs = %v, 期待 [A B]", got)
	}
	if got := f.StationIDs(); len(got) != 2 || got[0] != "T1" || got[1] != "T2" {
		t.Errorf("StationIDs = %v, 期待 [T1 T2]", got)
	}
	ssr, err := f.SSR("A")
	if err != nil {
		t.Fatal(err)
	}
	if ssr.ID != "A" || *ssr.Lat != 36 {
		t.Errorf("SSR(A) = %+v, ID と lat がキーに対応していない", ssr)
	}
	st, err := f.Station("T1")
	if err != nil {
		t.Fatal(err)
	}
	if st.ID != "T1" || *st.Lat != 35.2 {
		t.Errorf("Station(T1) = %+v, ID と lat がキーに対応していない", st)
	}
	if _, err := f.SSR("Z"); err == nil || !strings.Contains(err.Error(), "A, B") {
		t.Errorf("未登録 SSR のエラーに登録済み ID が無い: %v", err)
	}
	if _, err := f.Station("Z"); err == nil || !strings.Contains(err.Error(), "T1, T2") {
		t.Errorf("未登録測定局のエラーに登録済み ID が無い: %v", err)
	}
}

// TestRejectsMasterShapeErrors はマスタ全体の形に関するエラーを確認する。
// 重複 ID は YAML の段階で弾かれ、検証エラーは該当する ID を含む。
func TestRejectsMasterShapeErrors(t *testing.T) {
	dup := sample + `  KX90:
    lat: 35
    lon: 137
    alt: 0
`
	if _, err := Load(write(t, dup)); err == nil {
		t.Error("stations の重複 ID がエラーにならない")
	}
	noStations := sample[:strings.Index(sample, "stations:")]
	if _, err := Load(write(t, noStations)); err == nil {
		t.Error("stations が無いのにエラーにならない")
	}
	bad := replaceLine(sample, "      mode_pattern: ACAC", "      mode_pattern: AXC")
	if _, err := Load(write(t, bad)); err == nil || !strings.Contains(err.Error(), "ssrs.KX90S") {
		t.Errorf("検証エラーにどの SSR かが無い: %v", err)
	}
}

// TestRejectsIncompletePosition は位置の 3 要素が揃わないと弾かれることを
// 確認する。特に alt の省略を 0 扱いにしてはならない。
func TestRejectsIncompletePosition(t *testing.T) {
	cases := map[string]string{
		"alt 欠落":   replaceLine(sample, "    lat: 34.8583717981495\n    lon: 136.810685698149\n    alt: 0\n", "    lat: 34.8583717981495\n    lon: 136.810685698149\n"),
		"lon 欠落":   replaceLine(sample, "    lon: 136.810685698149\n", ""),
		"lat が範囲外": replaceLine(sample, "    lat: 34.8583717981495\n", "    lat: 91\n"),
		"lon が範囲外": replaceLine(sample, "    lon: 136.810685698149\n", "    lon: 181\n"),
		"旧形式の x/y": replaceLine(sample, "    lat: 34.8583717981495\n    lon: 136.810685698149\n    alt: 0\n", "    x: 0\n    y: 0\n"),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(write(t, body)); err == nil {
				t.Errorf("%s がエラーにならない", name)
			}
		})
	}
}

func replaceLine(body, from, to string) string {
	i := strings.Index(body, from)
	if i < 0 {
		panic("行が見つからない: " + from)
	}
	return body[:i] + to + body[i+len(from):]
}

// TestAnalysisSection は analysis 節が省略可で、書いた項目だけが設定に載り、
// 未知のキーがエラーになることを確認する。
func TestAnalysisSection(t *testing.T) {
	f, _, _ := load(t, sample)
	if f.Analysis.Interrogator.AmplitudeGateDbm != nil || f.Analysis.PSSR.MinReplies != nil {
		t.Errorf("省略した analysis が nil でない: %+v", f.Analysis)
	}

	f, _, _ = load(t, sample+`
analysis:
  interrogator:
    amplitude_gate_dbm: -40
  pssr:
    max_gap: 0
    max_altitude_ft: 70000
`)
	a := f.Analysis
	if a.Interrogator.AmplitudeGateDbm == nil || *a.Interrogator.AmplitudeGateDbm != -40 || a.Interrogator.GateNs != nil {
		t.Errorf("interrogator: %+v", a.Interrogator)
	}
	if a.PSSR.MaxGap == nil || *a.PSSR.MaxGap != 0 || a.PSSR.MaxAltitudeFt == nil || *a.PSSR.MaxAltitudeFt != 70000 || a.PSSR.MinReplies != nil {
		t.Errorf("pssr: %+v", a.PSSR)
	}

	if _, err := Load(write(t, sample+`
analysis:
  pssr:
    min_replys: 4
`)); err == nil {
		t.Error("未知のキーがエラーにならない")
	}
}
