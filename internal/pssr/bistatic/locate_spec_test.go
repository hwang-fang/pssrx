package bistatic

import (
	"math"
	"math/rand/v2"
	"testing"

	"pssrx/internal/geodesy"
	"pssrx/internal/geodesy/geoid"
	"pssrx/internal/pssr/plot"
)

func geoidForTest(t *testing.T) geodesy.GeoidHeightProvider {
	t.Helper()
	gm, err := geoid.Load()
	if err != nil {
		t.Fatal(err)
	}
	return gm
}

// 仕様書「PSSR 初期座標算出 実装仕様書 v3.0」のテストベクタ TV1〜TV7。
// 受信点を ENU で直接与え、曲率を切って solveRho 単体を検証する。

func enuGeometry(e, n, u float64) Geometry {
	return Geometry{station: geodesy.ENU{E: e, N: n, U: u}, B: math.Hypot(e, n)}
}

func noMargin() Config {
	cfg := DefaultConfig()
	cfg.ZMarginM = 0
	return cfg
}

// bistaticSum は検算用の d1 + d2。
func bistaticSum(g Geometry, rho, sinT, cosT, z float64) float64 {
	q := geodesy.ENU{E: rho * sinT, N: rho * cosT, U: z}
	return math.Sqrt(q.E*q.E+q.N*q.N+q.U*q.U) + distToStation(g, q)
}

// forward は真の位置から L と方位を作る（曲率無し）。
func forward(g Geometry, p geodesy.ENU) (L, azimuth float64) {
	d1 := math.Sqrt(p.E*p.E + p.N*p.N + p.U*p.U)
	return d1 + distToStation(g, p), math.Atan2(p.E, p.N)
}

func TestSpecTV1Basic(t *testing.T) {
	g := enuGeometry(0, 30000, 0)
	az := 1.0471975511965976
	sinT, cosT := math.Sincos(az)
	rho, res := solveRho(g, noMargin(), 100000, sinT, cosT, 10000)
	if res != locateOK {
		t.Fatalf("res = %v", res)
	}
	if want := 52417.223511; math.Abs(rho-want) > 1e-6 {
		t.Errorf("ρ = %.6f, 期待 %.6f", rho, want)
	}
	if e, n := rho*sinT, rho*cosT; math.Abs(e-45394.647157) > 1e-6 || math.Abs(n-26208.611756) > 1e-6 {
		t.Errorf("(E, N) = (%.6f, %.6f)", e, n)
	}
	if d := bistaticSum(g, rho, sinT, cosT, 10000); math.Abs(d-100000) > 1e-6*100000 {
		t.Errorf("d1+d2 = %.6f", d)
	}
}

func TestSpecTV2RoundTrip(t *testing.T) {
	g := enuGeometry(40000, -12000, 0)
	truth := geodesy.ENU{E: 62000, N: 88000, U: 10668}
	L, az := forward(g, truth)
	if math.Abs(L-211120.534156) > 1e-6 || math.Abs(az-0.613770095) > 1e-9 {
		t.Errorf("L = %.6f, az = %.9f", L, az)
	}
	sinT, cosT := math.Sincos(az)
	rho, res := solveRho(g, noMargin(), L, sinT, cosT, truth.U)
	if res != locateOK {
		t.Fatalf("res = %v", res)
	}
	if want := math.Hypot(truth.E, truth.N); math.Abs(rho-want) > 1e-9 {
		t.Errorf("ρ = %.9f, 期待 %.9f", rho, want)
	}
}

// TV3 / TV4: |z| ≥ ℓ のとき p̂ の符号で曖昧と解なしに分かれる。
func TestSpecTV3TV4Rejection(t *testing.T) {
	g := enuGeometry(0, 100000, 0)
	cases := []struct {
		azDeg float64
		want  locateResult
	}{
		{0, locateAmbiguous}, {5, locateAmbiguous}, {10, locateAmbiguous},
		{170, locateNoSolution}, {180, locateNoSolution},
	}
	for _, c := range cases {
		sinT, cosT := math.Sincos(c.azDeg * math.Pi / 180)
		if _, res := solveRho(g, noMargin(), 101000, sinT, cosT, 3000); res != c.want {
			t.Errorf("方位 %g°: res = %v, 期待 %v", c.azDeg, res, c.want)
		}
	}
	// 補助: 曖昧の条件では 2 次方程式の正根が 2 つあり、どちらも幾何を満たす
	sinT, cosT := math.Sincos(0)
	L, z := 101000.0, 3000.0
	pHat := (g.station.E*sinT + g.station.N*cosT) / L
	kHat := 1 - pHat*pHat
	ell := ((L - g.B) * (L + g.B)) / (2 * L)
	disc := ell*ell - kHat*z*z
	for _, rho := range []float64{(pHat*ell + math.Sqrt(disc)) / kHat, (pHat*ell - math.Sqrt(disc)) / kHat} {
		if rho <= 0 {
			t.Errorf("正根でない: %g", rho)
		}
		if d := bistaticSum(g, rho, sinT, cosT, z); math.Abs(d-L) > 1e-6*L {
			t.Errorf("ρ = %g で d1+d2 = %.6f", rho, d)
		}
	}
}

