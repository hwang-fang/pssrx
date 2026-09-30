package bistatic

import (
	"math"

	"pssrx/internal/geodesy"
	"pssrx/internal/pssr/plot"
	"pssrx/internal/pssr/tracking"
	"pssrx/internal/ssr"
)

// Solution は解いた位置と、検算のための量。
type Solution struct {
	Position tracking.Position
	// Measure は観測の標準偏差・双基地距離・幾何因子など、位置と一緒に出す量。
	Measure tracking.Measurement
	// GroundRangeM は SSR からの地上距離 ρ [m]（SSR の ENU 平面上の距離）。
	GroundRangeM float64
	// RangeSSRM / RangeStationM は SSR・応答局から機体までの斜距離 [m]。
	RangeSSRM     float64
	RangeStationM float64
	// ResidualM は検算値 | |P| + |P−R| − L | [m]。Iterations は曲率の反復回数。
	ResidualM  float64
	Iterations int
}

// Geometry は位置推定に使う、SSR と応答局の幾何。構築後は変えない。
type Geometry struct {
	conv    *geodesy.ENUConverter // SSR を原点にした ENU
	station geodesy.ENU           // 応答局 R = (E_r, N_r, U_r)
	h0      float64               // SSR の標高 [m]。ENU 原点の高さ
	B       float64               // 基線の水平成分 √(E_r² + N_r²) [m]
}

// NewGeometry は SSR と応答局の位置から Geometry を作る。
func NewGeometry(ssr, station geodesy.OrthometricLLA, geoid geodesy.GeoidHeightProvider) (Geometry, error) {
	conv, err := geodesy.NewENUConverter(ssr, geoid)
	if err != nil {
		return Geometry{}, err
	}
	st, err := conv.LLAToENU(station)
	if err != nil {
		return Geometry{}, err
	}
	return Geometry{conv: conv, station: st, h0: ssr.Alt, B: math.Hypot(st.E, st.N)}, nil
}

// Locate はプロットの位置を解く。解けなければ ok が偽で、理由は stats に数える。
//
// SSR を原点にした ENU で、機体を P = (ρ sinθ, ρ cosθ, z) とおく（θ は
// ビーム方位、ρ は地上距離、z は高さ）。双基地距離 L = (τ − 応答遅延)·c に
// 対し
//
//	|P| + |P − R| = L      R は応答局
//
// は z を与えれば ρ の 2 次方程式になり、閉形式で解ける（solveRho）。
// z は地球の曲率のぶん ρ に依存するので、「z を与えて ρ を解く → その ρ で
// 標高が h になる z を求める」を z が動かなくなるまで繰り返す。収縮率は
// 400 km でも 1/500 程度で、2〜3 回で CurvatureTolM に収まる。z の更新は
// 球近似ではなく geodesy の厳密な変換（楕円体 + ジオイド）で行う。
// 大気屈折は無視する。
//
// 解けなかったときも、列から分かる項目（時刻・スコーク・高度・τ・方位・
// 列の形）を埋めた Fix を返し、理由を Fix.Drop に入れる（デバッグ出力用）。
func Locate(g Geometry, stats *Stats, params Params, cfg Config, p plot.Plot) (tracking.Fix, bool) {
	codes := p.Codes()
	first, last := p.AzimuthSpan()
	f := tracking.Fix{
		Timestamp: p.Timestamp, Squawk: p.Squawk, AltitudeFt: p.AltitudeFt, Replies: len(p.Replies),
		TauNs: p.TauNs, Azimuth: p.Azimuth, AzimuthFirst: first, AzimuthLast: last,
		ModeAReplies: codes.ModeA, ModeCReplies: codes.ModeC, AltitudeSpreadFt: codes.AltitudeSpreadFt,
		Siblings: p.Siblings,
	}
	sol, res := solve(g, stats, params, cfg, p)
	if res != locateOK {
		f.Drop = res.String()
		return f, false
	}
	f.Located, f.Position, f.Measure = true, sol.Position, sol.Measure
	return f, true
}

// Solve は Locate の本体で、検算のための量も返す。
func Solve(g Geometry, stats *Stats, params Params, cfg Config, p plot.Plot) (Solution, bool) {
	sol, res := solve(g, stats, params, cfg, p)
	return sol, res == locateOK
}

