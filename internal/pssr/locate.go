package pssr

import (
	"fmt"
	"math"

	"pssrx/internal/geodesy"
	"pssrx/internal/ssr"
)

// Position は推定した機体の位置。
type Position struct {
	Lat float64 // WGS84 [deg]
	Lon float64 // WGS84 [deg]
	Alt float64 // 標高 [m]。気圧高度から換算したもの
	// GroundRangeM は SSR からの地上距離 ρ [m]（SSR の ENU 平面上の距離）。
	GroundRangeM float64
	// RangeSSRM / RangeStationM は SSR・応答局から機体までの斜距離 [m]。
	RangeSSRM     float64
	RangeStationM float64
	// ENU は SSR を原点にした ENU 座標 [m]。連続性の門の距離計算と、
	// 便どうしの比較（エコー判定）に使う。
	ENU geodesy.ENU
	// Cov は SSR の ENU 系での位置の共分散 [m²]。添字は E, N, U の順。
	// 観測量（双基地距離 L、方位 θ、高さ z）の分散を線形伝播したもの。
	Cov [3][3]float64
	// ResidualM は検算値 | |P| + |P−R| − L | [m]。Iterations は曲率の反復回数。
	ResidualM  float64
	Iterations int
}

// Fix はプロットとその位置。Track と Status は連続性の段（Track）が付ける。
type Fix struct {
	Plot
	Position Position
	// Track は便 ID。処理の開始からの連番で、打ち切った便の ID は再利用
	// しない。0 は未付与。
	Track int64
	// Status は連続性の判定。
	Status FixStatus
}

// FixStatus は位置が便として確定したかの判定。
type FixStatus uint8

const (
	FixOK          FixStatus = iota // 確定した便の点
	FixUnconfirmed                  // 便が確定に届かず棄却
)

// String は CSV に書く表記。
func (s FixStatus) String() string {
	switch s {
	case FixOK:
		return "ok"
	case FixUnconfirmed:
		return "unconfirmed"
	}
	return fmt.Sprintf("status(%d)", uint8(s))
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
func Locate(g Geometry, stats *Stats, params Params, cfg Config, p Plot) (Fix, bool) {
	L := float64(p.TauNs-ssr.TransponderDelayNs) * ssr.SpeedOfLightMPerNs
	if L <= 0 {
		stats.Inconsistent++
		return Fix{}, false
	}
	// 基線特異点の安全弁。z > 0 なら一意性判定が自動的に弾くが、z ≈ 0 では
	// 効かないので先に見る
	if L <= g.B+cfg.BaselineMarginM {
		stats.Baseline++
		return Fix{}, false
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
		return Fix{}, false
	case locateAmbiguous:
		stats.Ambiguous++
		return Fix{}, false
	case locateNoSolution:
		stats.NoSolution++
		return Fix{}, false
	case locateNonConvergent:
		stats.NonConvergent++
		return Fix{}, false
	}
	q := geodesy.ENU{E: rho * sinT, N: rho * cosT, U: z}
	lla, err := g.conv.ENUToLLA(q)
	if err != nil {
		stats.Inconsistent++
		return Fix{}, false
	}
	d1, d2 := math.Sqrt(q.E*q.E+q.N*q.N+q.U*q.U), distToStation(g, q)
	stats.Fixes++
	return Fix{
		Plot: p,
		Position: Position{
			Lat: lla.Lat, Lon: lla.Lon, Alt: lla.Alt,
			ENU:           q,
			GroundRangeM:  rho,
			RangeSSRM:     d1,
			RangeStationM: d2,
			Cov:           covariance(g, cfg, azimuthSigma(params, cfg, len(p.Replies)), rho, z, sinT, cosT, d1, d2),
			ResidualM:     math.Abs(d1 + d2 - L),
			Iterations:    iterations,
		},
	}, true
}

type locateResult int

const (
	locateOK            locateResult = iota
	locateInconsistent               // L が 0 以下、または座標変換の失敗
	locateAmbiguous                  // 正根が 2 つ（機体が基線の近傍）。|z| ≥ ℓ かつ p̂ > 0
	locateNoSolution                 // 正根が無い。|z| ≥ ℓ かつ p̂ ≤ 0
	locateNonConvergent              // 曲率の反復が収束しない
)

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

// azimuthSigma は応答数 n の列のビーム中心の方位の標準偏差 [rad]。
//
// 列が完全（n ≥ DwellFullReplies）なら SigmaAzimuthRad。欠けた応答 1 件に
// つき AzimuthFragmentFactor × （1 質問あたりのビームの回転角）を足す。
// 断片は最初と最後の中点が欠けた側と反対に寄るため。
func azimuthSigma(params Params, cfg Config, n int) float64 {
	missing := cfg.DwellFullReplies - n
	if missing <= 0 {
		return cfg.SigmaAzimuthRad
	}
	perInterrogation := 2 * math.Pi * params.MeanPRINs / float64(params.AroundTimeNs)
	return cfg.SigmaAzimuthRad + cfg.AzimuthFragmentFactor*float64(missing)*perInterrogation
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
func covariance(g Geometry, cfg Config, sigmaTheta, rho, z, sinT, cosT, d1, d2 float64) [3][3]float64 {
	R := g.station
	p := R.E*sinT + R.N*cosT
	gf := rho/d1 + (rho-p)/d2
	dRhodL := 1 / gf
	dRhodZ := -(z/d1 + (z-R.U)/d2) / gf

	sigmaL := ssr.SpeedOfLightMPerNs * math.Hypot(cfg.SigmaTimingNs, cfg.SigmaTransponderNs)
	sigma := [3]float64{sigmaL, sigmaTheta, cfg.SigmaAltitudeM}
	// 列が ∂P/∂L, ∂P/∂θ, ∂P/∂z
	J := [3][3]float64{
		{sinT * dRhodL, rho * cosT, sinT * dRhodZ},
		{cosT * dRhodL, -rho * sinT, cosT * dRhodZ},
		{0, 0, 1},
	}
	var C [3][3]float64
	for i := range 3 {
		for j := range 3 {
			var v float64
			for k := range 3 {
				v += J[i][k] * sigma[k] * sigma[k] * J[j][k]
			}
			C[i][j] = v
		}
	}
	return C
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