// TV5: 受信点に高低差があっても厳密。
func TestSpecTV5ReceiverHeight(t *testing.T) {
	g := enuGeometry(40000, -12000, 850)
	truth := geodesy.ENU{E: 62000, N: 88000, U: 10668}
	L, az := forward(g, truth)
	if math.Abs(L-211035.925145) > 1e-6 {
		t.Errorf("L = %.6f", L)
	}
	sinT, cosT := math.Sincos(az)
	rho, res := solveRho(g, noMargin(), L, sinT, cosT, truth.U)
	if res != locateOK {
		t.Fatalf("res = %v", res)
	}
	if want := 107647.573126; math.Abs(rho-want) > 1e-6 {
		t.Errorf("ρ = %.6f, 期待 %.6f", rho, want)
	}
	// 高低差補正を落とすと −44.245 m ずれる。補正項が効いていることの番人
	ellNoCorr := ((L - g.B) * (L + g.B)) / (2 * L)
	pHat := (g.station.E*sinT + g.station.N*cosT) / L
	kHat := 1 - pHat*pHat
	rhoNoCorr := (pHat*ellNoCorr + math.Sqrt(ellNoCorr*ellNoCorr-kHat*truth.U*truth.U)) / kHat
	if math.Abs(rhoNoCorr-107603.327677) > 1e-3 {
		t.Errorf("補正無しの ρ = %.6f, 期待 107603.327677", rhoNoCorr)
	}
}

// TV6: ランダムな往復。値が返れば真値と 1e-6 m 以内、棄却なら |z| ≥ ℓ が成立。
func TestSpecTV6Random(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 5))
	uniform := func(lo, hi float64) float64 { return lo + (hi-lo)*r.Float64() }
	rejected := 0
	for range 20000 {
		g := enuGeometry(uniform(-80e3, 80e3), uniform(-80e3, 80e3), uniform(-3e3, 3e3))
		truth := geodesy.ENU{E: uniform(-300e3, 300e3), N: uniform(-300e3, 300e3), U: uniform(0, 15e3)}
		L, az := forward(g, truth)
		sinT, cosT := math.Sincos(az)
		rho, res := solveRho(g, noMargin(), L, sinT, cosT, truth.U)
		if res != locateOK {
			rejected++
			ell := ((L-g.B)*(L+g.B) + g.station.U*(2*truth.U-g.station.U)) / (2 * L)
			if math.Abs(truth.U) < ell {
				t.Fatalf("棄却したが |z| < ℓ: z=%g ℓ=%g res=%v", truth.U, ell, res)
			}
			continue
		}
		if want := math.Hypot(truth.E, truth.N); math.Abs(rho-want) > 1e-6 {
			t.Fatalf("ρ = %.9f, 期待 %.9f (R=%+v P=%+v)", rho, want, g.station, truth)
		}
	}
	if rejected == 0 || rejected > 400 {
		t.Errorf("棄却 %d 件（一様分布では 0.4%% 程度のはず）", rejected)
	}
}

// TV7: 機体が SSR 直上なら ℓ = z が厳密に成り立つ（一意性判定の境界）。
func TestSpecTV7BoundaryIdentity(t *testing.T) {
	g := enuGeometry(40000, -12000, 850)
	truth := geodesy.ENU{E: 0, N: 0, U: 12000}
	L, _ := forward(g, truth)
	if math.Abs(L-55224.096289) > 1e-6 {
		t.Errorf("L = %.6f", L)
	}
	ell := ((L-g.B)*(L+g.B) + g.station.U*(2*truth.U-g.station.U)) / (2 * L)
	if math.Abs(ell-truth.U) > 1e-6 {
		t.Errorf("ℓ − z = %g", ell-truth.U)
	}
	sinT, cosT := math.Sincos(0)
	if _, res := solveRho(g, DefaultConfig(), L, sinT, cosT, truth.U); res == locateOK {
		t.Error("境界上が採択された")
	}
}

// TV8: 曲率の反復。仕様の球近似ではなく geodesy の厳密な z を使うので、
// 期待値は「OK、反復 3 回以内、z が初期値より約 900 m 低い、残差 ≤ 1e-6 L」。
// 受信点 (40 km, −12 km, 850 m) を SSR の緯度経度から ENU で置いた幾何で試す。
func TestSpecTV8Curvature(t *testing.T) {
	gm := geoidForTest(t)
	ssr := geodesy.OrthometricLLA{Lat: 34.85058333, Lon: 136.82093888, Alt: 0}
	conv, err := geodesy.NewENUConverter(ssr, gm)
	if err != nil {
		t.Fatal(err)
	}
	stationLLA, err := conv.ENUToLLA(geodesy.ENU{E: 40000, N: -12000, U: 850})
	if err != nil {
		t.Fatal(err)
	}
	g, err := NewGeometry(ssr, stationLLA, gm)
	if err != nil {
		t.Fatal(err)
	}
	// 仕様の TV8 と同じ L と方位。τ は ns に丸める
	L := 211035.925145
	tau := int64(math.RoundToEven(L/0.299792458)) + 3000
	var stats Stats
	fix, ok := Solve(g, &stats, Params{MaxRangeM: 400_000}, DefaultConfig(),
		plot.Plot{TauNs: tau, Azimuth: 0.613770095, AltitudeFt: 35000})
	if !ok {
		t.Fatalf("解けない: %+v", stats)
	}
	if fix.Iterations > 3 {
		t.Errorf("反復 %d 回", fix.Iterations)
	}
	zBase := HeightFromPressureAltitude(35000) - ssr.Alt
	// ρ ≈ 107.6 km なので ρ²/(2R) ≈ 900 m
	q, err := pointAt(g, fix.GroundRangeM*math.Sin(0.613770095), fix.GroundRangeM*math.Cos(0.613770095), HeightFromPressureAltitude(35000))
	if err != nil {
		t.Fatal(err)
	}
	if drop := zBase - q.U; drop < 850 || drop > 950 {
		t.Errorf("曲率による z の低下 %g m, 期待 約 900 m", drop)
	}
	if fix.ResidualM > 1e-6*L {
		t.Errorf("残差 %g m", fix.ResidualM)
	}
}
