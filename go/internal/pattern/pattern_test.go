package pattern

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"testing"
)

// テストベクタは tools/gen_patternvectors.py が Python 側の
// InterrogationPattern から直接生成する。

type vector struct {
	Name          string             `json:"name"`
	StaggerNs     []int64            `json:"stagger_ns"`
	Quest         string             `json:"quest"`
	ModesIn       []uint8            `json:"modes_in"`
	RawLength     int64              `json:"raw_length"`
	RawPeriod     int64              `json:"raw_period"`
	RawIntervals  []int64            `json:"raw_intervals"`
	RawModes      []uint8            `json:"raw_modes"`
	MeanPRI       string             `json:"mean_pri"`
	Cumulative    [][2]int64         `json:"cumulative"`
	Delta         [][3]int64         `json:"delta"`
	ModeAt        [][2]int64         `json:"mode_at"`
	RelativeTimes map[string][]int64 `json:"relative_times"`
}

func loadVectors(t *testing.T) []vector {
	t.Helper()
	raw, err := os.ReadFile("testdata/patternvectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v []vector
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func build(t *testing.T, v vector) *Pattern {
	t.Helper()
	modes, err := ParseModes(v.Quest)
	if err != nil {
		t.Fatalf("%s: ParseModes: %v", v.Name, err)
	}
	for i := range modes {
		if modes[i] != v.ModesIn[i] {
			t.Fatalf("%s: ParseModes[%d] = %d, Python INTG_MODE_CODE = %d", v.Name, i, modes[i], v.ModesIn[i])
		}
	}
	p, err := FromStagger(v.StaggerNs, modes)
	if err != nil {
		t.Fatalf("%s: FromStagger: %v", v.Name, err)
	}
	return p
}

// TestCumulativeAndDeltaAreReductionInvariant は簡約の正当性そのものを検証する。
//
// Go 側は質問パターンを最小周期へ簡約するので L は Python と異なりうるが、
// 累積時刻・経過時間・質問種別は「先頭から数えて何発目か」で決まる量であり、
// 表現の取り方に依存しない。ここが一致していれば、簡約によって
// 変わるのは DP の状態数と _link_steps の候補間隔だけだと言い切れる。
func TestCumulativeAndDeltaAreReductionInvariant(t *testing.T) {
	for _, v := range loadVectors(t) {
		t.Run(v.Name, func(t *testing.T) {
			p := build(t, v)

			for _, c := range v.Cumulative {
				if got := p.Cumulative(c[0]); got != c[1] {
					t.Errorf("Cumulative(%d) = %d, Python = %d", c[0], got, c[1])
				}
			}
			for _, d := range v.Delta {
				if got := p.Delta(d[0], d[1]); got != d[2] {
					t.Errorf("Delta(%d, %d) = %d, Python = %d", d[0], d[1], got, d[2])
				}
			}
			for _, m := range v.ModeAt {
				if got := int64(p.ModeAt(m[0])); got != m[1] {
					t.Errorf("ModeAt(%d) = %d, Python = %d", m[0], got, m[1])
				}
			}
		})
	}
}

func TestRelativeTimesMatchesPython(t *testing.T) {
	for _, v := range loadVectors(t) {
		t.Run(v.Name, func(t *testing.T) {
			p := build(t, v)
			for key, want := range v.RelativeTimes {
				var p0, count int
				if _, err := fmt.Sscanf(key, "%d:%d", &p0, &count); err != nil {
					t.Fatalf("キー %q: %v", key, err)
				}
				got := p.RelativeTimes(int64(p0), count)
				if len(got) != len(want) {
					t.Errorf("RelativeTimes(%d, %d): 長さ %d, Python = %d", p0, count, len(got), len(want))
					continue
				}
				for i := range want {
					if got[i] != want[i] {
						t.Errorf("RelativeTimes(%d, %d)[%d] = %d, Python = %d", p0, count, i, got[i], want[i])
						break
					}
				}
			}
		})
	}
}

// TestReductionShrinksOnlyRedundantPatterns は簡約が効くべき場合にだけ
// 効くことを確認する。"ACAC" は "AC" へ縮み、周期が本当に 6 のものは縮まない。
func TestReductionShrinksOnlyRedundantPatterns(t *testing.T) {
	want := map[string]int64{
		"centrair":       2, // ACAC -> AC。main.py のハードコード [3,5] と同じ L
		"centrair_min":   2,
		"kx00":           2,
		"single":         1,
		"stagger3":       6, // スタガ 3 * 種別 2 で真に周期 6
		"stagger2_mode3": 6,
		"redundant":      2, // 冗長なスタガと冗長な種別の両方が縮む
		"mode4":          4,
	}
	for _, v := range loadVectors(t) {
		p := build(t, v)
		if got := p.Length(); got != want[v.Name] {
			t.Errorf("%s: 簡約後 L = %d, 期待 %d (Python 展開時 %d)", v.Name, got, want[v.Name], v.RawLength)
		}
		// 周期は簡約後の L 倍で元の period に一致する
		if v.RawPeriod%p.Period() != 0 || v.RawPeriod/p.Period() != v.RawLength/p.Length() {
			t.Errorf("%s: period %d が Python の %d と整合しない", v.Name, p.Period(), v.RawPeriod)
		}
		if got, want := p.MeanPRI(), mustHex(t, v.MeanPRI); got != want {
			t.Errorf("%s: MeanPRI = %v, Python = %v", v.Name, got, want)
		}
	}
}

func TestNewRejectsBadInput(t *testing.T) {
	if _, err := New(nil, nil); err == nil {
		t.Error("空パターンがエラーにならない")
	}
	if _, err := New([]int64{100, 0}, []uint8{3, 5}); err == nil {
		t.Error("非正の PRI がエラーにならない")
	}
	if _, err := New([]int64{100, -1}, []uint8{3, 5}); err == nil {
		t.Error("負の PRI がエラーにならない")
	}
	if _, err := ParseModes("AXC"); err == nil {
		t.Error("不正な質問種別文字がエラーにならない")
	}
	if _, err := ParseModes(""); err == nil {
		t.Error("空の質問種別がエラーにならない")
	}
}

func mustHex(t *testing.T, s string) float64 {
	t.Helper()
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
