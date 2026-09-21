package pssr

import (
	"math"
	"slices"

	"pssrx/internal/geodesy"
)

// SmoothState はフライトごとの位置を平滑化する段が持ち越す記録。
// ゼロ値から使える。
//
// 位置（Locate の観測値）は走査ごとに独立で、方位方向の誤差が大きい
// （応答 3 件で σ_θ ≈ 1.8°、100 km で 3 km）。フライトの点は 1 機体の
// 運動なので、等速直線 + 白色加速度（CV）のカルマンフィルタで繋ぎ、
// 後ろ SmoothLagScans 走査の点まで見た固定遅延平滑化（RTS）で出す。
//
// 状態は SSR の ENU での位置と速度の 6 次元。観測は Fix の ENU と
// その共分散（σ_τ・σ_θ(n)・σ_alt を伝播したもの）で、線形。旋回中は
// 等速のモデルから外れて残差が偏るが、まず CV で入れて残差の統計
// （Stats.SmoothNIS*）を見る。
//
// 判定は変えない。unconfirmed の点はフライトに 1〜2 点しか無いので
// 素通しし、それ以外（ok / echo / ambiguous）はフライト内で平滑化する。
// 像も運動は滑らかなので、平滑化して害はない。
//
// 点は時刻順に出す。点の時刻 + 遅延幅を透かしが越えたら、それまでに
// 届いた同じフライトの点（時刻が点 + 遅延幅以内のもの）で平滑化して
// 出すので、投入の刻みによらない。
type SmoothState struct {
	held      []Fix // 時刻順の出力待ち
	flights   map[int64]*kalman
	watermark int64
}

// Kinematics は平滑化した位置と速度。
type Kinematics struct {
	Lat, Lon, Alt float64     // WGS84 [deg], 標高 [m]
	ENU           geodesy.ENU // SSR を原点にした ENU [m]
	Velocity      geodesy.ENU // ENU の速度 [m/s]
	Cov           [6][6]float64
}

// kalman はフライト 1 本のフィルタ。
type kalman struct {
	last  int64
	steps []kfStep // 時刻順。平滑化の後退計算に使う
}

// kfStep は観測 1 点ぶんのフィルタの記録。
type kfStep struct {
	t        int64
	xPred, x vec6 // 予測 x_k|k-1 と更新後 x_k|k
	pPred, p mat6
	f        mat6 // 前の点からこの点への遷移
	emitted  bool
}

type (
	vec6 [6]float64
	mat6 [6][6]float64
)

// Smooth はフライト ID の付いた点を受け取り、平滑化した位置と速度を付けて
// 時刻順に返す。last が真なら保留を全部出す。
func Smooth(st *SmoothState, stats *Stats, geom Geometry, params Params, cfg Config, fixes []Fix, last bool) []Fix {
	if st.flights == nil {
		st.flights = map[int64]*kalman{}
	}
	lag := int64(cfg.SmoothLagScans)*params.AroundTimeNs + params.AroundTimeNs/4

	for _, f := range fixes {
		st.watermark = max(st.watermark, f.Timestamp)
		if f.Status != FixUnconfirmed && f.Flight != 0 {
			kf := st.flights[f.Flight]
			if kf == nil {
				kf = &kalman{}
				st.flights[f.Flight] = kf
				stats.SmoothedFlights++
			}
			kf.update(stats, cfg, f)
		}
		i, _ := slices.BinarySearchFunc(st.held, f.Timestamp, func(h Fix, t int64) int {
			return compareInt64(h.Timestamp, t)
		})
		for i < len(st.held) && st.held[i].Timestamp == f.Timestamp {
			i++
		}
		st.held = slices.Insert(st.held, i, f)
	}
	stats.SmoothHeldMax = max(stats.SmoothHeldMax, len(st.held))

	// 出す。遅延幅ぶん待った点から
	n := 0
	for n < len(st.held) && (last || st.held[n].Timestamp+lag <= st.watermark) {
		n++
	}
	out := make([]Fix, n)
	for k := range n {
		f := st.held[k]
		if kf := st.flights[f.Flight]; kf != nil && f.Status != FixUnconfirmed {
			if km, ok := kf.smoothed(geom, f.Timestamp, f.Timestamp+lag); ok {
				f.Smoothed = km
				stats.SmoothedFixes++
			}
		}
		out[k] = f
	}
	st.held = slices.Delete(st.held, 0, n)

	// 出した点より前の記録と、続きの来ないフライトを忘れる
	for id, kf := range st.flights {
		if st.watermark-kf.last > cfg.LinkMaxGapNs+lag {
			delete(st.flights, id)
			continue
		}
		kf.prune()
	}
	if last {
		st.flights, st.held = map[int64]*kalman{}, nil
	}
	return out
}

