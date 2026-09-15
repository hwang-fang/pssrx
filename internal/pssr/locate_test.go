package pssr_test

import (
	"math"
	"testing"

	"pssrx/internal/config"
	"pssrx/internal/geodesy"
	"pssrx/internal/geodesy/geoid"
	"pssrx/internal/pssr"
	"pssrx/internal/pssr/simtest"
)

var (
	// testdata/kx90.yaml の SSR と局
	ssrLLA     = geodesy.OrthometricLLA{Lat: 34.85058333, Lon: 136.82093888, Alt: 0}
	stationLLA = geodesy.OrthometricLLA{Lat: 34.8583717981495, Lon: 136.810685698149, Alt: 0}
)

// locator は幾何と統計をまとめたテスト用の入れ物。
type locator struct {
	geom  pssr.Geometry
	stats pssr.Stats
}

func newLocator(t *testing.T) (*locator, geodesy.GeoidHeightProvider) {
	t.Helper()
	gm, err := geoid.Load()
	if err != nil {
		t.Fatal(err)
	}
	geom, err := pssr.NewGeometry(ssrLLA, stationLLA, gm)
	if err != nil {
		t.Fatal(err)
	}
	return &locator{geom: geom}, gm
}

func (l *locator) Locate(p pssr.Plot) (pssr.Fix, bool) {
	return pssr.Locate(l.geom, &l.stats, testParams, pssr.DefaultConfig(), p)
}

func (l *locator) Stats() pssr.Stats { return l.stats }

// TestLocateRecoversKnownPosition は既知の位置から作った τ・方位・高度で
// 位置が復元できることを確認する。τ は 1 ns（0.3 m）に量子化されるので、
// 位置の誤差はその程度まで許す。
func TestLocateRecoversKnownPosition(t *testing.T) {
	l, gm := newLocator(t)
	cases := []struct {
		name     string
		lat, lon float64
		ft       int
	}{
		{"近距離・低高度（離陸直後）", 34.87, 136.85, 1000},
		{"中距離・南東", 34.5, 137.3, 12000},
		{"遠距離・北西 300 km", 36.9, 134.5, 37000},
		{"遠距離・南 380 km", 31.5, 137.0, 41000},
		{"局のほぼ真上", 34.8584, 136.8107, 5000},
		{"方位 0 付近", 35.9, 136.822, 20000},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ac := geodesy.OrthometricLLA{Lat: c.lat, Lon: c.lon, Alt: pssr.HeightFromPressureAltitude(c.ft)}
			obs, err := simtest.Observe(ssrLLA, stationLLA, ac, gm)
			if err != nil {
				t.Fatal(err)
			}
			fix, ok := l.Locate(pssr.Plot{TauNs: obs.TauNs, Azimuth: obs.Azimuth, AltitudeFt: c.ft})
			if !ok {
				t.Fatalf("解けない: stats %+v", l.Stats())
			}
			// 緯度 1e-5 度 ≈ 1.1 m、経度は cos(35°) で 0.9 m
			if d := math.Abs(fix.Position.Lat - c.lat); d > 1e-5 {
				t.Errorf("lat %.7f, 期待 %.7f (差 %.2g 度)", fix.Position.Lat, c.lat, d)
			}
			if d := math.Abs(fix.Position.Lon - c.lon); d > 1e-5 {
				t.Errorf("lon %.7f, 期待 %.7f (差 %.2g 度)", fix.Position.Lon, c.lon, d)
			}
			if d := math.Abs(fix.Position.Alt - ac.Alt); d > 1e-3 {
				t.Errorf("alt %.4f, 期待 %.4f", fix.Position.Alt, ac.Alt)
			}
			if fix.Position.RangeSSRM <= 0 || fix.Position.RangeStationM <= 0 || fix.Position.GroundRangeM <= 0 {
				t.Errorf("距離が正でない: %+v", fix.Position)
			}
		})
	}
}

