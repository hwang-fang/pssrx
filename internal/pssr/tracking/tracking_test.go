package tracking_test

import (
	"testing"

	"pssrx/internal/geodesy"
	"pssrx/internal/geodesy/geoid"
	"pssrx/internal/pssr/tracking"
)

const (
	start    = int64(1_781_000_000_000_000_000)
	aroundNs = int64(4_040_000_000)
)

var testParams = tracking.Params{AroundTimeNs: aroundNs}

// testConverter は平滑化のテスト用の ENU 変換（テスト用の SSR 位置を原点に）。
func testConverter(t *testing.T) *geodesy.ENUConverter {
	t.Helper()
	gm, err := geoid.Load()
	if err != nil {
		t.Fatal(err)
	}
	conv, err := geodesy.NewENUConverter(geodesy.OrthometricLLA{Lat: 34.85058333, Lon: 136.82093888, Alt: 0}, gm)
	if err != nil {
		t.Fatal(err)
	}
	return conv
}
