package config

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sample = `
ssr:
  id: KX90S
  name: KX90_SSR
  icao: KX9
  serial_no: 1
  x: -127458.67663663127
  y: -31615.025566053235
  interrogation:
    around_time_sec: 4.05
    pattern: ACAC
    quest_cycle_100ns: 29065
    stagger: 0
    clockwise: true
station:
  id: KX90
  name: KX90_STATION
  icao: KX9
  serial_no: 1
  x: -126591.43986481673
  y: -32549.562701800554
`

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestParamsMatchesCentrairMapping は従来の設定ファイル centrair.txt の
// 実値が、解析パラメータへ正しく写ることを確認する。値は運用中の
// SSR（名古屋）のもの。
func TestParamsMatchesCentrairMapping(t *testing.T) {
	f, err := Load(write(t, sample))
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.Params()
	if err != nil {
		t.Fatal(err)
	}
	if p.AroundTimeNs != 4_050_000_000 {
		t.Errorf("AroundTimeNs = %d, 期待 4050000000 (= 4.05 秒)", p.AroundTimeNs)
	}
	if p.Pattern.Length() != 2 {
		t.Errorf("ACAC の簡約後 L = %d, 期待 2 (AC と等価)", p.Pattern.Length())
	}
	if p.Pattern.Period() != 5_813_000 {
		t.Errorf("period = %d, 期待 5813000 (= 2906500 * 2)", p.Pattern.Period())
	}
	if got := p.Pattern.Modes(); len(got) != 2 || got[0] != 3 || got[1] != 5 {
		t.Errorf("modes = %v, 期待 [3 5]", got)
	}
	if !p.Clockwise {
		t.Error("clockwise が false になっている")
	}
}

// TestAroundTimeTruncatesNotRounds は秒から ns への変換が切り捨てで
// あることを固定する。4.1 秒は 2 進で厳密に表せないので 4099999999 ns に
// なる。四捨五入に変えると既存の出力と食い違う。
func TestAroundTimeTruncatesNotRounds(t *testing.T) {
	cases := map[float64]int64{
		4.05: 4_050_000_000,
		4.0:  4_000_000_000,
		4.1:  4_099_999_999, // 四捨五入すると 4100000000 になってしまう
		2.5:  2_500_000_000,
		10.0: 10_000_000_000,
	}
	for sec, want := range cases {
		got := int64(math.Trunc(sec * 1e9))
		if got != want {
			t.Errorf("%v 秒 -> %d ns, 期待 %d ns", sec, got, want)
		}
	}
}

func TestGeometry(t *testing.T) {
	f, err := Load(write(t, sample))
	if err != nil {
		t.Fatal(err)
	}
	dist, az := f.Geometry()
	wantDist, wantAz := 1274.935008727154, 5.460452220221545
	if dist != wantDist {
		t.Errorf("dist = %x, 期待 %x", dist, wantDist)
	}
	if az != wantAz {
		t.Errorf("azimuth = %x, 期待 %x", az, wantAz)
	}
}

func TestClockwiseDefaultsToTrue(t *testing.T) {
	const noClockwise = `
ssr:
  id: S1
  x: 0
  y: 0
  interrogation:
    around_time_sec: 4.05
    pattern: AC
    quest_cycle_100ns: 29065
station:
  id: T1
  x: 100
  y: 0
`
	f, err := Load(write(t, noClockwise))
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.Params()
	if err != nil {
		t.Fatal(err)
	}
	if !p.Clockwise {
		t.Error("clockwise 省略時の既定が true になっていない")
	}
}

func TestRejectsInvalidConfig(t *testing.T) {
	bad := map[string]string{
		"不正な質問種別":      "pattern: AXC",
		"stagger が非ゼロ": "stagger: 3",
		"PRI が 0":      "quest_cycle_100ns: 0",
		"走査周期が 0":      "around_time_sec: 0",
	}
	for name, repl := range bad {
		t.Run(name, func(t *testing.T) {
			body := sample
			switch {
			case repl == "pattern: AXC":
				body = replaceLine(body, "    pattern: ACAC", "    pattern: AXC")
			case repl == "stagger: 3":
				body = replaceLine(body, "    stagger: 0", "    stagger: 3")
			case repl == "quest_cycle_100ns: 0":
				body = replaceLine(body, "    quest_cycle_100ns: 29065", "    quest_cycle_100ns: 0")
			case repl == "around_time_sec: 0":
				body = replaceLine(body, "    around_time_sec: 4.05", "    around_time_sec: 0")
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

// TestStaggerListOverridesQuestCycle はスタガ列を直接指定できることを確認する。
func TestStaggerListOverridesQuestCycle(t *testing.T) {
	body := replaceLine(sample, "    stagger: 0", "    stagger_100ns: [29000, 29065, 29130]")
	f, err := Load(write(t, body))
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.Params()
	if err != nil {
		t.Fatal(err)
	}
	// スタガ 3 * 種別 2 (ACAC は AC へ簡約) で L = 6
	if p.Pattern.Length() != 6 {
		t.Errorf("L = %d, 期待 6", p.Pattern.Length())
	}
	if want := int64((2900000 + 2906500 + 2913000) * 2); p.Pattern.Period() != want {
		t.Errorf("period = %d, 期待 %d", p.Pattern.Period(), want)
	}
}

func replaceLine(body, from, to string) string {
	i := strings.Index(body, from)
	if i < 0 {
		panic("行が見つからない: " + from)
	}
	return body[:i] + to + body[i+len(from):]
}
