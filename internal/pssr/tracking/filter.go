package tracking

import "math"

// trackFilter は航跡片の水平の等速カルマンフィルタ。状態は SSR の ENU での
// 位置と速度 (E, N, vE, vN)、観測は Fix の水平位置とその共分散（方位方向に
// 伸びた楕円）。門の判定にだけ使い、推定した位置は出力しない（平滑化は
// 後続の仕事）。過去の点だけで決まるので実時間で行える。
//
// 最初の点では速度が分からないので、速度を 0 に標準偏差
// TrackMaxSpeedMps / TrackGateSigmas で置く。2 点目の門はおよそ
// 「最大速度 × Δt の円」を観測の楕円で膨らませたものになり、3 点目からは
// 推定した速度の予測を中心に、予測と観測の共分散で決まる楕円になる。
// 過程雑音は白色加速度 TrackAccelSigmaMps2（旋回・離陸の加速を吸収する）。
type trackFilter struct {
	t int64      // 最後に更新した時刻 [ns]
	x [4]float64 // E, N, vE, vN
	p [4][4]float64
}

func newTrackFilter(cfg Config, f Fix) trackFilter {
	c := f.Position.Cov
	sv := cfg.TrackMaxSpeedMps / cfg.TrackGateSigmas
	var p [4][4]float64
	p[0][0], p[0][1], p[1][0], p[1][1] = c[0][0], c[0][1], c[1][0], c[1][1]
	p[2][2], p[3][3] = sv*sv, sv*sv
	return trackFilter{t: f.Timestamp, x: [4]float64{f.Position.ENU.E, f.Position.ENU.N, 0, 0}, p: p}
}

// predict は時刻 t への予測の状態と共分散を返す。
func (k *trackFilter) predict(cfg Config, t int64) ([4]float64, [4][4]float64) {
	dt := float64(t-k.t) / 1e9
	x := k.x
	x[0] += dt * x[2]
	x[1] += dt * x[3]
	// P = F P Fᵀ + Q。F は位置に dt × 速度を足す
	var fp [4][4]float64
	for j := range 4 {
		fp[0][j] = k.p[0][j] + dt*k.p[2][j]
		fp[1][j] = k.p[1][j] + dt*k.p[3][j]
		fp[2][j] = k.p[2][j]
		fp[3][j] = k.p[3][j]
	}
	var p [4][4]float64
	for i := range 4 {
		p[i][0] = fp[i][0] + dt*fp[i][2]
		p[i][1] = fp[i][1] + dt*fp[i][3]
		p[i][2] = fp[i][2]
		p[i][3] = fp[i][3]
	}
	q := cfg.TrackAccelSigmaMps2 * cfg.TrackAccelSigmaMps2
	d2, d3 := dt*dt/2, dt*dt*dt/3
	for i := range 2 {
		p[i][i] += q * d3
		p[i][i+2] += q * d2
		p[i+2][i] += q * d2
		p[i+2][i+2] += q * dt
	}
	return x, p
}

// innovation は点 f の予測からの残差と、その共分散 S = P_予測（位置） + R。
func (k *trackFilter) innovation(cfg Config, f Fix) (x [4]float64, p [4][4]float64, y [2]float64, s [2][2]float64) {
	x, p = k.predict(cfg, f.Timestamp)
	c := f.Position.Cov
	y = [2]float64{f.Position.ENU.E - x[0], f.Position.ENU.N - x[1]}
	s = [2][2]float64{{p[0][0] + c[0][0], p[0][1] + c[0][1]}, {p[1][0] + c[1][0], p[1][1] + c[1][1]}}
	return x, p, y, s
}

// distance は点 f の予測からのマハラノビス距離 √(yᵀ S⁻¹ y)。
func (k *trackFilter) distance(cfg Config, f Fix) (float64, bool) {
	_, _, y, s := k.innovation(cfg, f)
	inv, ok := inverse2(s)
	if !ok {
		return 0, false
	}
	d2 := y[0]*(inv[0][0]*y[0]+inv[0][1]*y[1]) + y[1]*(inv[1][0]*y[0]+inv[1][1]*y[1])
	return math.Sqrt(max(d2, 0)), true
}

// update は点 f で状態を更新する（Joseph 形）。
func (k *trackFilter) update(cfg Config, f Fix) {
	x, p, y, s := k.innovation(cfg, f)
	inv, ok := inverse2(s)
	if !ok {
		k.t, k.x, k.p = f.Timestamp, x, p
		return
	}
	// K = P Hᵀ S⁻¹（4×2）。H は位置の 2 成分を取り出す
	var g [4][2]float64
	for i := range 4 {
		for j := range 2 {
			g[i][j] = p[i][0]*inv[0][j] + p[i][1]*inv[1][j]
		}
	}
	for i := range 4 {
		x[i] += g[i][0]*y[0] + g[i][1]*y[1]
	}
	// P = (I − K H) P (I − K H)ᵀ + K R Kᵀ
	var a [4][4]float64 // I − K H
	for i := range 4 {
		a[i][i] = 1
		a[i][0] -= g[i][0]
		a[i][1] -= g[i][1]
	}
	var ap, np [4][4]float64
	for i := range 4 {
		for j := range 4 {
			for m := range 4 {
				ap[i][j] += a[i][m] * p[m][j]
			}
		}
	}
	c := f.Position.Cov
	for i := range 4 {
		for j := range 4 {
			for m := range 4 {
				np[i][j] += ap[i][m] * a[j][m]
			}
			for u := range 2 {
				for v := range 2 {
					np[i][j] += g[i][u] * c[u][v] * g[j][v]
				}
			}
		}
	}
	k.t, k.x, k.p = f.Timestamp, x, np
}

// inverse2 は 2×2 の逆行列。特異なら ok が偽。
func inverse2(m [2][2]float64) ([2][2]float64, bool) {
	det := m[0][0]*m[1][1] - m[0][1]*m[1][0]
	if det <= 0 || math.IsNaN(det) || math.IsInf(det, 0) {
		return [2][2]float64{}, false
	}
	return [2][2]float64{{m[1][1] / det, -m[0][1] / det}, {-m[1][0] / det, m[0][0] / det}}, true
}
