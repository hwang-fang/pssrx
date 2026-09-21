package plot

import "testing"

// TestSquawk はビット配置どおりにスコークが組み立つことを確認する。
func TestSquawk(t *testing.T) {
	cases := []struct {
		code uint16
		want uint16
	}{
		{0, 0},
		{0o7777, 0o7777},
		// 実データで頻出の生符号。A1=1 A2=1 A4=0 → A=3, B1=1 B2=0 B4=1 → B=5,
		// C1=1 C2=1 C4=0 → C=3, D1=0 D2=0 D4=1 → D=4
		{0o1656, 0o3534},
		{1 << bitA1, 0o1000},
		{1 << bitA4, 0o4000},
		{1 << bitD1, 0o0001},
		{1 << bitC4, 0o0040},
	}
	for _, c := range cases {
		if got := Squawk(c.code); got != c.want {
			t.Errorf("Squawk(%04o) = %04o, 期待 %04o", c.code, got, c.want)
		}
	}
}

// TestAltitude は Gillham 復号を既知の対応で固定する。
func TestAltitude(t *testing.T) {
	// name -> bit で符号を組む
	mk := func(set ...int) uint16 {
		var c uint16
		for _, b := range set {
			c |= 1 << b
		}
		return c
	}
	cases := []struct {
		name string
		code uint16
		want int
		ok   bool
	}{
		// 500 ft 段 0（グレイ 0）。C1 C2 C4 のグレイ値 → n100: 001→1, 011→2, 010→3, 110→4, 100→7→5
		{"最小 -1200 ft (C4)", mk(bitC4), -1200, true},
		{"-1100 ft (C2 C4)", mk(bitC2, bitC4), -1100, true},
		{"-1000 ft (C2)", mk(bitC2), -1000, true},
		{"-900 ft (C1 C2)", mk(bitC1, bitC2), -900, true},
		{"-800 ft (C1)", mk(bitC1), -800, true},
		// 段が奇数のとき C の向きが反転する: 段 1 (B4) と C1 (n100 = 5 → 6 − 5 = 1) → 500 + 100 − 1300
		{"奇数段の反転", mk(bitB4, bitC1), -700, true},
		// 実データ: 降下中の機体の列 0132 → 0136 → 0134 → 0124 → 0126
		{"実データ 0o0132", 0o0132, 5500, true},
		{"実データ 0o0136", 0o0136, 5400, true},
		{"実データ 0o0134", 0o0134, 5300, true},
		{"実データ 0o0124", 0o0124, 5200, true},
		{"実データ 0o0126", 0o0126, 5100, true},
		{"D1 が立つ", mk(bitD1, bitC1), 0, false},
		{"C が 000 (n100 = 0)", 0, 0, false},
		{"C が 101 (n100 = 6)", mk(bitC1, bitC4), 0, false},
		{"C が 111 (n100 = 5)", mk(bitC1, bitC2, bitC4), 0, false},
	}
	for _, c := range cases {
		got, ok := Altitude(c.code)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("%s: Altitude(%012b) = (%d, %v), 期待 (%d, %v)", c.name, c.code, got, ok, c.want, c.ok)
		}
	}
}

// TestAltitudeMonotonic は 100 ft 刻みで連続する符号列が単調に復号されることを
// 確認する。Gillham 符号は隣り合う高度で 1 ビットしか変わらない。
func TestAltitudeMonotonic(t *testing.T) {
	// 復号できる符号を全部集め、高度でならべて 100 ft 刻みに抜けが無いことを見る
	seen := map[int]uint16{}
	for code := uint16(0); code < 1<<12; code++ {
		if ft, ok := Altitude(code); ok {
			if prev, dup := seen[ft]; dup {
				t.Errorf("高度 %d ft に 2 つの符号 %012b と %012b", ft, prev, code)
			}
			seen[ft] = code
		}
	}
	for ft := -1200; ft <= 126_700; ft += 100 {
		if _, ok := seen[ft]; !ok {
			t.Errorf("高度 %d ft に対応する符号が無い", ft)
		}
	}
	// 隣の高度とはハミング距離 1
	for ft := -1200; ft < 126_700; ft += 100 {
		a, b := seen[ft], seen[ft+100]
		if d := a ^ b; d&(d-1) != 0 {
			t.Errorf("%d -> %d ft で 2 ビット以上変わる: %012b -> %012b", ft, ft+100, a, b)
		}
	}
}