func solve(g Geometry, stats *Stats, params Params, cfg Config, p plot.Plot) (Solution, locateResult) {
	L := float64(p.TauNs-ssr.TransponderDelayNs) * ssr.SpeedOfLightMPerNs
	if L <= 0 {
		stats.Inconsistent++
		return Solution{}, locateInconsistent
	}
	// 基線特異点の安全弁。z > 0 なら一意性判定が自動的に弾くが、z ≈ 0 では
	// 効かないので先に見る
	if L <= g.B+cfg.BaselineMarginM {
		stats.Baseline++
		return Solution{}, locateBaseline
	}
	h := HeightFromPressureAltitude(p.AltitudeFt)
	sinT, cosT := math.Sincos(p.Azimuth)

	var (
		z          = h - g.h0 // 曲率を無視した初期値
		rho        float64
		res        locateResult
		iterations int
		converged  bool
	)
	for iterations = 1; iterations <= cfg.CurvatureMaxIter; iterations++ {
		rho, res = solveRho(g, cfg, L, sinT, cosT, z)
		if res != locateOK {
			break
		}
		q, err := pointAt(g, rho*sinT, rho*cosT, h)
		if err != nil {
			res = locateInconsistent
			break
		}
		converged = math.Abs(q.U-z) < cfg.CurvatureTolM
		z = q.U
		if converged {
			// 収束した z でもう一度解いて、ρ と z を整合させる
			rho, res = solveRho(g, cfg, L, sinT, cosT, z)
			break
		}
	}
	if res == locateOK && !converged {
		res = locateNonConvergent
	}
	switch res {
	case locateInconsistent:
		stats.Inconsistent++
		return Solution{}, res
	case locateAmbiguous:
		stats.Ambiguous++
		return Solution{}, res
	case locateNoSolution:
		stats.NoSolution++
		return Solution{}, res
	case locateNonConvergent:
		stats.NonConvergent++
		return Solution{}, res
	}
	q := geodesy.ENU{E: rho * sinT, N: rho * cosT, U: z}
	lla, err := g.conv.ENUToLLA(q)
	if err != nil {
		stats.Inconsistent++
		return Solution{}, locateInconsistent
	}
	d1, d2 := math.Sqrt(q.E*q.E+q.N*q.N+q.U*q.U), distToStation(g, q)
	stats.Fixes++
	sigmaTheta := azimuthSigma(cfg, p)
	cov, gf, sigmaL := covariance(g, cfg, sigmaTheta, rho, z, sinT, cosT, d1, d2)
	residual := math.Abs(d1 + d2 - L)
	return Solution{
		Position: tracking.Position{Lat: lla.Lat, Lon: lla.Lon, Alt: lla.Alt, ENU: q, Cov: cov},
		Measure: tracking.Measurement{
			SigmaBistaticM: sigmaL, SigmaAzimuthRad: sigmaTheta, SigmaAltitudeM: cfg.SigmaAltitudeM,
			BistaticRangeM: L, GroundRangeM: rho, GeometryFactor: gf,
			ResidualM: residual, Iterations: iterations,
		},
		GroundRangeM: rho, RangeSSRM: d1, RangeStationM: d2,
		ResidualM: residual, Iterations: iterations,
	}, locateOK
}

type locateResult int

const (
	locateOK            locateResult = iota
	locateInconsistent               // L が 0 以下、または座標変換の失敗
	locateBaseline                   // 双基地距離が基線長 + 余裕以下（基線特異点）
	locateAmbiguous                  // 正根が 2 つ（機体が基線の近傍）。|z| ≥ ℓ かつ p̂ > 0
	locateNoSolution                 // 正根が無い。|z| ≥ ℓ かつ p̂ ≤ 0
	locateNonConvergent              // 曲率の反復が収束しない
)

// String は出力（Fix.Drop）に書く表記。
func (r locateResult) String() string {
	switch r {
	case locateOK:
		return ""
	case locateInconsistent:
		return "locate_inconsistent"
	case locateBaseline:
		return "locate_baseline"
	case locateAmbiguous:
		return "locate_ambiguous"
	case locateNoSolution:
		return "locate_no_solution"
	case locateNonConvergent:
		return "locate_nonconvergent"
	}
	return "locate_unknown"
}

// solveRho は高さ z を与えて地上距離 ρ を閉形式で解く。
//
// 機体と同じ高さの水平面に SSR と局を射影し、d₁ = |P|、d₂ = |P − R| を
//
//	d₁² = ρ² + z²
//	d₂² = ρ² − 2pρ + B² + (z − U_r)²      p = E_r sinθ + N_r cosθ
//
// と書いて d₂ = L − d₁ を二乗すると ρ² が消え、d₁ = ℓ + p̂ρ という 1 次式になる。
//
//	p̂ = p / L,  K̂ = 1 − p̂²
//	ℓ = ((L − B)(L + B) + U_r(2z − U_r)) / (2L)     実効半直弦
//	K̂ρ² − 2ℓp̂ρ + (z² − ℓ²) = 0
//	ρ = (p̂ℓ + √(ℓ² − K̂z²)) / K̂
//
// 根の積は (z² − ℓ²)/K̂ なので、|z| < ℓ なら正根はちょうど 1 つで和の根が
// それになる。|z| ≥ ℓ は p̂ > 0 なら正根 2 つ（曖昧）、p̂ ≤ 0 なら正根無し。
// K̂ ≤ 1 から |z| < ℓ のとき判別式 ℓ² − K̂z² は自動的に正になる。
// (L − B)(L + B) を L² − B² と書くと L ≈ B で桁落ちする。
func solveRho(g Geometry, cfg Config, L, sinT, cosT, z float64) (float64, locateResult) {
	R := g.station
	pHat := (R.E*sinT + R.N*cosT) / L
	kHat := 1 - pHat*pHat
	ell := ((L-g.B)*(L+g.B) + R.U*(2*z-R.U)) / (2 * L)
	if math.Abs(z) >= ell-cfg.ZMarginM {
		if pHat > 0 {
			return 0, locateAmbiguous
		}
		return 0, locateNoSolution
	}
	disc := ell*ell - kHat*z*z
	if disc <= 0 { // 一意性判定を通れば起きない
		return 0, locateInconsistent
	}
	return (pHat*ell + math.Sqrt(disc)) / kHat, locateOK
}

