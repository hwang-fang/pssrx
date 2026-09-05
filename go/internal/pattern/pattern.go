// Package pattern は SSR の質問パターン（PRI 列と質問種別列）を表す。
package pattern

import (
	"errors"
	"fmt"
	"slices"
)

// ModeCode は設定に書く質問種別の文字と、データ上の種別コードの対応。
var ModeCode = map[rune]uint8{'1': 1, '2': 2, '3': 3, 'A': 3, 'B': 4, 'C': 5, 'D': 6}

// Pattern は 1 周期分の (次の質問までの間隔 [ns], 質問種別) の列。
//
// 入力は「間隔」、内部表現は「累積時刻」。スタガ位相と種別位相を単一の
// 位相 p ∈ [0, L) に畳んであるため、連鎖検出の DP 状態が (観測, 位相) に収まる。
//
// 生成後は不変なので、1 度組み立てたら解析中は使い回せる。
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

// FromStagger は PRI 列と種別列を噛み合わせて 1 周期ぶんへ展開する。
//
//	L = lcm(len(stagger), len(modes)),  steps[i] = (stagger[i%ns], modes[i%nm])
//
// 展開後は必ず最小周期へ簡約する。種別 "ACAC" と "AC" は物理的には同じ
// パターンだが、L はそのままドウェル間の連結候補の間隔 (L*PRI) になる。
// 簡約しないと候補が疎になり、連結の余裕判定が実際より甘くなって、
// 本来棄却すべきドウェル対を通してしまう。DP の状態数も L に比例するので、
// 簡約は速度の面でも効く。
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
// 約数となる長さを短い方から試し、最初に全体を張れたものを採る。
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

// NormalizePhase は任意の質問番号を [0, L) の位相へ畳み込む。
//
// 連鎖どうしの位相差は負になりうるが、位相は [0, L) の値でなければ
// パターンの索引として使えない。Go の % は被除数の符号を引き継ぐので、
// ここで床方向へ寄せる。
func (p *Pattern) NormalizePhase(n int64) int64 {
	r := n % p.length
	if r < 0 {
		r += p.length
	}
	return r
}

// ModeAt は質問番号 n の質問種別。
func (p *Pattern) ModeAt(n int64) uint8 { return p.modes[p.NormalizePhase(n)] }

// Cumulative は質問番号 n のパターン先頭基準の累積時刻 [ns]。
// 整数演算だけで求めるので誤差は無い。n が負ならパターン先頭より前を指す。
func (p *Pattern) Cumulative(n int64) int64 {
	r := p.NormalizePhase(n)
	return (n-r)/p.length*p.period + p.offsets[r]
}

// Delta は位相 p から d ステップ進んだときの経過時間 [ns]。p mod L のみに依存する。
func (p *Pattern) Delta(phase, d int64) int64 {
	return p.Cumulative(phase+d) - p.Cumulative(phase)
}

// RelativeTimes は s ∈ [0, count) について Delta(p0, s) をまとめて返す。
//
// ブラケット 1 本ぶんで千数百段になるので、Delta を 1 段ずつ呼ばず、
// 位相 p0 から始まる間隔列を巡回させながら累積して一括で埋める。
func (p *Pattern) RelativeTimes(p0 int64, count int) []int64 {
	if count <= 0 {
		return nil
	}
	out := make([]int64, count)
	shift := p.NormalizePhase(p0)
	var acc int64
	for i := 1; i < count; i++ {
		acc += p.intervals[(int64(i-1)+shift)%p.length]
		out[i] = acc
	}
	return out
}
