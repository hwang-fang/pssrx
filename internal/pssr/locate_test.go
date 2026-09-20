package pssr_test

import (
	"math"
	"testing"

	"pssrx/internal/geodesy"
	"pssrx/internal/geodesy/geoid"
	"pssrx/internal/pssr"
	"pssrx/internal/pssr/simtest"
	"pssrx/internal/ssr"
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

// TestLocateNearBaseline は基線長（1.3 km）程度に近い機体も閉形式で解けることを
// 確認する。空港近傍の離着陸機がここに入る。基線余裕（L > B + 500 m）と
// 一意性の余裕（|z| < ℓ − 200 m）の外側の点を選ぶ。
func TestLocateNearBaseline(t *testing.T) {
	l, gm := newLocator(t)
	cases := []struct {
		name     string
		lat, lon float64
		ft       int
	}{
		{"局の 1.5 km 東 1000 ft", 34.85837, 136.82716, 1000},
		{"SSR から 2 km 北 3000 ft", 34.8686, 136.82094, 3000},
		{"SSR から 2 km 南西 2000 ft", 34.8378, 136.8055, 2000},
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
	// 双基地距離が基線の水平成分 + 余裕より短い（基線特異点）
	if _, ok := l.Locate(pssr.Plot{TauNs: ssr.TransponderDelayNs + 1000, Azimuth: 1, AltitudeFt: 5000}); ok {
		t.Error("基線より短い双基地距離が解けてしまう")
	}
	if s := l.Stats(); s.Baseline != 1 {
		t.Errorf("stats = %+v, 期待 Baseline=1", s)
	}
	// 双基地距離が 0 以下（τ が応答遅延より短い）
	if _, ok := l.Locate(pssr.Plot{TauNs: ssr.TransponderDelayNs - 1, Azimuth: 1, AltitudeFt: 5000}); ok {
		t.Error("負の双基地距離が解けてしまう")
	}
	if s := l.Stats(); s.Inconsistent != 1 {
		t.Errorf("stats = %+v, 期待 Inconsistent=1", s)
	}
	// SSR 直上: |z| = ℓ が厳密に成り立つ境界で、余裕の内側なので棄却される
	over := geodesy.OrthometricLLA{Lat: ssrLLA.Lat, Lon: ssrLLA.Lon, Alt: pssr.HeightFromPressureAltitude(10000)}
	obs, err := simtest.Observe(ssrLLA, stationLLA, over, gm)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := l.Locate(pssr.Plot{TauNs: obs.TauNs, Azimuth: obs.Azimuth, AltitudeFt: 10000}); ok {
		t.Error("SSR 直上の機体が解けてしまう")
	}
	if s := l.Stats(); s.Ambiguous+s.NoSolution != 1 {
		t.Errorf("stats = %+v, 期待 Ambiguous か NoSolution が 1", s)
	}
	// 局の真上は U_r ≈ 0 なら ℓ = z が厳密に成り立ち（SSR 直上と同じ L・同じ
	// 射線で区別できない）、曖昧として棄却される
	overStation := geodesy.OrthometricLLA{Lat: stationLLA.Lat, Lon: stationLLA.Lon, Alt: pssr.HeightFromPressureAltitude(8000)}
	obs, err = simtest.Observe(ssrLLA, stationLLA, overStation, gm)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := l.Locate(pssr.Plot{TauNs: obs.TauNs, Azimuth: obs.Azimuth, AltitudeFt: 8000}); ok {
		t.Error("局の真上の機体が解けてしまう")
	}
	if s := l.Stats(); s.Ambiguous+s.NoSolution != 2 {
		t.Errorf("stats = %+v, 期待 Ambiguous + NoSolution = 2", s)
	}
	// 局の 1.5 km 東・1000 ft は余裕の外側で解ける
	beside := geodesy.OrthometricLLA{Lat: 34.85837, Lon: 136.82716, Alt: pssr.HeightFromPressureAltitude(1000)}
	obs, err = simtest.Observe(ssrLLA, stationLLA, beside, gm)
	if err != nil {
		t.Fatal(err)
	}
	fix, ok := l.Locate(pssr.Plot{TauNs: obs.TauNs, Azimuth: obs.Azimuth, AltitudeFt: 1000})
	if !ok {
		t.Fatalf("基線の横の機体が解けない: %+v", l.Stats())
	}
	if fix.Position.ResidualM > 1e-6*fix.Position.RangeSSRM || fix.Position.Iterations > 3 {
		t.Errorf("残差 %g m, 反復 %d", fix.Position.ResidualM, fix.Position.Iterations)
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
	// 応答列は完全（DwellFullReplies 以上）として σ_θ の基準値を使う
	plot := pssr.Plot{TauNs: obs.TauNs, Azimuth: obs.Azimuth, AltitudeFt: 10000, Replies: make([]pssr.PairedReply, 20)}

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
	sigmaL := ssr.SpeedOfLightMPerNs * 100
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

// TestAzimuthSigmaGrowsWithMissingReplies は応答列の欠けに応じて方位の標準
// 偏差が増え、完全な列では基準値のままであることを確認する。
func TestAzimuthSigmaGrowsWithMissingReplies(t *testing.T) {
	gm, err := geoid.Load()
	if err != nil {
		t.Fatal(err)
	}
	ssrPos := geodesy.OrthometricLLA{Lat: 34.85, Lon: 136.82, Alt: 0}
	stPos := geodesy.OrthometricLLA{Lat: 34.86, Lon: 136.81, Alt: 0}
	geom, err := pssr.NewGeometry(ssrPos, stPos, gm)
	if err != nil {
		t.Fatal(err)
	}
	obs, err := simtest.Observe(ssrPos, stPos, geodesy.OrthometricLLA{Lat: 35.3, Lon: 136.82, Alt: 3048}, gm)
	if err != nil {
		t.Fatal(err)
	}
	cfg := pssr.DefaultConfig()
	cfg.SigmaTimingNs, cfg.SigmaTransponderNs, cfg.SigmaAltitudeM = 0, 0, 0
	sigmaE := func(n int) float64 {
		var st pssr.Stats
		fix, ok := pssr.Locate(geom, &st, testParams, cfg, pssr.Plot{TauNs: obs.TauNs, Azimuth: obs.Azimuth, AltitudeFt: 10000, Replies: make([]pssr.PairedReply, n)})
		if !ok {
			t.Fatal("解けない")
		}
		return math.Sqrt(fix.Position.Cov[0][0]) / fix.Position.GroundRangeM
	}
	full, more := sigmaE(cfg.DwellFullReplies), sigmaE(cfg.DwellFullReplies+5)
	if math.Abs(full-cfg.SigmaAzimuthRad) > 1e-9 || math.Abs(more-cfg.SigmaAzimuthRad) > 1e-9 {
		t.Errorf("完全な列の σ_θ = %g / %g, 期待 %g", full, more, cfg.SigmaAzimuthRad)
	}
	perInterrogation := 2 * math.Pi * testParams.MeanPRINs / float64(testParams.AroundTimeNs)
	want := cfg.SigmaAzimuthRad + cfg.AzimuthFragmentFactor*float64(cfg.DwellFullReplies-3)*perInterrogation
	if got := sigmaE(3); math.Abs(got-want) > 1e-9 {
		t.Errorf("3 応答の σ_θ = %g, 期待 %g", got, want)
	}
	if sigmaE(8) <= sigmaE(11) || sigmaE(11) <= sigmaE(13) {
		t.Error("σ_θ が欠けに対して単調に増えていない")
	}
}
