package truth

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"pssrx/internal/geodesy"
	"pssrx/internal/geodesy/geoid"
)

// TestReadAndAt は真値 CSV を機体ごとに読み、間隔が maxGap 以内なら内挿し、
// 超えれば位置を返さないこと、地上の行（airborne=0）を読まないことを確認する。
func TestReadAndAt(t *testing.T) {
	body := `time_jst,icao,squawk,lat,lon,pressure_alt_ft,nic,airborne
2026-06-10T10:00:00,ABC123,2216,34.90,136.82,10000,8,1
2026-06-10T10:00:04,abc123,2216,34.91,136.82,10100,7,1
2026-06-10T10:00:30,ABC123,2216,34.95,136.82,10500,8,1
2026-06-10T10:00:01,DEF456,1200,34.80,136.82,,8,0
`
	p := filepath.Join(t.TempDir(), "truth.csv")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	gm, err := geoid.Load()
	if err != nil {
		t.Fatal(err)
	}
	conv, err := geodesy.NewENUConverter(geodesy.OrthometricLLA{Lat: 34.85, Lon: 136.82, Alt: 0}, gm)
	if err != nil {
		t.Fatal(err)
	}
	ac, err := Read(p, conv)
	if err != nil {
		t.Fatal(err)
	}
	if len(ac) != 1 || ac[0].ICAO != "abc123" || len(ac[0].Samples) != 3 {
		t.Fatalf("機体 %+v, 期待 abc123 の 3 行だけ（地上の行は読まない、ICAO は小文字にそろえる）", ac)
	}
	a := ac[0]
	t0 := a.Samples[0].T
	s, ok := a.At(t0+2_000_000_000, 10_000_000_000)
	if !ok {
		t.Fatal("間隔 4 s の間で内挿できない")
	}
	mid := (a.Samples[0].N + a.Samples[1].N) / 2
	if math.Abs(s.N-mid) > 1e-6 || s.AltFt != 10050 || s.NIC != 7 || s.Squawk != "2216" {
		t.Errorf("内挿 %+v, 期待 N=%g、高度 10050、NIC は小さい方の 7", s, mid)
	}
	if _, ok := a.At(t0+10_000_000_000, 10_000_000_000); ok {
		t.Error("間隔 26 s（maxGap 10 s 超）の間で位置を返した")
	}
}
