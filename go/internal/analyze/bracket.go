package analyze

import (
	"math"

	"pssrx/internal/numeric"
	"pssrx/internal/pattern"
)

// Dwell は SSR が測定局を向いた間に取得された一連のデータセット。
type Dwell struct {
	CenterTs     int64 // 送信時刻基準のビーム中心時刻
	PeakPowerDbm float64
	Chain        *Chain
}

// bracket はブラケット 1 本の内挿結果。Steps と DriftPPM は監視・ログ用。
type bracket struct {
	Times    []int64 // 送信時刻 [ns]（受信時刻フレーム。遅延補正は呼び出し側）
	Modes    []uint8 // 質問種別
	Steps    int64   // A の先頭から B の先頭までの質問数
	DriftPPM float64 // 1 質問あたりの時間誤差を PRI 比で表した値
}

// linkSteps は位相 p0 から dtNs 経過した先の質問が何段先かを決める。
//
// 候補は位相合同条件 d ≡ phaseShift (mod L) を満たすものだけなので、
// 間隔は L*PRI 刻みになる。1 走査 4 s の予測誤差は許容 100 ppm でも
// 400 us 程度なので、四捨五入は十分安全。
// 第 2 要素に次善候補との時間差（余裕）を返す。
func linkSteps(pat *pattern.Pattern, p0, dtNs, phaseShift int64) (int64, float64) {
	l := pat.Length()
	r := numeric.FloorMod(phaseShift, l)
	k := numeric.RoundToInt64((float64(dtNs)/pat.MeanPRI() - float64(r)) / float64(l))

	var best, bestErr, second int64
	hasBest, hasSecond := false, false
	for _, kk := range [3]int64{k - 1, k, k + 1} {
		d := r + kk*l
		if d <= 0 {
			continue
		}
		err := pat.Delta(p0, d) - dtNs
		if err < 0 {
			err = -err
		}
		switch {
		case !hasBest || err < bestErr:
			second, hasSecond = bestErr, hasBest
			best, bestErr, hasBest = d, err, true
		case !hasSecond || err < second:
			second, hasSecond = err, true
		}
	}
	if !hasBest {
		return 0, 0.0
	}
	if !hasSecond {
		return best, math.Inf(1)
	}
	return best, float64(second - bestErr)
}

// interpolateBracket はドウェル A・B に挟まれた区間の質問時刻を内挿する。
// 失敗時は nil。
//
// 時刻モデルは t(s) = t_a0 + a + P(s) + eps*s（s は A の先頭質問からの段数）。
// P はパターンから決まる理想時刻、eps は 1 質問あたりの時間誤差。
// a と eps は「両ドウェルの全質問データの残差平均」が一致するように決める。
// すなわち観測時刻とパターン理想時刻のずれを、間に挟まる質問数で等分する。
//
// 最小二乗ではなく等分なのは、誤差要因が実質的にレーダー側の基準発振器の
// ずれ（1 走査で数 us の一定ドリフト）だけで、段数に比例して蓄積するため。
// 両端をドウェルの平均で固定するので区間内部は外挿ではなく内挿になる。
func interpolateBracket(pat *pattern.Pattern, dwellA, dwellB *Dwell) *bracket {
	chA, chB := dwellA.Chain, dwellB.Chain
	l := pat.Length()
	t0 := chA.Times[0]
	p0 := chA.Phase0()

	// --- A の先頭から B の先頭までの段数 ---
	steps, linkMargin := linkSteps(pat, p0, chB.Times[0]-t0, chB.Phase0()-chA.Phase0())
	if steps <= 0 || linkMargin < pat.MeanPRI()*0.25 {
		return nil // 丸めの余裕がない = 連結が信用できない
	}

	sA := make([]int64, len(chA.NLocal))
	for i, v := range chA.NLocal {
		sA[i] = v - chA.Phase0()
	}
	sB := make([]int64, len(chB.NLocal))
	for i, v := range chB.NLocal {
		sB[i] = v - chB.Phase0() + steps
	}
	meanSA, meanSB := numeric.MeanInt64(sA), numeric.MeanInt64(sB)
	if meanSB <= meanSA {
		return nil
	}

	// --- 残差の平均を両ドウェルで取り、その差を段数で等分する ---
	rA := numeric.MeanInt64(residuals(pat, chA.Times, t0, p0, sA))
	rB := numeric.MeanInt64(residuals(pat, chB.Times, t0, p0, sB))
	eps := (rB - rA) / (meanSB - meanSA)
	a := rA - eps*meanSA

	pRel := pat.RelativeTimes(p0, int(steps))
	times := make([]int64, steps)
	modes := make([]uint8, steps)
	for i := range times {
		times[i] = t0 + int64(numeric.RoundHalfEven((a+float64(pRel[i]))+eps*float64(i)))
		modes[i] = pat.Modes()[numeric.FloorMod(p0+int64(i), l)]
	}
	return &bracket{
		Times:    times,
		Modes:    modes,
		Steps:    steps,
		DriftPPM: eps / pat.MeanPRI() * 1e6,
	}
}

// residuals は観測時刻とパターン理想時刻のずれを返す。
func residuals(pat *pattern.Pattern, times []int64, t0, p0 int64, s []int64) []int64 {
	out := make([]int64, len(times))
	for i := range times {
		out[i] = times[i] - t0 - pat.Delta(p0, s[i])
	}
	return out
}
