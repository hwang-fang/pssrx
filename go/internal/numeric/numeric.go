// Package numeric は interrogator の演算規約をまとめる。
//
// ここにある関数は Go の標準的な演算とわざと違う挙動をする。
//
//	丸め       偶数丸め（0.5 は近い方の偶数へ。math.Round は 0 から離れる方向）
//	整数除算   床方向（Go の / は 0 方向切り捨て）
//	剰余       結果の符号は除数に合わせる（Go の % は被除数に合わせる）
//	実数剰余   同上（math.Mod は被除数に合わせる）
//	総和       pairwise summation（逐次加算とは丸め誤差の出方が違う）
//	整数化     0 方向切り捨て（四捨五入ではない）
//
// これらは好みではなく出力フォーマットの一部である。intg の方位角は
// [0, 2pi) を 32 bit に量子化して書き出すので、最下位ビット 1 個ぶんの
// 差（約 1.5e-9 rad）でもファイルの中身が変わる。すでに蓄積されている
// intg 資産と突き合わせられる限り、この規約は変更できない。
//
// 素の math.Round や Go の / と % を使うと、境界に当たったレコードだけが
// 静かにずれる。壊れ方が派手でないぶん見つけにくいので、演算はこの
// パッケージを経由させること。
//
// 各規約が返すべき値は testdata/vectors.json に固定してある。
package numeric

import (
	"math"
	"slices"
)

// RoundHalfEven は x を偶数丸めする。0.5 はより近い偶数へ倒れるので、
// RoundHalfEven(0.5) == 0、RoundHalfEven(1.5) == 2 となる。
func RoundHalfEven(x float64) float64 { return math.RoundToEven(x) }

// RoundToInt64 は RoundHalfEven の結果を整数で返す。
func RoundToInt64(x float64) int64 { return int64(math.RoundToEven(x)) }

// FloorDiv は a / b を床方向へ丸めた商を返す。
// 負の被除数で Go の / と食い違う（FloorDiv(-7, 3) == -3、-7/3 == -2）。
func FloorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

// FloorMod は FloorDiv に対応する剰余を返す。結果は b と同じ符号になる。
//
// 位相の計算で必ず使うこと。連鎖どうしの位相差は負になりうるが、位相は
// [0, L) の値でなければ意味を持たない。Go の % をそのまま使うと負の位相が
// でき、連結先の候補がまるごと別物になる。
func FloorMod(a, b int64) int64 {
	m := a % b
	if m != 0 && ((m < 0) != (b < 0)) {
		m += b
	}
	return m
}

// Mod は x を y で割った剰余を、y と同じ符号で返す。
//
// 方位角を [0, 2pi) へ畳み込むのに使う。方位の内挿の重みはドウェル先頭で
// 負になるため、math.Mod では負の方位角が出てしまう。
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

// TruncToUint32 は x を 0 方向へ切り捨てて uint32 にする。
// 方位角をファイル上の 32 bit 表現へ落とすときの規約で、四捨五入ではない。
func TruncToUint32(x float64) uint32 { return uint32(math.Trunc(x)) }

// pwBlocksize は pairwise summation が再帰分割に切り替わる境界。
const pwBlocksize = 128

// Sum は a の総和を pairwise summation で求める。
//
// 8 要素ずつ別々のアキュムレータへ足し込んでから木状にまとめることで、
// 逐次加算より丸め誤差の蓄積が小さくなる。放物線フィットの残差二乗和は
// この総和の結果がそのまま棄却の閾値と比較されるため、加算順序が
// 出力に効く。
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

// MeanInt64 は整数列の平均を float64 で返す。
//
// 合計を整数のまま取り、最後に 1 度だけ割ることで丸めを 1 回に抑える。
// 呼び出し側が渡すのは残差（ns 単位、高々 1e5 程度）と段数だけなので、
// 合計が int64 を溢れることも float64 の厳密表現域 2^53 を出ることもない。
func MeanInt64(v []int64) float64 {
	var s int64
	for _, x := range v {
		s += x
	}
	return float64(s) / float64(len(v))
}

// Median は a の中央値を返す。要素数が偶数なら中央 2 つの平均。
// a は変更しない。
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

// LowerBound は昇順の a のうち v 以上が始まる位置を返す。
func LowerBound(a []float64, v float64) int {
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

// UpperBound は昇順の a のうち v より大きい値が始まる位置を返す。
// LowerBound との組で、v に等しい要素の範囲 [LowerBound, UpperBound) を表す。
func UpperBound(a []float64, v float64) int {
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