// TestLocateNearBaseline は基線長（1.3 km）より近い機体も閉形式で解けることを
// 確認する。空港近傍の離着陸機がここに入る。
func TestLocateNearBaseline(t *testing.T) {
	l, gm := newLocator(t)
	cases := []struct {
		name     string
		lat, lon float64
		ft       int
	}{
		{"局の真上 1000 ft", stationLLA.Lat, stationLLA.Lon, 1000},
		{"局の 500 m 東（基線の横）", 34.8584, 136.8162, 800},
		{"SSR から 800 m 北", 34.8578, 136.8209, 1500},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ac := geodesy.OrthometricLLA{Lat: c.lat, Lon: c.lon, Alt: pssr.HeightFromPressureAltitude(c.ft)}
			obs, err := simtest.Observe(ssrLLA, stationLLA, ac, gm)
			if err != nil {
				t.Fatal(err)
			}
			fix, ok := l.Locate(pssr.Plot{TauNs: obs.TauNs, Azimuth: obs.Azimuth, AltitudeFt: c.ft})
			if !ok {
				t.Fatalf("解けない: stats %+v", l.Stats())
			}
			if d := math.Abs(fix.Position.Lat-c.lat) + math.Abs(fix.Position.Lon-c.lon); d > 2e-5 {
				t.Errorf("(%.6f, %.6f), 期待 (%.6f, %.6f)", fix.Position.Lat, fix.Position.Lon, c.lat, c.lon)
			}
		})
	}
}

// TestLocateRejectsUnsolvable は解けないプロットの扱いを固定する。
func TestLocateRejectsUnsolvable(t *testing.T) {
	l, gm := newLocator(t)
	// 双基地距離が基線長より短い（物理的にあり得ない）
	if _, ok := l.Locate(pssr.Plot{TauNs: config.TransponderDelayNs + 1000, Azimuth: 1, AltitudeFt: 5000}); ok {
		t.Error("基線より短い双基地距離が解けてしまう")
	}
	if s := l.Stats(); s.Inconsistent != 1 {
		t.Errorf("stats = %+v, 期待 Inconsistent=1", s)
	}
	// 覆域の外（MaxRange 400 km に対し 900 km 相当）
	if _, ok := l.Locate(pssr.Plot{TauNs: config.TransponderDelayNs + 6_000_000, Azimuth: 1, AltitudeFt: 5000}); ok {
		t.Error("覆域外の双基地距離が解けてしまう")
	}
	if s := l.Stats(); s.OutOfRange != 1 {
		t.Errorf("stats = %+v, 期待 OutOfRange=1", s)
	}
	// 基線上: SSR から局の方向へ 600 m、低高度。双基地角が 180 度に近く解が
	// 発散する。τ の 1 ns 量子化（0.3 m）でも幾何が両立しなくなるので、
	// 特異・曖昧・不整合のどれかで棄却される
	ac := geodesy.OrthometricLLA{Lat: 34.85418, Lon: 136.81616, Alt: pssr.HeightFromPressureAltitude(100)}
	obs, err := simtest.Observe(ssrLLA, stationLLA, ac, gm)
	if err != nil {
		t.Fatal(err)
	}
	before := l.Stats()
	if _, ok := l.Locate(pssr.Plot{TauNs: obs.TauNs, Azimuth: obs.Azimuth, AltitudeFt: 100}); ok {
		t.Error("基線上の機体が解けてしまう")
	}
	s := l.Stats()
	if rejected := s.Singular + s.Ambiguous + s.Inconsistent - before.Inconsistent; rejected != 1 || s.Fixes != 0 {
		t.Errorf("stats = %+v, 期待 棄却 1 件", s)
	}
	// 基線の少し横（500 m 東）に 300 ft なら特異点から外れ、解ける
	beside := geodesy.OrthometricLLA{Lat: 34.85418, Lon: 136.82160, Alt: pssr.HeightFromPressureAltitude(300)}
	obs, err = simtest.Observe(ssrLLA, stationLLA, beside, gm)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := l.Locate(pssr.Plot{TauNs: obs.TauNs, Azimuth: obs.Azimuth, AltitudeFt: 300}); !ok {
		t.Errorf("基線の横の機体が解けない: %+v", l.Stats())
	}
}

