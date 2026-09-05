// Package numeric は解析で使う小さな数値ユーティリティを集める。
package numeric

import (
	"math"
	"slices"
)

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
