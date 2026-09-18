package pipeline_test

import (
	"math"
	"testing"

	"pssrx/internal/config"
	"pssrx/internal/pipeline"
)

func boolp(b bool) *bool { return &b }

// TestInterrogatorParams は設定の質問の仕様が解析パラメータへ正しく写る
// ことを確認する。値は運用中の SSR（名古屋）のもの。
func TestInterrogatorParams(t *testing.T) {
	p, err := pipeline.InterrogatorParams(config.InterrogationSpec{
		AroundTimeSec:     4.05,
		ModePattern:       "ACAC",
		IntervalPatternNs: []int64{2906500},
		Clockwise:         boolp(true),
	})
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
		if got := int64(math.Trunc(sec * 1e9)); got != want {
			t.Errorf("math.Trunc: %v 秒 -> %d ns, 期待 %d ns", sec, got, want)
		}
		p, err := pipeline.InterrogatorParams(config.InterrogationSpec{
			AroundTimeSec: sec, ModePattern: "AC", IntervalPatternNs: []int64{2906500},
		})
		if err != nil {
			t.Fatal(err)
		}
		if p.AroundTimeNs != want {
			t.Errorf("%v 秒 -> %d ns, 期待 %d ns", sec, p.AroundTimeNs, want)
		}
	}
}

func TestClockwiseDefaultsToTrue(t *testing.T) {
	p, err := pipeline.InterrogatorParams(config.InterrogationSpec{
		AroundTimeSec: 4.05, ModePattern: "AC", IntervalPatternNs: []int64{2906500},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !p.Clockwise {
		t.Error("clockwise 省略時の既定が true になっていない")
	}
}

// TestStaggeredIntervals は間隔の列（スタガ運用）が種別の列と最小公倍数で
// 組み合わさることを確認する。
func TestStaggeredIntervals(t *testing.T) {
	p, err := pipeline.InterrogatorParams(config.InterrogationSpec{
		AroundTimeSec:     4.05,
		ModePattern:       "ACAC",
		IntervalPatternNs: []int64{2900000, 2906500, 2913000},
	})
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
