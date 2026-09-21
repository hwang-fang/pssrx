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
// 状態は SSR の ENU での位置と速度（と、協調旋回モデル CT では旋回率）。
// 観測は Fix の ENU とその共分散（σ_τ・σ_θ(n)・σ_alt を伝播したもの）で、
// 線形。CT は予測が非線形なので EKF（前の点の更新後の推定で線形化した
// ヤコビアンを平滑化にも使う）。CV は等速モデルで、旋回中は角を切って内側に偏る
// （真値との比較で 25〜50 m）。SmoothTurnRateSigmaDps が 0 なら CV。
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
	TurnRate      float64     // 旋回率 [rad/s]。ENU で反時計回り（左旋回）が正
	HasTurnRate   bool        // CT で推定した旋回率か（CV では偽）
	Cov           [stateDim][stateDim]float64
}

// 状態の添字。位置 3、速度 3、旋回率 1。CV は先頭 6 次元だけ使う。
const (
	iE, iN, iU, iVE, iVN, iVU, iOmega = 0, 1, 2, 3, 4, 5, 6
	stateDim                          = 7
)

// kalman はフライト 1 本のフィルタ。
type kalman struct {
	n       int     // 状態の次元。CV 6、CT 7
	ctMaxDt float64 // CT で旋回を外挿する Δt の上限 [s]。超える切れ目は直線で繋ぐ
	last    int64
	steps   []kfStep // 時刻順。平滑化の後退計算に使う
}

// kfStep は観測 1 点ぶんのフィルタの記録。
type kfStep struct {
	t        int64
	xPred, x vec // 予測 x_k|k-1 と更新後 x_k|k
	pPred, p mat
	f        mat // 前の点からこの点への遷移（CT では前の点の更新後の推定で線形化したヤコビアン）
	emitted  bool
}

type (
	vec [stateDim]float64
	mat [stateDim][stateDim]float64
)

// initialTurnRateSigma は CT の最初の点で旋回率 0 に置く標準偏差 [rad/s]
// （3°/s、標準率旋回）。
const initialTurnRateSigma = 3 * math.Pi / 180

