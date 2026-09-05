// Package npcompat は Python / numpy の数値意味論を Go で再現する。
//
// 移植元の Python 実装と .intg 出力をバイト単位で一致させるのが目的なので、
// ここにある関数は「Go として自然な挙動」ではなく「Python / numpy が実際に
// 返す値」を返す。差異が生じやすいのは次の 4 点で、いずれも testdata の
// 実測ベクタ（tools/gen_npvectors.py が numpy から直接生成）で検証している。
//
//   - 丸め       : Python の round() と np.rint は偶数丸め（Go の math.Round は違う）
//   - 除算・剰余 : Python の // と % は床除算（Go の / と % は 0 方向切り捨て）
//   - 総和       : np.sum は素朴な逐次加算ではなく pairwise summation
//   - 型変換     : float64 → 整数は 0 方向切り捨て（丸めではない）
package npcompat

import (
	"math"
	"slices"
)

// RoundHalfEven は Python の round(x) / np.rint(x) と同じ偶数丸めを行う。
func RoundHalfEven(x float64) float64 { return math.RoundToEven(x) }

// RoundToInt64 は Python の round(x) が返す int に相当する。
func RoundToInt64(x float64) int64 { return int64(math.RoundToEven(x)) }

// FloorDiv は Python の a // b（床除算）。
func FloorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

// FloorMod は Python の a % b。結果は b と同じ符号になる。
//
// 移植で最も壊れやすい箇所。analyze.py の phase_shift % L は
// phase_shift が負になりうるため、Go の % をそのまま使うと符号が反転する。
func FloorMod(a, b int64) int64 {
	m := a % b
	if m != 0 && ((m < 0) != (b < 0)) {
		m += b
	}
	return m
}

// Mod は np.mod(x, y) を再現する。math.Mod（= C の fmod）ではなく
// 除数の符号に合わせる方。方位角の [0, 2pi) への畳み込みに使う。
func Mod(x, y float64) float64 {
	m := math.Mod(x, y)
	if y == 0 {
		return m
	}
	if m != 0 {
		if (y < 0) != (m < 0) {
			m += y
		}
	} else {
		m = math.Copysign(0, y)
	}
	return m
}

// TruncToUint32 は numpy が float64 配列を <u4 フィールドへ代入するときの
// C 形式キャスト（0 方向切り捨て）を再現する。四捨五入ではない。
func TruncToUint32(x float64) uint32 { return uint32(math.Trunc(x)) }

const pwBlocksize = 128

// Sum は np.sum(a) の pairwise summation を再現する。
// 逐次加算とは丸め誤差の出方が違うため、残差二乗和のように
// 結果がそのまま閾値判定に使われる箇所では区別が必要になる。
func Sum(a []float64) float64 {
	n := len(a)
	switch {
	case n < 8:
		res := 0.0
		for _, v := range a {
			res += v
		}
		return res
	case n <= pwBlocksize:
		var r [8]float64
		copy(r[:], a[:8])
		i := 8
		for ; i < n-(n%8); i += 8 {
			r[0] += a[i+0]
			r[1] += a[i+1]
			r[2] += a[i+2]
			r[3] += a[i+3]
			r[4] += a[i+4]
			r[5] += a[i+5]
			r[6] += a[i+6]
			r[7] += a[i+7]
		}
		res := ((r[0] + r[1]) + (r[2] + r[3])) + ((r[4] + r[5]) + (r[6] + r[7]))
		for ; i < n; i++ {
			res += a[i]
		}
		return res
	default:
		n2 := n / 2
		n2 -= n2 % 8
		return Sum(a[:n2]) + Sum(a[n2:])
	}
}

// MeanInt64 は int64 配列に対する np.mean を再現する。
//
// numpy は float64 のアキュムレータで合計してから割るが、要素も部分和も
// 2^53 未満の整数に収まる範囲では合計は厳密なので、int64 で合計してから
// 1 回だけ割る本実装とビット単位で一致する。呼び出し側は残差（ns 単位、
// 高々 1e5 程度）と段数しか渡さないためこの前提は常に成り立つ。
func MeanInt64(v []int64) float64 {
	var s int64
	for _, x := range v {
		s += x
	}
	return float64(s) / float64(len(v))
}

// Median は np.median(a) を再現する。a は変更しない。
func Median(a []float64) float64 {
	n := len(a)
	if n == 0 {
		return math.NaN()
	}
	s := slices.Clone(a)
	slices.Sort(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// SearchSortedLeft は np.searchsorted(a, v, side="left") を再現する。
func SearchSortedLeft(a []float64, v float64) int {
	lo, hi := 0, len(a)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if a[mid] < v {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}

// SearchSortedRight は np.searchsorted(a, v, side="right") を再現する。
func SearchSortedRight(a []float64, v float64) int {
	lo, hi := 0, len(a)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if a[mid] <= v {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}
