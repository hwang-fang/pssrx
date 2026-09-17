package store

import (
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
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

func TestQpkxPathLayout(t *testing.T) {
	r := &QpkxDir{Root: "/data"}
	dt := time.Date(2026, 6, 10, 3, 47, 0, 0, JST)
	want := "/data/202606/KX90/20260610/qpkx/202606100347KX90.qpkx"
	if got := r.filePath("KX90", dt); got != want {
		t.Errorf("qpkx パス = %s, 期待 %s", got, want)
	}
}

func TestIntgPathLayout(t *testing.T) {
	r := &IntgDir{Root: "/out"}
	ts := time.Date(2026, 7, 13, 11, 59, 0, 0, JST).UnixNano()
	want := "/out/202607/NGOS1/20260713/202607131159NGOS1.intg"
	if got := r.filePath("NGOS1", ts); got != want {
		t.Errorf("intg パス = %s, 期待 %s", got, want)
	}
}

func TestIntgRoundTrip(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 6, 10, 0, 47, 0, 0, JST).UnixNano()
	in := []Intg{
		{Timestamp: base + 1_234_500, Azimuth: 0, Mode: 3},
		{Timestamp: base + 2_000_000, Azimuth: math.Pi, Mode: 5},
		{Timestamp: base + 59_999_999_900, Azimuth: 2*math.Pi - 1e-9, Mode: 5},
	}
	r := &IntgDir{Root: dir}
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
	base := time.Date(2026, 6, 10, 0, 47, 0, 0, JST).UnixNano()
	rec := []Intg{{Timestamp: base + 1000, Azimuth: 1.0, Mode: 3}}

	r1 := &IntgDir{Root: dir}
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

	r2 := &IntgDir{Root: dir} // 流し直し（新しいプロセス相当）
	if err := r2.Save("XX01", rec); err != nil {
		t.Fatal(err)
	}
	if size := fileSize(t, path); size != intgRecordSize {
		t.Errorf("再実行後 %d byte, 期待 %d byte（切り詰められるべき）", size, intgRecordSize)
	}

	r3 := &IntgDir{Root: dir, Append: true} // 切り詰めを抑止した場合
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

func TestReadMinuteSortsInvertedInput(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 6, 10, 0, 0, 0, 0, JST)
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

func fileSize(t *testing.T, p string) int64 {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}

// TestSaveStateIsBoundedByStationCount は、いくら書き続けても内部の保持量が
// 増えないことを確認する。
//
// 常駐させたときにここが青天井だと、書いたファイル名を溜め込んで
// 1 分あたり約 100 byte、1 年で 60 MB 近く消費してしまう。
func TestSaveStateIsBoundedByStationCount(t *testing.T) {
	dir := t.TempDir()
	r := &IntgDir{Root: dir, Log: discardLogger()}
	base := time.Date(2026, 6, 10, 0, 0, 0, 0, JST).UnixNano()

	// 3 日ぶん（4320 分）を 2 つの SSR について時系列に流す
	for i := range 3 * 24 * 60 {
		ts := base + int64(i)*OneMinute
		for _, ssr := range []string{"AA01", "BB02"} {
			if err := r.Save(ssr, []Intg{{Timestamp: ts, Azimuth: 1.0, Mode: 3}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if got := len(r.lastMinute); got != 2 {
		t.Errorf("内部の保持件数 %d, 期待 2（SSR の数）。分の数だけ増えている", got)
	}
}

// TestSaveTracksHighWaterMarkPerSSR は、SSR ごとに独立して
// 到達済みの分を数えていることを確認する。
func TestSaveTracksHighWaterMarkPerSSR(t *testing.T) {
	dir := t.TempDir()
	r := &IntgDir{Root: dir, Log: discardLogger()}
	base := time.Date(2026, 6, 10, 0, 0, 0, 0, JST).UnixNano()
	rec := func(ts int64) []Intg { return []Intg{{Timestamp: ts, Azimuth: 1.0, Mode: 3}} }

	// AA01 を 10 分先まで進めてから BB02 を 0 分に書いても、
	// BB02 の 0 分は「初めて到達した分」として切り詰められる
	for i := range 10 {
		if err := r.Save("AA01", rec(base+int64(i)*OneMinute)); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Save("BB02", rec(base)); err != nil {
		t.Fatal(err)
	}
	if err := r.Save("BB02", rec(base)); err != nil {
		t.Fatal(err)
	}
	// 同じ分への 2 度目は追記になる
	if got := fileSize(t, r.filePath("BB02", base)); got != 2*intgRecordSize {
		t.Errorf("BB02 の 0 分 = %d byte, 期待 %d byte", got, 2*intgRecordSize)
	}
	if got := fileSize(t, r.filePath("AA01", base)); got != intgRecordSize {
		t.Errorf("AA01 の 0 分 = %d byte, 期待 %d byte", got, intgRecordSize)
	}
}

// TestSaveSpanningTwoMinutes は、伝搬遅延の補正でレコードが前の分へ
// またがる実際の並びを再現する。分 M は Save(M) と Save(M+1) の
// 2 回書かれるが、切り詰められるのは初めて到達したときだけ。
func TestSaveSpanningTwoMinutes(t *testing.T) {
	dir := t.TempDir()
	r := &IntgDir{Root: dir, Log: discardLogger()}
	base := time.Date(2026, 6, 10, 0, 0, 0, 0, JST).UnixNano()

	// Save(M) は [M-1 の末尾, M] を、Save(M+1) は [M の末尾, M+1] を書く
	for i := range 5 {
		cur := base + int64(i)*OneMinute
		recs := []Intg{
			{Timestamp: cur - 1000, Azimuth: 1.0, Mode: 3}, // 前の分へこぼれる
			{Timestamp: cur + 1000, Azimuth: 1.0, Mode: 5},
		}
		if err := r.Save("AA01", recs); err != nil {
			t.Fatal(err)
		}
	}
	// 分 0..3 は「自分の Save で 1 件」＋「次の Save のこぼれで 1 件」= 2 件
	for i := range 4 {
		p := r.filePath("AA01", base+int64(i)*OneMinute)
		if got := fileSize(t, p); got != 2*intgRecordSize {
			t.Errorf("分 %d = %d byte, 期待 %d byte（自分の 1 件 + 次のこぼれ 1 件）",
				i, got, 2*intgRecordSize)
		}
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