// Smooth はフライト ID の付いた点を受け取り、平滑化した位置と速度を付けて
// 時刻順に返す。last が真なら保留を全部出す。
func Smooth(st *SmoothState, stats *Stats, geom Geometry, params Params, cfg Config, fixes []Fix, last bool) []Fix {
	if st.flights == nil {
		st.flights = map[int64]*kalman{}
	}
	lag := int64(cfg.SmoothLagScans)*params.AroundTimeNs + params.AroundTimeNs/4
	n := 6
	if cfg.SmoothTurnRateSigmaDps > 0 {
		n = 7
	}
	// Track が繋ぐ幅（欠測 MaxMissedScans まで）の中だけ旋回を外挿する。
	// それより長い切れ目は Link が直線の外挿で繋いだもので、不確かな旋回率で
	// 長く回すと EKF の線形化が壊れる（真値との比較で 10 km の誤り）
	ctMaxDt := float64(int64(cfg.TrackMaxMissedScans+1)*params.AroundTimeNs+params.AroundTimeNs/4) / 1e9

	for _, f := range fixes {
		st.watermark = max(st.watermark, f.Timestamp)
		if f.Status != FixUnconfirmed && f.Flight != 0 {
			kf := st.flights[f.Flight]
			if kf == nil {
				kf = &kalman{n: n, ctMaxDt: ctMaxDt}
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
	n0 := 0
	for n0 < len(st.held) && (last || st.held[n0].Timestamp+lag <= st.watermark) {
		n0++
	}
	out := make([]Fix, n0)
	for k := range n0 {
		f := st.held[k]
		if kf := st.flights[f.Flight]; kf != nil && f.Status != FixUnconfirmed {
			if km, ok := kf.smoothed(geom, f.Timestamp, f.Timestamp+lag); ok {
				f.Smoothed = km
				stats.SmoothedFixes++
			}
		}
		out[k] = f
	}
	st.held = slices.Delete(st.held, 0, n0)

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

// update は点 f でフィルタを 1 歩進める。最初の点は位置をそのまま、速度
// （と旋回率）は 0 に大きな分散で置く。
func (kf *kalman) update(stats *Stats, cfg Config, f Fix) {
	n := kf.n
	z := vec{f.Position.ENU.E, f.Position.ENU.N, f.Position.ENU.U}
	var r mat
	for i := range 3 {
		for j := range 3 {
			r[i][j] = f.Position.Cov[i][j]
		}
	}
	kf.last = max(kf.last, f.Timestamp)
	if len(kf.steps) == 0 {
		var p mat
		for i := range 3 {
			for j := range 3 {
				p[i][j] = r[i][j]
			}
			p[3+i][3+i] = cfg.SmoothInitialVelocitySigmaMps * cfg.SmoothInitialVelocitySigmaMps
		}
		// 旋回率の分散は範囲に入ったときに置く（update）
		kf.steps = append(kf.steps, kfStep{t: f.Timestamp, xPred: z, x: z, pPred: p, p: p, f: identity(n)})
		return
	}
	prev := kf.steps[len(kf.steps)-1]
	dt := float64(f.Timestamp-prev.t) / 1e9
	// 旋回は SSR から SmoothTurnMaxRangeM 以内でだけ回す。遠方では方位の
	// 雑音が km に及び、旋回率は走査の間隔では決まらない。決まらない旋回率で
	// 回すと雑音に曲線を当てて km の誤りになる（真値との比較で確認）。
	// 旋回は空港の近傍に集中する（1°/s 以上の点の 9 割が 50 km 以内）
	turn := n == 7 && dt <= kf.ctMaxDt && math.Hypot(prev.x[iE], prev.x[iN]) <= cfg.SmoothTurnMaxRangeM
	if turn && prev.p[iOmega][iOmega] == 0 {
		// 範囲に入った。旋回率を 0 から学び直す
		prev.p[iOmega][iOmega] = initialTurnRateSigma * initialTurnRateSigma
	}
	xPred, fm := kf.predict(prev.x, dt, turn)
	q := processNoise(n, dt, cfg)
	pPred := fm.mul(prev.p, n).mul(fm.transpose(n), n).add(q, n)

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
	// 残差が χ²(3) の 99% 点を大きく超えるときは、予測の共分散を残差に
	// 合うまで膨らませて観測に寄せる（fading memory）。モデルから外れた
	// とき（遠方の雑音で覚えた誤った旋回率など）に、小さい P のまま観測を
	// 無視し続けて発散するのを防ぐ
	if nis > smoothNIS99 {
		scale := nis / smoothNIS99
		for i := range n {
			for j := range n {
				pPred[i][j] *= scale
			}
		}
		for i := range 3 {
			for j := range 3 {
				s[i][j] = pPred[i][j] + r[i][j]
			}
		}
		if sInv, ok = inverse3(s); !ok {
			kf.steps = append(kf.steps, kfStep{t: f.Timestamp, xPred: xPred, x: xPred, pPred: pPred, p: pPred, f: fm})
			return
		}
		stats.SmoothInflated++
	}
	stats.SmoothUpdates++
	stats.SmoothNISSum += nis
	if nis > smoothNIS99 {
		stats.SmoothNISOver99++
	}
	// K = P Hᵀ S⁻¹ (n×3)
	var k [stateDim][3]float64
	for i := range n {
		for j := range 3 {
			for m := range 3 {
				k[i][j] += pPred[i][m] * sInv[m][j]
			}
		}
	}
	x := xPred
	for i := range n {
		for j := range 3 {
			x[i] += k[i][j] * y[j]
		}
	}
	if n == 7 {
		x[iOmega] = max(-maxTurnRate, min(maxTurnRate, x[iOmega]))
	}
	// P = (I − K H) P_pred (I − K H)ᵀ + K R Kᵀ（Joseph 形）
	ikh := identity(n)
	for i := range n {
		for j := range 3 {
			ikh[i][j] -= k[i][j]
		}
	}
	p := ikh.mul(pPred, n).mul(ikh.transpose(n), n)
	for i := range n {
		for j := range n {
			for a := range 3 {
				for b := range 3 {
					p[i][j] += k[i][a] * r[a][b] * k[j][b]
				}
			}
		}
	}
	if n == 7 && !turn {
		// 回していない間は旋回率を持たない（等速と同じ）。不確かさを持ち越すと
		// 速度の更新に漏れて針路が荒れる
		x[iOmega] = 0
		for i := range n {
			p[i][iOmega], p[iOmega][i] = 0, 0
		}
	}
	kf.steps = append(kf.steps, kfStep{t: f.Timestamp, xPred: xPred, x: x, pPred: pPred, p: p, f: fm})
}

// maxTurnRate は旋回率の上限 [rad/s]（10°/s。標準率旋回の 3 倍）。
const maxTurnRate = 10 * math.Pi / 180

// predict は状態を dt [s] 進めた予測と、その遷移のヤコビアンを返す。
// CV は等速、CT は turn が真のとき水平の速度が旋回率 ω で回る（協調旋回）。
// turn が偽なら直線で進める（旋回率はそのまま持ち越し、ω = 0 の
// ヤコビアンで観測から学び続ける）。
func (kf *kalman) predict(x vec, dt float64, turn bool) (vec, mat) {
	f := identity(kf.n)
	out := x
	out[iU] += x[iVU] * dt
	f[iU][iVU] = dt
	if kf.n == 6 {
		out[iE] += x[iVE] * dt
		out[iN] += x[iVN] * dt
		f[iE][iVE], f[iN][iVN] = dt, dt
		return out, f
	}
	ve, vn, w := x[iVE], x[iVN], x[iOmega]
	if !turn {
		w = 0
	}
	wt := w * dt
	c, s := math.Cos(wt), math.Sin(wt)
	// A = sin ωT / ω, B = (1 − cos ωT) / ω とその ω 微分。|ωT| が小さいときは級数
	var a, b, da, db float64
	if math.Abs(wt) < 1e-4 {
		t2, t3 := dt*dt, dt*dt*dt
		a, b = dt-w*w*t3/6, w*t2/2-w*w*w*t2*t2/24
		da, db = -w*t3/3, t2/2-w*w*t2*t2/8
	} else {
		a, b = s/w, (1-c)/w
		da, db = (dt*c*w-s)/(w*w), (dt*s*w-(1-c))/(w*w)
	}
	out[iE] = x[iE] + ve*a - vn*b
	out[iN] = x[iN] + ve*b + vn*a
	out[iVE] = ve*c - vn*s
	out[iVN] = ve*s + vn*c
	f[iE][iVE], f[iE][iVN], f[iE][iOmega] = a, -b, ve*da-vn*db
	f[iN][iVE], f[iN][iVN], f[iN][iOmega] = b, a, ve*db+vn*da
	f[iVE][iVE], f[iVE][iVN], f[iVE][iOmega] = c, -s, -dt*out[iVN]
	f[iVN][iVE], f[iVN][iVN], f[iVN][iOmega] = s, c, dt*out[iVE]
	return out, f
}

// smoothedNIS99 は χ²(3) の 99% 点。残差がこれを超える点を数える。
const smoothNIS99 = 11.345

// smoothed は時刻 t の点を、時刻 until までの点で平滑化した結果を返す。
// RTS の後退計算を until の点から t の点まで行う。
func (kf *kalman) smoothed(geom Geometry, t, until int64) (*Kinematics, bool) {
	n := kf.n
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
		// 旋回率を持たない歩（分散 0）は 6 次元で逆行列を取る（擬似逆行列）
		dim := n
		if n == 7 && next.pPred[iOmega][iOmega] == 0 {
			dim = 6
		}
		inv, ok := next.pPred.inverse(dim)
		if !ok {
			x, p = cur.x, cur.p
			continue
		}
		c := cur.p.mul(next.f.transpose(n), n).mul(inv, n)
		x = cur.x.add(c.mulVec(x.sub(next.xPred, n), n), n)
		p = cur.p.add(c.mul(p.sub(next.pPred, n), n).mul(c.transpose(n), n), n)
	}
	kf.steps[i].emitted = true
	enu := geodesy.ENU{E: x[iE], N: x[iN], U: x[iU]}
	lla, err := geom.conv.ENUToLLA(enu)
	if err != nil {
		return nil, false
	}
	return &Kinematics{
		Lat: lla.Lat, Lon: lla.Lon, Alt: lla.Alt, ENU: enu,
		Velocity: geodesy.ENU{E: x[iVE], N: x[iVN], U: x[iVU]},
		TurnRate: x[iOmega], HasTurnRate: n == 7,
		Cov: p,
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

// processNoise は Δt [s] の過程雑音。位置・速度は白色加速度（水平
// SmoothAccelSigmaMps2、鉛直 SmoothVerticalAccelSigmaMps2）の標準形、
// 旋回率（CT）は白色雑音 SmoothTurnRateSigmaDps。
func processNoise(n int, dt float64, cfg Config) mat {
	var q mat
	t2, t3 := dt*dt, dt*dt*dt
	for i := range 3 {
		s2 := cfg.SmoothAccelSigmaMps2 * cfg.SmoothAccelSigmaMps2
		if i == iU {
			s2 = cfg.SmoothVerticalAccelSigmaMps2 * cfg.SmoothVerticalAccelSigmaMps2
		}
		q[i][i] = s2 * t3 / 3
		q[i][3+i] = s2 * t2 / 2
		q[3+i][i] = s2 * t2 / 2
		q[3+i][3+i] = s2 * dt
	}
	if n == 7 {
		sw := cfg.SmoothTurnRateSigmaDps * math.Pi / 180
		q[iOmega][iOmega] = sw * sw * dt
	}
	return q
}

func identity(n int) mat {
	var m mat
	for i := range n {
		m[i][i] = 1
	}
	return m
}

func (a mat) mul(b mat, n int) mat {
	var c mat
	for i := range n {
		for k := range n {
			if a[i][k] == 0 {
				continue
			}
			for j := range n {
				c[i][j] += a[i][k] * b[k][j]
			}
		}
	}
	return c
}

func (a mat) mulVec(v vec, n int) vec {
	var out vec
	for i := range n {
		for j := range n {
			out[i] += a[i][j] * v[j]
		}
	}
	return out
}

func (a mat) add(b mat, n int) mat {
	for i := range n {
		for j := range n {
			a[i][j] += b[i][j]
		}
	}
	return a
}

func (a mat) sub(b mat, n int) mat {
	for i := range n {
		for j := range n {
			a[i][j] -= b[i][j]
		}
	}
	return a
}

func (a mat) transpose(n int) mat {
	var t mat
	for i := range n {
		for j := range n {
			t[j][i] = a[i][j]
		}
	}
	return t
}

// inverse は Gauss-Jordan（部分ピボット）による逆行列。特異なら ok が偽。
func (a mat) inverse(n int) (mat, bool) {
	inv := identity(n)
	for col := range n {
		piv := col
		for r := col + 1; r < n; r++ {
			if math.Abs(a[r][col]) > math.Abs(a[piv][col]) {
				piv = r
			}
		}
		if a[piv][col] == 0 {
			return mat{}, false
		}
		a[col], a[piv] = a[piv], a[col]
		inv[col], inv[piv] = inv[piv], inv[col]
		d := a[col][col]
		for j := range n {
			a[col][j] /= d
			inv[col][j] /= d
		}
		for r := range n {
			if r == col || a[r][col] == 0 {
				continue
			}
			m := a[r][col]
			for j := range n {
				a[r][j] -= m * a[col][j]
				inv[r][j] -= m * inv[col][j]
			}
		}
	}
	return inv, true
}

func (v vec) add(w vec, n int) vec {
	for i := range n {
		v[i] += w[i]
	}
	return v
}

func (v vec) sub(w vec, n int) vec {
	for i := range n {
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
