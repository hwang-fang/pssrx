package tracking_test

import (
	"math"
	"math/rand"
	"reflect"
	"testing"

	"pssrx/internal/geodesy"
	"pssrx/internal/pssr/tracking"
)

// smoothAll は Resolve の出力に相当する点列を Smooth に流す。
func smoothAll(t *testing.T, conv *geodesy.ENUConverter, cfg tracking.Config, fixes []tracking.Fix, chunk int) ([]tracking.Fix, tracking.Stats) {
	t.Helper()
	if err := tracking.Validate(testParams, cfg); err != nil {
		t.Fatal(err)
	}
	var st tracking.SmoothState
	stats := tracking.Stats{}
	if chunk <= 0 {
		chunk = len(fixes)
	}
	var out []tracking.Fix
	for i := 0; i < len(fixes); i += chunk {
		end := min(i+chunk, len(fixes))
		out = append(out, tracking.Smooth(&st, &stats, conv, testParams, cfg, fixes[i:end], end == len(fixes))...)
	}
	return out, stats
}

// noisyLine は東へ ve、北へ vn [m/s] で飛ぶ機体の点を n 走査ぶん作り、
// 位置に σ の正規雑音（乱数の種は固定）を足す。真の位置も返す。
func noisyLine(n int, flight int64, ve, vn, sigmaE, sigmaN float64, seed int64) ([]tracking.Fix, [][2]float64) {
	rng := rand.New(rand.NewSource(seed))
	var fixes []tracking.Fix
	var truth [][2]float64
	for k := range n {
		sec := float64(k) * float64(aroundNs) / 1e9
		e, n := ve*sec, vn*sec
		f := fixAt(float64(k), 0o1234, 10000, e+rng.NormFloat64()*sigmaE, n+rng.NormFloat64()*sigmaN)
		f.Position.ENU.U = rng.NormFloat64() * 8.8
		f.Position.Cov = [3][3]float64{{sigmaE * sigmaE, 0, 0}, {0, sigmaN * sigmaN, 0}, {0, 0, 8.8 * 8.8}}
		f.Track, f.Flight, f.Status = flight, flight, tracking.FixOK
		fixes = append(fixes, f)
		truth = append(truth, [2]float64{e, n})
	}
	return fixes, truth
}

// TestSmoothRecoversVelocityAndReducesError は等速直線の点で、速度が復元され、
// 平滑化した位置の誤差が観測より小さいことを確認する。
func TestSmoothRecoversVelocityAndReducesError(t *testing.T) {
	conv := testConverter(t)
	fixes, truth := noisyLine(60, 1, 150, -80, 300, 40, 1)
	out, s := smoothAll(t, conv, tracking.DefaultConfig(), fixes, 0)
	if len(out) != len(fixes) || s.SmoothedFixes != len(fixes) || s.SmoothedFlights != 1 {
		t.Fatalf("出力 %d, stats %+v", len(out), s)
	}
	var rawErr, smErr float64
	for k, f := range out {
		if f.Smoothed == nil {
			t.Fatalf("点 %d が平滑化されていない", k)
		}
		rawErr += math.Hypot(f.Position.ENU.E-truth[k][0], f.Position.ENU.N-truth[k][1])
		smErr += math.Hypot(f.Smoothed.ENU.E-truth[k][0], f.Smoothed.ENU.N-truth[k][1])
		if k >= 10 && k < 50 {
			if v := f.Smoothed.Velocity; math.Abs(v.E-150) > 15 || math.Abs(v.N+80) > 5 {
				t.Errorf("点 %d の速度 (%.1f, %.1f), 期待 (150, -80)", k, v.E, v.N)
			}
		}
	}
	if smErr > rawErr/2 {
		t.Errorf("平滑化の誤差 %.0f m が観測 %.0f m の半分を超える", smErr, rawErr)
	}
	// 雑音の設定が観測と整合していれば NIS の平均は自由度 3 に近い
	if mean := s.SmoothNISSum / float64(s.SmoothUpdates); mean < 2 || mean > 4.5 {
		t.Errorf("NIS 平均 %.2f", mean)
	}
}