// update は点 f でフィルタを 1 歩進める。最初の点は位置をそのまま、速度は
// 0 に大きな分散で置く。
func (kf *kalman) update(stats *Stats, cfg Config, f Fix) {
	z := vec6{f.Position.ENU.E, f.Position.ENU.N, f.Position.ENU.U}
	var r mat6
	for i := range 3 {
		for j := range 3 {
			r[i][j] = f.Position.Cov[i][j]
		}
	}
	kf.last = max(kf.last, f.Timestamp)
	if len(kf.steps) == 0 {
		var p mat6
		for i := range 3 {
			for j := range 3 {
				p[i][j] = r[i][j]
			}
			p[3+i][3+i] = cfg.SmoothInitialVelocitySigmaMps * cfg.SmoothInitialVelocitySigmaMps
		}
		kf.steps = append(kf.steps, kfStep{t: f.Timestamp, xPred: z, x: z, pPred: p, p: p, f: identity6()})
		return
	}
	prev := kf.steps[len(kf.steps)-1]
	dt := float64(f.Timestamp-prev.t) / 1e9
	fm := transition(dt)
	q := processNoise(dt, cfg.SmoothAccelSigmaMps2, cfg.SmoothVerticalAccelSigmaMps2)
	xPred := fm.mulVec(prev.x)
	pPred := fm.mul(prev.p).mul(fm.transpose()).add(q)

	// 更新。H = [I 0] なので位置のブロックだけ
	var y [3]float64
	var s [3][3]float64
	for i := range 3 {
		y[i] = z[i] - xPred[i]
		for j := range 3 {
			s[i][j] = pPred[i][j] + r[i][j]
		}
	}
	sInv, ok := inverse3(s)
	if !ok {
		kf.steps = append(kf.steps, kfStep{t: f.Timestamp, xPred: xPred, x: xPred, pPred: pPred, p: pPred, f: fm})
		return
	}
	nis := 0.0
	for i := range 3 {
		for j := range 3 {
			nis += y[i] * sInv[i][j] * y[j]
		}
	}
	stats.SmoothUpdates++
	stats.SmoothNISSum += nis
	if nis > smoothNIS99 {
		stats.SmoothNISOver99++
	}
	// K = P Hᵀ S⁻¹ (6×3)
	var k [6][3]float64
	for i := range 6 {
		for j := range 3 {
			for m := range 3 {
				k[i][j] += pPred[i][m] * sInv[m][j]
			}
		}
	}
	x := xPred
	for i := range 6 {
		for j := range 3 {
			x[i] += k[i][j] * y[j]
		}
	}
	// P = (I − K H) P_pred (I − K H)ᵀ + K R Kᵀ（Joseph 形）
	ikh := identity6()
	for i := range 6 {
		for j := range 3 {
			ikh[i][j] -= k[i][j]
		}
	}
	p := ikh.mul(pPred).mul(ikh.transpose())
	for i := range 6 {
		for j := range 6 {
			for a := range 3 {
				for b := range 3 {
					p[i][j] += k[i][a] * r[a][b] * k[j][b]
				}
			}
		}
	}
	kf.steps = append(kf.steps, kfStep{t: f.Timestamp, xPred: xPred, x: x, pPred: pPred, p: p, f: fm})
}

// smoothedNIS99 は χ²(3) の 99% 点。残差がこれを超える点を数える。
const smoothNIS99 = 11.345

// smoothed は時刻 t の点を、時刻 until までの点で平滑化した結果を返す。
// RTS の後退計算を until の点から t の点まで行う。
func (kf *kalman) smoothed(geom Geometry, t, until int64) (*Kinematics, bool) {
	i := slices.IndexFunc(kf.steps, func(s kfStep) bool { return s.t == t && !s.emitted })
	if i < 0 {
		return nil, false
	}
	j := i
	for j+1 < len(kf.steps) && kf.steps[j+1].t <= until {
		j++
	}
	x, p := kf.steps[j].x, kf.steps[j].p
	for m := j - 1; m >= i; m-- {
		cur, next := kf.steps[m], kf.steps[m+1]
		inv, ok := next.pPred.inverse()
		if !ok {
			x, p = cur.x, cur.p
			continue
		}
		c := cur.p.mul(next.f.transpose()).mul(inv)
		x = cur.x.add(c.mulVec(x.sub(next.xPred)))
		p = cur.p.add(c.mul(p.sub(next.pPred)).mul(c.transpose()))
	}
	kf.steps[i].emitted = true
	enu := geodesy.ENU{E: x[0], N: x[1], U: x[2]}
	lla, err := geom.conv.ENUToLLA(enu)
	if err != nil {
		return nil, false
	}
	return &Kinematics{
		Lat: lla.Lat, Lon: lla.Lon, Alt: lla.Alt, ENU: enu,
		Velocity: geodesy.ENU{E: x[3], N: x[4], U: x[5]},
		Cov:      p,
	}, true
}