// TestCovariance は誤差の伝播の性質を固定する。
//
//	方位の誤差だけなら誤差は方位に直交する水平方向に ρ σ_θ
//	時刻の誤差だけなら誤差は方位方向で、遠距離では σ_L / 2（双基地角 ≈ 0）
func TestCovariance(t *testing.T) {
	gm, err := geoid.Load()
	if err != nil {
		t.Fatal(err)
	}
	geom, err := pssr.NewGeometry(ssrLLA, stationLLA, gm)
	if err != nil {
		t.Fatal(err)
	}
	// 真北 200 km、10000 ft
	ac := geodesy.OrthometricLLA{Lat: ssrLLA.Lat + 1.8, Lon: ssrLLA.Lon, Alt: pssr.HeightFromPressureAltitude(10000)}
	obs, err := simtest.Observe(ssrLLA, stationLLA, ac, gm)
	if err != nil {
		t.Fatal(err)
	}
	plot := pssr.Plot{TauNs: obs.TauNs, Azimuth: obs.Azimuth, AltitudeFt: 10000}

	azOnly := pssr.DefaultConfig()
	azOnly.SigmaTimingNs, azOnly.SigmaTransponderNs, azOnly.SigmaAltitudeM = 0, 0, 0
	var st pssr.Stats
	fix, ok := pssr.Locate(geom, &st, testParams, azOnly, plot)
	if !ok {
		t.Fatal("解けない")
	}
	rho := fix.Position.GroundRangeM
	want := rho * azOnly.SigmaAzimuthRad
	c := fix.Position.Cov
	// 真北なので方位の誤差は E 方向
	if d := math.Abs(math.Sqrt(c[0][0]) - want); d > want*1e-6 {
		t.Errorf("σ_E = %g, 期待 ρ σ_θ = %g", math.Sqrt(c[0][0]), want)
	}
	if math.Sqrt(c[1][1]) > 1e-3 || math.Sqrt(c[2][2]) > 1e-3 {
		t.Errorf("方位の誤差が N/U に漏れている: σ_N=%g σ_U=%g", math.Sqrt(c[1][1]), math.Sqrt(c[2][2]))
	}

	timeOnly := pssr.DefaultConfig()
	timeOnly.SigmaAzimuthRad, timeOnly.SigmaAltitudeM, timeOnly.SigmaTransponderNs = 0, 0, 0
	timeOnly.SigmaTimingNs = 100
	fix, _ = pssr.Locate(geom, &st, testParams, timeOnly, plot)
	c = fix.Position.Cov
	sigmaL := config.SpeedOfLightMPerNs * 100
	// 遠距離では ∂ρ/∂L ≈ 1/2（cos ε1 + cos ξ2 ≈ 2）
	if got := math.Sqrt(c[1][1]); math.Abs(got-sigmaL/2) > sigmaL*0.01 {
		t.Errorf("σ_N = %g, 期待 σ_L/2 = %g", got, sigmaL/2)
	}
	if math.Sqrt(c[0][0]) > 1e-3 {
		t.Errorf("時刻の誤差が E に漏れている: σ_E=%g", math.Sqrt(c[0][0]))
	}
}

// TestHeightFromPressureAltitude は単位換算だけであることを固定する。
// QNH 補正を足すときはここが変わる。
func TestHeightFromPressureAltitude(t *testing.T) {
	if got := pssr.HeightFromPressureAltitude(10000); got != 3048 {
		t.Errorf("10000 ft -> %g m, 期待 3048", got)
	}
}
