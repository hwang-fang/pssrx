package pssr

import (
	"math"

	"pssrx/internal/config"
	"pssrx/internal/geodesy"
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
	// Cov は SSR の ENU 系での位置の共分散 [m²]。添字は E, N, U の順。
	// 観測量（双基地距離 L、方位 θ、高さ z）の分散を線形伝播したもの。
	Cov [3][3]float64
}

// Fix はプロットとその位置。
type Fix struct {
	Plot
	Position Position
}

// Geometry は位置推定に使う、SSR と応答局の幾何。構築後は変えない。
type Geometry struct {
	conv    *geodesy.ENUConverter // SSR を原点にした ENU
	station geodesy.ENU           // 応答局 R = (E_r, N_r, U_r)
	h0      float64               // SSR の標高 [m]。ENU 原点の高さ
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
	return Geometry{conv: conv, station: st, h0: ssr.Alt}, nil
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
// 400 km でも 1/500 程度で、2〜3 回で 1 mm 未満に収まる。z の更新は
// 球近似ではなく geodesy の厳密な変換（楕円体 + ジオイド）で行う。
// 大気屈折は無視する。
func Locate(g Geometry, stats *Stats, params Params, cfg Config, p Plot) (Fix, bool) {
	L := float64(p.TauNs-config.TransponderDelayNs) * config.SpeedOfLightMPerNs
	h := HeightFromPressureAltitude(p.AltitudeFt)
	sinT, cosT := math.Sincos(p.Azimuth)

	var (
		z   = h - g.h0 // 曲率を無視した初期値
		rho float64
		res locateResult
		q   geodesy.ENU
	)
	for range 20 {
		rho, res = solveRho(g, cfg, L, sinT, cosT, z)
		if res != locateOK {
			break
		}
		var err error
		q, err = pointAt(g, rho*sinT, rho*cosT, h)
		if err != nil {
			res = locateInconsistent
			break
		}
		if math.Abs(q.U-z) < 1e-3 {
			z = q.U
			rho, res = solveRho(g, cfg, L, sinT, cosT, z)
			break
		}
		z = q.U
	}
	switch res {
	case locateInconsistent:
		stats.Inconsistent++
		return Fix{}, false
	case locateAmbiguous:
		stats.Ambiguous++
		return Fix{}, false
	case locateSingular:
		stats.Singular++
		return Fix{}, false
	}
	if rho > params.MaxRangeM*1.05 {
		stats.OutOfRange++
		return Fix{}, false
	}
	q = geodesy.ENU{E: rho * sinT, N: rho * cosT, U: z}
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
			GroundRangeM:  rho,
			RangeSSRM:     d1,
			RangeStationM: d2,
			Cov:           covariance(g, cfg, rho, z, sinT, cosT, d1, d2),
		},
	}, true
}

type locateResult int

const (
	locateOK           locateResult = iota
	locateInconsistent              // 判別式が負、または方程式を満たす根が無い
	locateAmbiguous                 // 有効な根が 2 つ（機体が基線の近傍）
	locateSingular                  // 機体が基線上に近く、解が発散する
)

// solveRho は高さ z を与えて地上距離 ρ を閉形式で解く。
//
//	p  = E_r sinθ + N_r cosθ            局の方位方向への水平射影
//	D² = E_r² + N_r² + (z − U_r)²
//	A  = L² + z² − D²
//	(L² − p²) ρ² − pA ρ + (L² z² − A²/4) = 0
//	ρ  = (pA ± L √(A² − 4z²(L² − p²))) / (2(L² − p²))
//
// 二乗で増えた偽の根は |P| + |P−R| = L を直接検算して除く。
func solveRho(g Geometry, cfg Config, L, sinT, cosT, z float64) (float64, locateResult) {
	R := g.station
	p := R.E*sinT + R.N*cosT
	D2 := R.E*R.E + R.N*R.N + (z-R.U)*(z-R.U)
	A := L*L + z*z - D2
	den := L*L - p*p
	// L は SSR→機体→局の経路長なので基線長 |R| ≥ |p| を下回らない
	if den <= 0 || L*L < R.E*R.E+R.N*R.N+R.U*R.U {
		return 0, locateInconsistent
	}
	disc := A*A - 4*z*z*den
	if disc < 0 {
		return 0, locateInconsistent
	}
	s := L * math.Sqrt(disc)
	roots := [2]float64{(p*A + s) / (2 * den), (p*A - s) / (2 * den)}

	var (
		rho   float64
		valid int
	)
	for _, r := range roots {
		if r <= 0 {
			continue
		}
		d1 := math.Sqrt(r*r + z*z)
		d2 := math.Sqrt(r*r - 2*p*r + D2)
		if math.Abs(d1+d2-L) > 1e-6*L {
			continue
		}
		// ∂(d1+d2)/∂ρ = cos ε1 + cos ξ2。基線上（双基地角 180°）で 0 に近づき、
		// 距離の誤差が発散する
		if r/d1+(r-p)/d2 < cfg.MinGeometryFactor {
			return 0, locateSingular
		}
		rho = r
		valid++
	}
	switch valid {
	case 0:
		return 0, locateInconsistent
	case 2:
		return 0, locateAmbiguous
	}
	return rho, locateOK
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
// σ_L は t1・t2 のジッタと応答遅延の公差を合成したもの。
func covariance(g Geometry, cfg Config, rho, z, sinT, cosT, d1, d2 float64) [3][3]float64 {
	R := g.station
	p := R.E*sinT + R.N*cosT
	gf := rho/d1 + (rho-p)/d2
	dRhodL := 1 / gf
	dRhodZ := -(z/d1 + (z-R.U)/d2) / gf

	sigmaL := config.SpeedOfLightMPerNs * math.Hypot(cfg.SigmaTimingNs, cfg.SigmaTransponderNs)
	sigma := [3]float64{sigmaL, cfg.SigmaAzimuthRad, cfg.SigmaAltitudeM}
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