// TestSmoothPassesUnconfirmedAndKeepsOrder は unconfirmed の点が素通しされ、
// 出力が時刻順であることを確認する。
func TestSmoothPassesUnconfirmedAndKeepsOrder(t *testing.T) {
	conv := testConverter(t)
	fixes, _ := noisyLine(12, 1, 100, 0, 50, 50, 2)
	stray := fixAt(5.5, 0o7777, 3000, 9000, 9000)
	stray.Track, stray.Flight, stray.Status = 9, 9, tracking.FixUnconfirmed
	all := append(append([]tracking.Fix{}, fixes[:6]...), stray)
	all = append(all, fixes[6:]...)
	out, s := smoothAll(t, conv, tracking.DefaultConfig(), all, 4)
	if len(out) != 13 {
		t.Fatalf("出力 %d", len(out))
	}
	for i, f := range out {
		if i > 0 && f.Timestamp < out[i-1].Timestamp {
			t.Fatal("時刻順でない")
		}
		if (f.Status == tracking.FixUnconfirmed) != (f.Smoothed == nil) {
			t.Errorf("点 %d: status %v, smoothed %v", i, f.Status, f.Smoothed != nil)
		}
	}
	if s.SmoothedFixes != 12 || s.SmoothedFlights != 1 {
		t.Errorf("stats %+v", s)
	}
}

// TestSmoothIsChunkInvariant は投入の刻みによらず同じ結果になることを確認する。
func TestSmoothIsChunkInvariant(t *testing.T) {
	conv := testConverter(t)
	a, _ := noisyLine(40, 1, 120, 60, 200, 30, 3)
	b, _ := noisyLine(25, 2, -90, 40, 200, 30, 4)
	for i := range b {
		b[i].Timestamp += 7 * aroundNs
	}
	var fixes []tracking.Fix
	fixes = append(fixes, a...)
	fixes = append(fixes, b...)
	// Resolve の出力どおり時刻順に
	for i := 1; i < len(fixes); i++ {
		for j := i; j > 0 && fixes[j].Timestamp < fixes[j-1].Timestamp; j-- {
			fixes[j], fixes[j-1] = fixes[j-1], fixes[j]
		}
	}
	whole, ws := smoothAll(t, conv, tracking.DefaultConfig(), fixes, 0)
	for _, chunk := range []int{1, 3, 11} {
		got, gs := smoothAll(t, conv, tracking.DefaultConfig(), fixes, chunk)
		if !reflect.DeepEqual(got, whole) {
			t.Errorf("chunk=%d: 出力が違う", chunk)
		}
		gs.SmoothHeldMax, ws.SmoothHeldMax = 0, 0
		if !reflect.DeepEqual(gs, ws) {
			t.Errorf("chunk=%d: stats %+v / %+v", chunk, gs, ws)
		}
	}
}

// TestSmoothBridgesGap は Link が繋いだ切れ目（30 s）の後も速度が保たれ、
// 再初期化されないことを確認する。
func TestSmoothBridgesGap(t *testing.T) {
	conv := testConverter(t)
	fixes, _ := noisyLine(30, 1, 200, 0, 30, 30, 5)
	var withGap []tracking.Fix
	for k, f := range fixes {
		if k >= 10 && k < 18 {
			continue
		}
		withGap = append(withGap, f)
	}
	out, _ := smoothAll(t, conv, tracking.DefaultConfig(), withGap, 0)
	after := out[10] // 切れ目の直後の点
	if after.Smoothed == nil || math.Abs(after.Smoothed.Velocity.E-200) > 20 {
		t.Errorf("切れ目の後の速度 %+v", after.Smoothed)
	}
	if math.Sqrt(after.Smoothed.Cov[0][0]) > 30 {
		t.Errorf("切れ目の後の σ_E %.1f m が観測より大きい", math.Sqrt(after.Smoothed.Cov[0][0]))
	}
}

