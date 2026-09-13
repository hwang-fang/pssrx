package pssr_test

import (
	"math"
	"testing"

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
	cfg := pssr.DefaultConfig()
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
			obs, err := simtest.Observe(ssrLLA, stationLLA, ac, gm, cfg.TransponderDelayNs)
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

// TestLocateRejectsUnsolvable は解けないプロットの扱いを固定する。
func TestLocateRejectsUnsolvable(t *testing.T) {
	l, _ := newLocator(t)
	cfg := pssr.DefaultConfig()
	// 双基地和が基線長より短い（物理的にあり得ない）
	if _, ok := l.Locate(pssr.Plot{TauNs: cfg.TransponderDelayNs + 1000, Azimuth: 1, AltitudeFt: 5000}); ok {
		t.Error("基線より短い双基地和が解けてしまう")
	}
	// 覆域の外
	if _, ok := l.Locate(pssr.Plot{TauNs: cfg.TransponderDelayNs + 3_000_000, Azimuth: 1, AltitudeFt: 5000}); ok {
		t.Error("覆域外の双基地和が解けてしまう")
	}
	if s := l.Stats(); s.TooClose != 1 || s.OutOfRange != 1 || s.Fixes != 0 {
		t.Errorf("stats = %+v", s)
	}
}

// TestHeightFromPressureAltitude は単位換算だけであることを固定する。
// QNH 補正を足すときはここが変わる。
func TestHeightFromPressureAltitude(t *testing.T) {
	if got := pssr.HeightFromPressureAltitude(10000); got != 3048 {
		t.Errorf("10000 ft -> %g m, 期待 3048", got)
	}
}
