package pipeline_test

import (
	"math"
	"path/filepath"
	"testing"

	"pssrx/internal/config"
	"pssrx/internal/pipeline"
)

// TestPSSRParams は設定から対応づけの窓が定義どおりに導かれることを確認する。
//
//	TauMin = 3 µs + d / c, TauMax = 3 µs + (2 R_max + d) / c
//
// d は testdata/kx90.yaml の SSR–局間 1275.0539493951226 m。
func TestPSSRParams(t *testing.T) {
	cfg, err := config.Load(filepath.Join("..", "..", "testdata", "kx90.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	ssr, _ := cfg.SSR("KX90S")
	st, _ := cfg.Station("KX90")
	p, err := pipeline.NewPSSRParams(ssr, st, pipeline.DefaultPSSRConfig())
	if err != nil {
		t.Fatal(err)
	}
	const c = 0.299792458
	if want := 3000 + int64(math.Ceil(1275.0539493951226/c)); p.Plot.TauMinNs != want {
		t.Errorf("TauMin = %d, 期待 %d", p.Plot.TauMinNs, want)
	}
	if want := 3000 + int64(math.Ceil((2*400000+1275.0539493951226)/c)); p.Plot.TauMaxNs != want {
		t.Errorf("TauMax = %d, 期待 %d", p.Plot.TauMaxNs, want)
	}
	if p.Plot.TauMaxNs >= 2_949_900 {
		t.Errorf("TauMax %d が PRI 以上", p.Plot.TauMaxNs)
	}
	if p.SSRID != "KX90S" || p.StationID != "KX90" {
		t.Errorf("ID = (%s, %s)", p.SSRID, p.StationID)
	}

	// 覆域が PRI に対して広すぎれば拒否する
	ssr.MaxRangeM = 450_000 // 2·450 km / c ≈ 3.0 ms > PRI 2.95 ms
	if _, err := pipeline.NewPSSRParams(ssr, st, pipeline.DefaultPSSRConfig()); err == nil {
		t.Error("TauMax が PRI を超える覆域がエラーにならない")
	}
}