// azimuthSigma は列のビーム中心の方位の標準偏差 [rad]。
//
// 列の方位の幅（最初と最後の質問の方位の差）が完全な幅 DwellFullSpanRad に
// 満たない列はドウェルの断片で、最初と最後の中点が欠けた側と反対に寄る。
// どちら側かは分からないので、符号の分からない偏り
// a = AzimuthFragmentFactor × 欠けた幅 / 2 の分散 a² を、完全な列の雑音
// SigmaAzimuthRad の分散に足す。
//
//	σ_θ = √(σ0² + a²)
//
// 応答数ではなく幅で見るのは、途中の応答が抜けただけの列（両端がそろう）は
// 偏らないため。真値との比較で、同じ幅なら応答数によらず誤差が同じだった。
func azimuthSigma(cfg Config, p plot.Plot) float64 {
	first, last := p.AzimuthSpan()
	span := math.Abs(math.Remainder(last-first, 2*math.Pi))
	a := cfg.AzimuthFragmentFactor * max(0, cfg.DwellFullSpanRad-span) / 2
	return math.Hypot(cfg.SigmaAzimuthRad, a)
}

// covariance は観測量の分散を位置へ線形伝播する。
//
//	F(ρ) = d1 + d2 − L = 0 の陰関数微分から
//	  ∂ρ/∂L = 1 / gf,   ∂ρ/∂z = −(z/d1 + (z − U_r)/d2) / gf,   gf = ρ/d1 + (ρ − p)/d2
//	ヤコビアン
//	  ∂P/∂L = u ∂ρ/∂L,  ∂P/∂θ = ρ (cosθ, −sinθ, 0),  ∂P/∂z = (0, 0, 1) + u ∂ρ/∂z
//	  u = (sinθ, cosθ, 0)
//	C = J diag(σ_L², σ_θ², σ_z²) Jᵀ
//
// σ_L は t1・t2 のジッタと応答遅延の公差を合成したもの。σ_θ は応答数に
// よる（azimuthSigma）。
//
// 幾何因子 gf と双基地距離の標準偏差 σ_L も返す（出力に残す）。
func covariance(g Geometry, cfg Config, sigmaTheta, rho, z, sinT, cosT, d1, d2 float64) (C [3][3]float64, gf, sigmaL float64) {
	R := g.station
	p := R.E*sinT + R.N*cosT
	gf = rho/d1 + (rho-p)/d2
	dRhodL := 1 / gf
	dRhodZ := -(z/d1 + (z-R.U)/d2) / gf

	sigmaL = ssr.SpeedOfLightMPerNs * math.Hypot(cfg.SigmaTimingNs, cfg.SigmaTransponderNs)
	sigma := [3]float64{sigmaL, sigmaTheta, cfg.SigmaAltitudeM}
	// 列が ∂P/∂L, ∂P/∂θ, ∂P/∂z
	J := [3][3]float64{
		{sinT * dRhodL, rho * cosT, sinT * dRhodZ},
		{cosT * dRhodL, -rho * sinT, cosT * dRhodZ},
		{0, 0, 1},
	}
	for i := range 3 {
		for j := range 3 {
			var v float64
			for k := range 3 {
				v += J[i][k] * sigma[k] * sigma[k] * J[j][k]
			}
			C[i][j] = v
		}
	}
	return C, gf, sigmaL
}

// pointAt は ENU の水平位置 (e, n) で標高が h になる点を返す。
//
// 標高は U にほぼ比例し、曲率のぶんだけずれる。U の初期値を h として
// 「標高の誤差ぶん U を戻す」反復で 0.1 mm 未満に収束する。
func pointAt(g Geometry, e, n, h float64) (geodesy.ENU, error) {
	u := h - g.h0
	for range 8 {
		lla, err := g.conv.ENUToLLA(geodesy.ENU{E: e, N: n, U: u})
		if err != nil {
			return geodesy.ENU{}, err
		}
		d := lla.Alt - h
		u -= d
		if math.Abs(d) < 1e-4 {
			break
		}
	}
	return geodesy.ENU{E: e, N: n, U: u}, nil
}

func distToStation(g Geometry, q geodesy.ENU) float64 {
	de, dn, du := q.E-g.station.E, q.N-g.station.N, q.U-g.station.U
	return math.Sqrt(de*de + dn*dn + du*du)
}

// HeightFromPressureAltitude は Mode C の気圧高度 [ft] を標高 [m] に直す。
//
// 気圧高度は標準大気（1013.25 hPa）基準で、実際の気圧配置とは数百 m
// 違いうる。QNH と気温による補正はここに集約して後から足す。いまは単位換算のみ。
func HeightFromPressureAltitude(ft int) float64 {
	return float64(ft) * 0.3048
}