// prune は出した点より前の記録を捨てる。後退計算は未来から過去へ進む
// ので、出した点より前の記録は要らない。最後の記録は前向きの状態として残す。
func (kf *kalman) prune() {
	n := 0
	for n < len(kf.steps)-1 && kf.steps[n].emitted {
		n++
	}
	if n > 0 {
		kf.steps = slices.Delete(kf.steps, 0, n)
	}
}

// transition は Δt [s] の等速の遷移行列。
func transition(dt float64) mat6 {
	f := identity6()
	for i := range 3 {
		f[i][3+i] = dt
	}
	return f
}

// processNoise は白色加速度（水平 σa、鉛直 σv [m/s²]）の Δt [s] の過程雑音。
func processNoise(dt, sigmaA, sigmaV float64) mat6 {
	var q mat6
	t2, t3 := dt*dt, dt*dt*dt
	for i := range 3 {
		s2 := sigmaA * sigmaA
		if i == 2 {
			s2 = sigmaV * sigmaV
		}
		q[i][i] = s2 * t3 / 3
		q[i][3+i] = s2 * t2 / 2
		q[3+i][i] = s2 * t2 / 2
		q[3+i][3+i] = s2 * dt
	}
	return q
}

func identity6() mat6 {
	var m mat6
	for i := range 6 {
		m[i][i] = 1
	}
	return m
}

func (a mat6) mul(b mat6) mat6 {
	var c mat6
	for i := range 6 {
		for k := range 6 {
			if a[i][k] == 0 {
				continue
			}
			for j := range 6 {
				c[i][j] += a[i][k] * b[k][j]
			}
		}
	}
	return c
}

func (a mat6) mulVec(v vec6) vec6 {
	var out vec6
	for i := range 6 {
		for j := range 6 {
			out[i] += a[i][j] * v[j]
		}
	}
	return out
}

func (a mat6) add(b mat6) mat6 {
	for i := range 6 {
		for j := range 6 {
			a[i][j] += b[i][j]
		}
	}
	return a
}

func (a mat6) sub(b mat6) mat6 {
	for i := range 6 {
		for j := range 6 {
			a[i][j] -= b[i][j]
		}
	}
	return a
}

func (a mat6) transpose() mat6 {
	var t mat6
	for i := range 6 {
		for j := range 6 {
			t[j][i] = a[i][j]
		}
	}
	return t
}

// inverse は Gauss-Jordan（部分ピボット）による逆行列。特異なら ok が偽。
func (a mat6) inverse() (mat6, bool) {
	inv := identity6()
	for col := range 6 {
		piv := col
		for r := col + 1; r < 6; r++ {
			if math.Abs(a[r][col]) > math.Abs(a[piv][col]) {
				piv = r
			}
		}
		if a[piv][col] == 0 {
			return mat6{}, false
		}
		a[col], a[piv] = a[piv], a[col]
		inv[col], inv[piv] = inv[piv], inv[col]
		d := a[col][col]
		for j := range 6 {
			a[col][j] /= d
			inv[col][j] /= d
		}
		for r := range 6 {
			if r == col || a[r][col] == 0 {
				continue
			}
			m := a[r][col]
			for j := range 6 {
				a[r][j] -= m * a[col][j]
				inv[r][j] -= m * inv[col][j]
			}
		}
	}
	return inv, true
}

func (v vec6) add(w vec6) vec6 {
	for i := range 6 {
		v[i] += w[i]
	}
	return v
}

func (v vec6) sub(w vec6) vec6 {
	for i := range 6 {
		v[i] -= w[i]
	}
	return v
}

// inverse3 は 3×3 の逆行列（余因子）。
func inverse3(m [3][3]float64) ([3][3]float64, bool) {
	det := m[0][0]*(m[1][1]*m[2][2]-m[1][2]*m[2][1]) -
		m[0][1]*(m[1][0]*m[2][2]-m[1][2]*m[2][0]) +
		m[0][2]*(m[1][0]*m[2][1]-m[1][1]*m[2][0])
	if det == 0 || math.IsNaN(det) || math.IsInf(det, 0) {
		return [3][3]float64{}, false
	}
	var inv [3][3]float64
	inv[0][0] = (m[1][1]*m[2][2] - m[1][2]*m[2][1]) / det
	inv[0][1] = (m[0][2]*m[2][1] - m[0][1]*m[2][2]) / det
	inv[0][2] = (m[0][1]*m[1][2] - m[0][2]*m[1][1]) / det
	inv[1][0] = (m[1][2]*m[2][0] - m[1][0]*m[2][2]) / det
	inv[1][1] = (m[0][0]*m[2][2] - m[0][2]*m[2][0]) / det
	inv[1][2] = (m[0][2]*m[1][0] - m[0][0]*m[1][2]) / det
	inv[2][0] = (m[1][0]*m[2][1] - m[1][1]*m[2][0]) / det
	inv[2][1] = (m[0][1]*m[2][0] - m[0][0]*m[2][1]) / det
	inv[2][2] = (m[0][0]*m[1][1] - m[0][1]*m[1][0]) / det
	return inv, true
}