// noisyTurn は速さ v [m/s]、旋回率 omega [rad/s]（反時計回り正）で回る機体の
// 点を n 走査ぶん作り、位置に正規雑音を足す。真の位置・速度も返す。
func noisyTurn(n int, v, omega, sigma float64, seed int64) ([]tracking.Fix, [][4]float64) {
	rng := rand.New(rand.NewSource(seed))
	r := v / omega
	var fixes []tracking.Fix
	var truth [][4]float64
	for k := range n {
		sec := float64(k) * float64(aroundNs) / 1e9
		// 原点から東へ進み始めて左に回る円
		e, nn := r*math.Sin(omega*sec), r*(1-math.Cos(omega*sec))
		ve, vn := v*math.Cos(omega*sec), v*math.Sin(omega*sec)
		f := fixAt(float64(k), 0o1234, 10000, e+rng.NormFloat64()*sigma, nn+rng.NormFloat64()*sigma)
		f.Position.ENU.U = rng.NormFloat64() * 8.8
		f.Position.Cov = [3][3]float64{{sigma * sigma, 0, 0}, {0, sigma * sigma, 0}, {0, 0, 8.8 * 8.8}}
		f.Track, f.Flight, f.Status = 1, 1, tracking.FixOK
		fixes = append(fixes, f)
		truth = append(truth, [4]float64{e, nn, ve, vn})
	}
	return fixes, truth
}

// TestSmoothCoordinatedTurn は一定旋回率の航跡で、CT が旋回率を復元し、
// 内側への偏り（角切り）が CV より小さいことを確認する。
func TestSmoothCoordinatedTurn(t *testing.T) {
	conv := testConverter(t)
	const omega = 2 * math.Pi / 180 // 2°/s
	fixes, truth := noisyTurn(60, 120, omega, 100, 7)
	cv := tracking.DefaultConfig()
	cv.SmoothTurnRateSigmaDps = 0
	ct := tracking.DefaultConfig()
	ct.SmoothTurnRateSigmaDps = 0.5
	bias := func(cfg tracking.Config) (inward, course, turn float64) {
		out, _ := smoothAll(t, conv, cfg, fixes, 0)
		n := 0
		for k := 10; k < 50; k++ {
			s := out[k].Smoothed
			ve, vn := truth[k][2], truth[k][3]
			// 左旋回の内側は進行方向の左 = (-vn, ve)/v
			ie, in := -vn/120, ve/120
			inward += (s.ENU.E-truth[k][0])*ie + (s.ENU.N-truth[k][1])*in
			dc := math.Atan2(s.Velocity.E, s.Velocity.N) - math.Atan2(ve, vn)
			course += math.Abs(math.Mod(dc+3*math.Pi, 2*math.Pi)-math.Pi) * 180 / math.Pi
			turn += s.TurnRate
			n++
		}
		return inward / float64(n), course / float64(n), turn / float64(n)
	}
	cvIn, cvCourse, _ := bias(cv)
	ctIn, ctCourse, ctTurn := bias(ct)
	t.Logf("CV: 内側 %.1f m, 針路 %.2f°; CT: 内側 %.1f m, 針路 %.2f°, 旋回率 %.2f°/s", cvIn, cvCourse, ctIn, ctCourse, ctTurn*180/math.Pi)
	if math.Abs(ctTurn-omega) > 0.3*math.Pi/180 {
		t.Errorf("旋回率 %.2f°/s, 期待 2", ctTurn*180/math.Pi)
	}
	// CV は角を切って内側に偏る。CT では偏りが観測の雑音（100 m）の 1/5 以下
	if math.Abs(ctIn) > 20 || ctCourse > 2 || math.Abs(cvIn) < 40 {
		t.Errorf("CT の内側 %.1f m / 針路 %.2f°、CV は %.1f m / %.2f°", ctIn, ctCourse, cvIn, cvCourse)
	}
}
