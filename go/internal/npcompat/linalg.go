package npcompat

import "math"

// Solve3 は 3x3 の連立一次方程式 A x = b を、LAPACK dgesv（= numpy の
// np.linalg.solve が呼ぶもの）と同じ順序の演算で解く。
//
// LAPACK と揃えている点は 3 つある。いずれも結果の最下位ビットに効く。
//   - dgetf2: 部分ピボット選択は列内の |値| 最大、同値なら先に現れた行
//   - dgetf2: 除算ではなく逆数を 1 回作って掛ける（x/p ではなく x*(1/p)）
//   - dtrsv : 前進・後退代入は列方向。特に後退代入では A02*x2 を A01*x1 より先に引く
//
// 特異行列（ピボットが 0）の場合は ok=false を返す。Python 側は
// LinAlgError で異常終了するが、こちらは呼び出し側でフィット失敗として扱う。
func Solve3(a [3][3]float64, b [3]float64) (x [3]float64, ok bool) {
	const n = 3
	m := a
	x = b

	// --- dgetf2: 部分ピボット付き LU 分解 ---
	for k := 0; k < n; k++ {
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

	// --- dtrsv: L y = b（単位下三角・列方向）---
	for j := 0; j < n; j++ {
		if x[j] == 0 {
			continue
		}
		for i := j + 1; i < n; i++ {
			x[i] -= x[j] * m[i][j]
		}
	}
	// --- dtrsv: U x = y（上三角・列方向）---
	for j := n - 1; j >= 0; j-- {
		if x[j] == 0 {
			continue
		}
		x[j] /= m[j][j]
		for i := 0; i < j; i++ {
			x[i] -= x[j] * m[i][j]
		}
	}
	return x, true
}

// NormalEquations3 は放物線フィットの設計行列 X = [1, u, u^2] に対する
// A = X^T X と b = X^T y を組み立てる。numpy 側の x.T @ x / x.T @ y に対応する。
//
// mask が nil でない場合は mask[k] が true の行だけを使う（Python の x[inlier]）。
func NormalEquations3(u, y []float64, mask []bool) (a [3][3]float64, b [3]float64) {
	for k := range u {
		if mask != nil && !mask[k] {
			continue
		}
		uk := u[k]
		row := [3]float64{1, uk, uk * uk}
		for i := 0; i < 3; i++ {
			for j := 0; j < 3; j++ {
				a[i][j] += row[i] * row[j]
			}
			b[i] += row[i] * y[k]
		}
	}
	return a, b
}
