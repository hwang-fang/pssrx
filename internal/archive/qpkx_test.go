package archive

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"pssrx/internal/record"
)

func TestQpkxPathLayout(t *testing.T) {
	r := &QpkxDir{Root: "/data"}
	dt := time.Date(2026, 6, 10, 3, 47, 0, 0, record.JST)
	want := "/data/202606/KX90/20260610/qpkx/202606100347KX90.qpkx"
	if got := r.filePath("KX90", dt); got != want {
		t.Errorf("qpkx パス = %s, 期待 %s", got, want)
	}
}

func TestReadQpkxRejectsBadSize(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.qpkx")
	if err := os.WriteFile(p, make([]byte, 10), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readQpkx(p, 0); err == nil {
		t.Error("7 で割り切れないサイズがエラーにならない")
	}
}

func TestReadMinuteSortsInvertedInput(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 6, 10, 0, 0, 0, 0, record.JST)
	p := filepath.Join(dir, "202606", "ZZ01", "20260610", "qpkx", "202606100000ZZ01.qpkx")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	// 実データに現れるのと同じ形の逆行（先頭に後ろの時刻が来る）を作る
	raw := []byte{}
	for _, tick := range []uint32{5_000_000, 100, 200, 300} {
		raw = append(raw,
			byte(tick), byte(tick>>8), byte(tick>>16), byte(tick>>24),
			3, 0xFF, 0xFF)
	}
	if err := os.WriteFile(p, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	unsorted, err := DecodeQpkx(raw, base.UnixNano())
	if err != nil {
		t.Fatal(err)
	}
	if unsorted[0].Timestamp <= unsorted[1].Timestamp {
		t.Error("ファイル上の逆行が保たれていない（テストデータが不正）")
	}
	sorted, err := (&QpkxDir{Root: dir}).ReadMinute("ZZ01", base)
	if err != nil {
		t.Fatal(err)
	}
	if len(sorted) != 4 {
		t.Fatalf("件数 %d, 期待 4", len(sorted))
	}
	for i := 0; i+1 < len(sorted); i++ {
		if sorted[i].Timestamp > sorted[i+1].Timestamp {
			t.Fatalf("昇順になっていない: [%d]=%d > [%d]=%d",
				i, sorted[i].Timestamp, i+1, sorted[i+1].Timestamp)
		}
	}
}
