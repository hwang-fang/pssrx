package record

import (
	"math"
	"testing"
)

// TestWaveheight は dBm と生値の対応を固定する。0 dBm が 0xFFFF で、
// 1/256 dB 刻みに小さくなる。
func TestWaveheight(t *testing.T) {
	enc := []struct {
		dbm  float64
		want uint16
	}{
		{-35.0, 56575}, {0.0, 65535}, {-255.0, 255},
		{-12.34, 62376}, {-1.0 / 256, 65534}, {-100.5, 39807},
	}
	for _, c := range enc {
		got, err := EncodeWaveheight(c.dbm)
		if err != nil {
			t.Errorf("EncodeWaveheight(%v): %v", c.dbm, err)
			continue
		}
		if got != c.want {
			t.Errorf("EncodeWaveheight(%v) = %d, 期待 %d", c.dbm, got, c.want)
		}
	}
	dec := []struct {
		raw  uint16
		want float64
	}{
		{0, -255.99609375}, {1, -255.9921875}, {255, -255.0},
		// 0xFFFF は 0 dBm。実装は -0.0 を返すが、後段の演算で符号付きゼロの
		// 区別は効かないので値としては 0 と等しければよい。
		{56575, -35.0}, {60030, -21.50390625}, {65535, 0.0},
	}
	for _, c := range dec {
		if got := DecodeWaveheight(c.raw); got != c.want {
			t.Errorf("DecodeWaveheight(%d) = %v, 期待 %v", c.raw, got, c.want)
		}
	}
	for _, bad := range []float64{0.1, -256.0, math.NaN()} {
		if _, err := EncodeWaveheight(bad); err == nil {
			t.Errorf("EncodeWaveheight(%v) がエラーにならない", bad)
		}
	}
}
