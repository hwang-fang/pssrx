package store

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"pssrx/internal/nanotime"
)

// TestWaveheight は dBm と生値の対応を固定する。0 dBm が 0xFFFF で、
// 1/256 dB 刻みに小さくなる。
func TestWaveheight(t *testing.T) {
	enc := []struct {
		dbm  float64
		want uint16
	}{
		{-35.0, 56575}, {-0.0, 65535}, {-255.0, 255},
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
		{56575, -35.0}, {60030, -21.50390625}, {65535, -0.0},
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

func TestQpkxPathLayout(t *testing.T) {
	r := &QdataRepository{Root: "/data"}
	dt := time.Date(2026, 6, 10, 3, 47, 0, 0, nanotime.JST)
	want := "/data/202606/KX90/20260610/qpkx/202606100347KX90.qpkx"
	if got := r.filePath("KX90", dt); got != want {
		t.Errorf("qpkx パス = %s, 期待 %s", got, want)
	}
}

func TestIntgPathLayout(t *testing.T) {
	r := &IntgRepository{Root: "/out"}
	ts := time.Date(2026, 7, 13, 11, 59, 0, 0, nanotime.JST).UnixNano()
	want := "/out/202607/NGOS1/20260713/202607131159NGOS1.intg"
	if got := r.filePath("NGOS1", ts); got != want {
		t.Errorf("intg パス = %s, 期待 %s", got, want)
	}
}

func TestIntgRoundTrip(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 6, 10, 0, 47, 0, 0, nanotime.JST).UnixNano()
	in := []Intg{
		{Timestamp: base + 1_234_500, Azimuth: 0, Mode: 3},
		{Timestamp: base + 2_000_000, Azimuth: math.Pi, Mode: 5},
		{Timestamp: base + 59_999_999_900, Azimuth: 2*math.Pi - 1e-9, Mode: 5},
	}
	r := &IntgRepository{Root: dir}
	if err := r.Save("XX01", in); err != nil {
		t.Fatal(err)
	}
	path := r.filePath("XX01", base)
	out, err := ReadIntg(path, base)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(in) {
		t.Fatalf("レコード数 %d, 期待 %d", len(out), len(in))
	}
	for i := range in {
		if out[i].Timestamp != in[i].Timestamp || out[i].Mode != in[i].Mode {
			t.Errorf("[%d] ts/mode が復元できない: %+v -> %+v", i, in[i], out[i])
		}
		// 方位角は u32 に量子化されるので 1 LSB 以内に戻ればよい
		if d := math.Abs(out[i].Azimuth - in[i].Azimuth); d > 2*math.Pi/0xFFFFFFFF {
			t.Errorf("[%d] 方位角の復元誤差 %g rad が 1 LSB を超える", i, d)
		}
	}
}

// TestSaveTruncatesOncePerProcess は「プロセス内では追記、流し直しでは
// 切り詰め」という書き分けを固定する。前者は遅延補正で前の分へまたがった
// レコードを落とさないため、後者は再実行でレコードが二重にならないため。
func TestSaveTruncatesOncePerProcess(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 6, 10, 0, 47, 0, 0, nanotime.JST).UnixNano()
	rec := []Intg{{Timestamp: base + 1000, Azimuth: 1.0, Mode: 3}}

	r1 := &IntgRepository{Root: dir}
	for range 3 {
		if err := r1.Save("XX01", rec); err != nil {
			t.Fatal(err)
		}
	}
	path := r1.filePath("XX01", base)
	if size := fileSize(t, path); size != 3*intgRecordSize {
		t.Errorf("同一プロセス内 3 回保存後 %d byte, 期待 %d byte（追記されるべき）",
			size, 3*intgRecordSize)
	}

	r2 := &IntgRepository{Root: dir} // 流し直し（新しいプロセス相当）
	if err := r2.Save("XX01", rec); err != nil {
		t.Fatal(err)
	}
	if size := fileSize(t, path); size != intgRecordSize {
		t.Errorf("再実行後 %d byte, 期待 %d byte（切り詰められるべき）", size, intgRecordSize)
	}

	r3 := &IntgRepository{Root: dir, Append: true} // 切り詰めを抑止した場合
	if err := r3.Save("XX01", rec); err != nil {
		t.Fatal(err)
	}
	if size := fileSize(t, path); size != 2*intgRecordSize {
		t.Errorf("Append 指定時 %d byte, 期待 %d byte", size, 2*intgRecordSize)
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

func TestFetchSortsInvertedInput(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 6, 10, 0, 0, 0, 0, nanotime.JST)
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

	st, ed := base.UnixNano(), base.Add(time.Minute).UnixNano()
	unsorted, err := (&QdataRepository{Root: dir}).Fetch("ZZ01", st, ed)
	if err != nil {
		t.Fatal(err)
	}
	if unsorted[0].Timestamp <= unsorted[1].Timestamp {
		t.Error("SortInput=false で逆行が保たれていない（テストデータが不正）")
	}
	sorted, err := (&QdataRepository{Root: dir, SortInput: true}).Fetch("ZZ01", st, ed)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(sorted); i++ {
		if sorted[i].Timestamp > sorted[i+1].Timestamp {
			t.Fatalf("SortInput=true でも昇順になっていない: [%d]=%d > [%d]=%d",
				i, sorted[i].Timestamp, i+1, sorted[i+1].Timestamp)
		}
	}
}

func fileSize(t *testing.T, p string) int64 {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}
