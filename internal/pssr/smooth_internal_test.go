package pssr

import (
	"math"
	"testing"
)

// TestPredictJacobian は CT の予測のヤコビアンが数値微分と一致し、旋回率 0 の
// 極限で等速の予測に一致することを確認する。
func TestPredictJacobian(t *testing.T) {
	ct := &kalman{n: 7, ctMaxDt: 13}
	cv := &kalman{n: 6}
	for _, w := range []float64{0, 1e-9, 1e-5, 0.01, 0.05, -0.03} {
		x := vec{1000, -2000, 3000, 120, -60, 3, w}
		const dt = 4.04
		pred, f := ct.predict(x, dt, true)
		for j := range 7 {
			h := 1e-4 * math.Max(1, math.Abs(x[j]))
			if j == iOmega {
				h = 1e-7
			}
			xp, xm := x, x
			xp[j] += h
			xm[j] -= h
			pp, _ := ct.predict(xp, dt, true)
			pm, _ := ct.predict(xm, dt, true)
			for i := range 7 {
				num := (pp[i] - pm[i]) / (2 * h)
				if math.Abs(num-f[i][j]) > 1e-3*math.Max(1, math.Abs(num)) {
					t.Errorf("ω=%g: F[%d][%d] = %g, 数値微分 %g", w, i, j, f[i][j], num)
				}
			}
		}
		if w == 0 || w == 1e-9 {
			cvPred, _ := cv.predict(x, dt, false)
			for i := range 6 {
				if math.Abs(pred[i]-cvPred[i]) > 1e-6 {
					t.Errorf("ω=%g: CT の予測 %v が CV %v と違う", w, pred, cvPred)
				}
			}
		}
	}
	// turn が偽なら旋回率によらず直線。ω の微分は ω = 0 のもの（位置には T²/2 で効く）
	x := vec{0, 0, 0, 100, 0, 0, 0.05}
	straight, f := ct.predict(x, 4, false)
	if straight[iE] != 400 || straight[iN] != 0 || straight[iVE] != 100 || f[iN][iOmega] != 100*8 {
		t.Errorf("直線でない: %v, F[N][ω] = %g", straight, f[iN][iOmega])
	}
}
