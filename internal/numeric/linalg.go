package numeric

import "math"

// Solve3 は 3x3 の連立一次方程式 A x = b を部分ピボット付き LU 分解で解く。
// 特異な場合は ok=false を返す。
//
// 演算の順序は放物線フィットの結果を左右するため固定してある。
//
//   - ピボットは列内の絶対値最大。同値なら先に現れた行を採る
//   - 消去の係数は除算ではなく逆数を 1 度作って掛ける（x/p ではなく x*(1/p)）
//   - 前進・後退代入は列方向。後退代入では A02*x2 を A01*x1 より先に引く
//
// この順序は既存の intg 資産と一致させるために選んだもので、数値的に
// 優れているから選んだわけではない。詳細と実測した許容差は NUMERICS.md を参照。
func Solve3(a [3][3]float64, b [3]float64) (x [3]float64, ok bool) {
	const n = 3
	m := a
	x = b

	// --- 部分ピボット付き LU 分解 ---
	for k := range n {
		p := k
		best := math.Abs(m[k][k])
		for i := k + 1; i < n; i++ {
			if v := math.Abs(m[i][k]); v > best {
				best, p = v, i
			}
		}
		if best == 0 {
			return x, false
		}
		if p != k {
			m[k], m[p] = m[p], m[k]
			x[k], x[p] = x[p], x[k]
		}
		r := 1.0 / m[k][k]
		for i := k + 1; i < n; i++ {
			m[i][k] *= r
		}
		for j := k + 1; j < n; j++ {
			for i := k + 1; i < n; i++ {
				m[i][j] -= m[i][k] * m[k][j]
			}
		}
	}

	// --- 前進代入 L y = b（単位下三角・列方向）---
	for j := range n {
		if x[j] == 0 {
			continue
		}
		for i := j + 1; i < n; i++ {
			x[i] -= x[j] * m[i][j]
		}
	}
	// --- 後退代入 U x = y（上三角・列方向）---
	for j := n - 1; j >= 0; j-- {
		if x[j] == 0 {
			continue
		}
		x[j] /= m[j][j]
		for i := range j {
			x[i] -= x[j] * m[i][j]
		}
	}
	return x, true
}

// NormalEquations3 は設計行列 X = [1, u, u^2] に対する正規方程式
// A = X^T X と b = X^T y を組み立てる。放物線 y = a0 + a1*u + a2*u^2 の
// 最小二乗解は Solve3(A, b) で得られる。
//
// mask が非 nil なら mask[k] が true の行だけを使う。ロバスト化の
// 反復で外れ値を落とすのに使う。
func NormalEquations3(u, y []float64, mask []bool) (a [3][3]float64, b [3]float64) {
	for k := range u {
		if mask != nil && !mask[k] {
			continue
		}
		uk := u[k]
		row := [3]float64{1, uk, uk * uk}
		for i := range 3 {
			for j := range 3 {
				a[i][j] += row[i] * row[j]
			}
			b[i] += row[i] * y[k]
		}
	}
	return a, b
}
