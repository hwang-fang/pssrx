package analyze

import (
	"math"

	"pssrx/internal/numeric"
)

// mad は中央絶対偏差による頑健なスケール推定
// （正規分布で sigma に一致するよう規格化）。
func mad(x []float64) float64 {
	if len(x) == 0 {
		return 0.0
	}
	med := numeric.Median(x)
	dev := make([]float64, len(x))
	for i, v := range x {
		dev[i] = math.Abs(v - med)
	}
	return 1.4826 * numeric.Median(dev)
}

// FitParabola は振幅列に放物線を当て、成功した場合は頂点時刻 [ns] と
// 振幅 [dBm] を返す。失敗時は ok=false。
//
// 外れ値自身が RMS を押し上げて閾値を緩めてしまう（マスキング）ため、
// 外れ値判定のスケールには RMS ではなく MAD を使う。
func FitParabola(times []int64, dbm []float64, cfg *Config) (center int64, peak float64, ok bool) {
	n := len(times)
	if n < cfg.ParabolaMinSamples {
		return 0, 0, false
	}

	// 数値条件のため ms 単位・中心化した座標で解く
	t0 := times[n/2]
	u := make([]float64, n)
	for i := range times {
		u[i] = float64(times[i]-t0) / 1e6
	}
	y := dbm

	inlier := make([]bool, n)
	for i := range inlier {
		inlier[i] = true
	}
	var beta [3]float64
	var rms float64
	resid := make([]float64, n)

	iters := max(cfg.RobustIters, 1)
	for it := 0; it < iters; it++ {
		if countTrue(inlier) < 4 {
			break
		}
		// 最小二乗法によるフィッティング
		a, b := numeric.NormalEquations3(u, y, inlier)
		solved, okSolve := numeric.Solve3(a, b)
		if !okSolve {
			// 観測時刻が 1 点に潰れている場合など。ビームパターンとして
			// 解釈できないのでフィット失敗として扱う。
			return 0, 0, false
		}
		beta = solved

		// 残差計算
		for i := range u {
			resid[i] = y[i] - (beta[0] + u[i]*beta[1] + u[i]*u[i]*beta[2])
		}
		nin := countTrue(inlier)
		sq := make([]float64, 0, nin)
		for i, in := range inlier {
			if in {
				sq = append(sq, resid[i]*resid[i])
			}
		}
		rms = math.Sqrt(numeric.Sum(sq) / float64(max(nin-3, 1)))

		if it == cfg.RobustIters-1 {
			// 最後の計算は早期 break
			break
		}

		inResid := make([]float64, 0, nin)
		for i, in := range inlier {
			if in {
				inResid = append(inResid, resid[i])
			}
		}
		thr := cfg.ParabolaOutlierK * math.Max(mad(inResid), cfg.ParabolaSigmaFloorDb)
		next := make([]bool, n)
		for i := range resid {
			next[i] = math.Abs(resid[i]) <= thr
		}
		// 半分に削ってしまったら終わり。データが変わらない（収束）場合も終わり。
		if countTrue(next) < max(cfg.ParabolaMinSamples, n/2) || equalMask(next, inlier) {
			break
		}
		inlier = next
	}

	a0, a1, a2 := beta[0], beta[1], beta[2]
	if a2 >= 0 { // 上に凸でない = ビームパターンではない
		return 0, 0, false
	}

	// 2 次関数のピーク計算
	uc := -a1 / (2.0 * a2)
	center = t0 + numeric.RoundToInt64(uc*1e6)
	peak = a0 + a1*uc/2.0

	span := float64(times[n-1] - times[0])
	margin := cfg.VertexMarginFrac * span

	if rms > cfg.ParabolaMaxResidualDb {
		return 0, 0, false
	}
	// 頂点がドウェルの範囲から大きく外れていないか。比較は float64 で行う。
	cf := float64(center)
	if !(float64(times[0])-margin <= cf && cf <= float64(times[n-1])+margin) {
		return 0, 0, false
	}
	minY := math.Inf(1)
	for i, in := range inlier {
		if in && y[i] < minY {
			minY = y[i]
		}
	}
	if peak-minY < cfg.MinPeakDropDb {
		return 0, 0, false
	}
	return center, peak, true
}

func countTrue(b []bool) int {
	n := 0
	for _, v := range b {
		if v {
			n++
		}
	}
	return n
}

func equalMask(a, b []bool) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
