// Package pattern は SSR の質問パターン（PRI 列と質問種別列）を表す。
// 移植元の interrogator/domain.py の InterrogationPattern に対応する。
package pattern

import (
	"errors"
	"fmt"
	"slices"

	"pssrx/internal/npcompat"
)

// ModeCode は設定の質問種別文字と内部コードの対応。domain.py の INTG_MODE_CODE。
var ModeCode = map[rune]uint8{'1': 1, '2': 2, '3': 3, 'A': 3, 'B': 4, 'C': 5, 'D': 6}

// Pattern は 1 周期分の (次の質問までの間隔 [ns], 質問種別) の列。
//
// 入力は「間隔」、内部表現は「累積時刻」。スタガ位相と種別位相を単一の
// 位相 p ∈ [0, L) に畳んであるため、連鎖検出の DP 状態が (観測, 位相) に収まる。
//
// 生成後は不変。Python 側は InterrogationParameter.pattern が property で
// 毎回作り直していたが、ここでは 1 度だけ構築して使い回す（結果は変わらない）。
type Pattern struct {
	intervals []int64
	modes     []uint8
	offsets   []int64
	period    int64
	length    int64
}

// New は (間隔, 種別) の列からパターンを作る。列は最小周期へ簡約される。
func New(intervals []int64, modes []uint8) (*Pattern, error) {
	if len(intervals) == 0 || len(intervals) != len(modes) {
		return nil, errors.New("EmptyPattern: 間隔列と種別列は同じ長さで 1 要素以上必要")
	}
	for i, v := range intervals {
		if v <= 0 {
			return nil, fmt.Errorf("NonPositivePri: index=%d value=%d", i, v)
		}
	}
	intervals, modes = reduce(intervals, modes)

	p := &Pattern{
		intervals: slices.Clone(intervals),
		modes:     slices.Clone(modes),
		offsets:   make([]int64, len(intervals)),
		length:    int64(len(intervals)),
	}
	var acc int64
	for i, v := range intervals {
		p.offsets[i] = acc
		acc += v
	}
	p.period = acc
	return p, nil
}

// FromStagger は設定の PRI 列と種別列から展開する。domain.py の from_stagger。
//
//	L = lcm(len(stagger), len(modes)),  steps[i] = (stagger[i%ns], modes[i%nm])
//
// 展開後に最小周期へ簡約する。たとえば種別 "ACAC" は "AC" と等価だが、
// L は DP の状態数と連結候補の間隔 (L*PRI) を決めるため、簡約しないと
// _link_steps の棄却判定の厳しさが変わってしまう。移植元の main.py は
// 簡約済みの [3,5] を直接渡していたので、簡約が既存出力との一致条件になる。
func FromStagger(staggerNs []int64, modes []uint8) (*Pattern, error) {
	ns, nm := int64(len(staggerNs)), int64(len(modes))
	if ns == 0 || nm == 0 {
		return nil, errors.New("EmptyPattern")
	}
	l := lcm(ns, nm)
	iv := make([]int64, l)
	md := make([]uint8, l)
	for i := int64(0); i < l; i++ {
		iv[i] = staggerNs[i%ns]
		md[i] = modes[i%nm]
	}
	return New(iv, md)
}

// ParseModes は "ACAC" のような質問種別文字列を内部コード列に変換する。
func ParseModes(s string) ([]uint8, error) {
	if s == "" {
		return nil, errors.New("質問種別が空です")
	}
	out := make([]uint8, 0, len(s))
	for _, r := range s {
		c, ok := ModeCode[r]
		if !ok {
			return nil, fmt.Errorf("質問種別として不適切な文字が含まれています: %q (文字列 %q)", r, s)
		}
		out = append(out, c)
	}
	return out, nil
}

// reduce は (間隔, 種別) の列を最小の繰り返し単位へ縮める。
func reduce(iv []int64, md []uint8) ([]int64, []uint8) {
	n := len(iv)
	for p := 1; p < n; p++ {
		if n%p != 0 {
			continue
		}
		ok := true
		for i := p; i < n && ok; i++ {
			ok = iv[i] == iv[i%p] && md[i] == md[i%p]
		}
		if ok {
			return iv[:p], md[:p]
		}
	}
	return iv, md
}

func gcd(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

func lcm(a, b int64) int64 { return a / gcd(a, b) * b }

// Length は 1 周期の質問数 L。
func (p *Pattern) Length() int64 { return p.length }

// Period は 1 周期の所要時間 [ns]。
func (p *Pattern) Period() int64 { return p.period }

// Intervals は 1 周期分の PRI 列。呼び出し側は変更してはならない。
func (p *Pattern) Intervals() []int64 { return p.intervals }

// Modes は 1 周期分の質問種別列。呼び出し側は変更してはならない。
func (p *Pattern) Modes() []uint8 { return p.modes }

// MeanPRI は 1 質問あたりの平均間隔 [ns]。
func (p *Pattern) MeanPRI() float64 { return float64(p.period) / float64(p.length) }

// ModeAt は質問番号 n の質問種別。
func (p *Pattern) ModeAt(n int64) uint8 { return p.modes[npcompat.FloorMod(n, p.length)] }

// Cumulative は質問番号 n のパターン先頭基準の累積時刻 [ns]（厳密整数）。
// n が負でも Python の divmod と同じ床除算で扱う。
func (p *Pattern) Cumulative(n int64) int64 {
	q := npcompat.FloorDiv(n, p.length)
	r := npcompat.FloorMod(n, p.length)
	return q*p.period + p.offsets[r]
}

// Delta は位相 p から d ステップ進んだときの経過時間 [ns]。p mod L のみに依存する。
func (p *Pattern) Delta(phase, d int64) int64 {
	return p.Cumulative(phase+d) - p.Cumulative(phase)
}

// RelativeTimes は s ∈ [0, count) について Delta(p0, s) を返す。
//
// Delta を 1 件ずつ呼ぶと 1 ブラケットで千数百回になるため、位相 p0 から
// 始まる間隔列を巡回させた累積和で一括計算する。
func (p *Pattern) RelativeTimes(p0 int64, count int) []int64 {
	if count <= 0 {
		return nil
	}
	out := make([]int64, count)
	shift := npcompat.FloorMod(p0, p.length)
	var acc int64
	for i := 1; i < count; i++ {
		acc += p.intervals[(int64(i-1)+shift)%p.length]
		out[i] = acc
	}
	return out
}
