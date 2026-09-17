package store

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"pssrx/internal/config"
)

func writeApkx(t *testing.T, r *AdataRepository, station string, dt time.Time, data []AData) {
	t.Helper()
	p := r.filePath(station, dt)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, EncodeApkx(data, dt.UnixNano()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestApkxPathLayout(t *testing.T) {
	r := &AdataRepository{Root: "/root"}
	got := r.filePath("KX90", time.Date(2026, 6, 10, 0, 47, 0, 0, JST))
	want := filepath.Join("/root", "202606", "KX90", "20260610", "apkx", "202606100047KX90.apkx")
	if got != want {
		t.Errorf("path = %s, 期待 %s", got, want)
	}
}

// TestApkxRoundTrip は 8 バイトレコードの読み書きが往復することを確認する。
// 実データで観測した値の桁（100 ns 単位のオフセット、12 ビット符号、
// 0xFFFF 基準の波高値）をそのまま使う。
func TestApkxRoundTrip(t *testing.T) {
	base := time.Date(2026, 6, 10, 0, 0, 0, 0, JST).UnixNano()
	in := []AData{
		{Timestamp: base + 2545*100, Code: 0o325, WH: 44859},
		{Timestamp: base + 12768*100, Code: 0o1432, WH: 46430},
		{Timestamp: base + 599937443*100, Code: 0o7777, WH: 0xFFFF},
	}
	raw := EncodeApkx(in, base)
	// ファイル上の時刻は F2 なので、F1 より F1–F2 間隔だけ後ろ
	if got := int64(binary.LittleEndian.Uint32(raw[0:4])) * tsResolution; got != 2545*100+config.ReplyFrameLengthNs {
		t.Errorf("ファイル上の時刻 %d, 期待 F1 + %d", got, config.ReplyFrameLengthNs)
	}
	out, err := DecodeApkx(raw, base)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(in) {
		t.Fatalf("件数 %d, 期待 %d", len(out), len(in))
	}
	for i := range in {
		if out[i] != in[i] {
			t.Errorf("[%d] %+v -> %+v", i, in[i], out[i])
		}
	}
	if _, err := DecodeApkx(make([]byte, 12), base); err == nil {
		t.Error("8 で割り切れないサイズがエラーにならない")
	}
}

// TestAdataFetch は分をまたぐ範囲の取得、範囲外の除外、欠けた分の読み飛ばし、
// 逆行した入力の安定ソートを確認する。
func TestAdataFetch(t *testing.T) {
	r := &AdataRepository{Root: t.TempDir(), SortInput: true}
	m0 := time.Date(2026, 6, 10, 0, 47, 0, 0, JST)
	m1 := m0.Add(time.Minute)
	m2 := m1.Add(time.Minute) // ファイルを置かない分
	// m0 は逆行を含む（先頭に遅い時刻）
	writeApkx(t, r, "KX90", m0, []AData{
		{Timestamp: m0.UnixNano() + 30_000_000_000, Code: 3},
		{Timestamp: m0.UnixNano() + 10_000_000_000, Code: 1},
		{Timestamp: m0.UnixNano() + 10_000_000_000, Code: 2}, // 同時刻。安定ソートで順序維持
	})
	writeApkx(t, r, "KX90", m1, []AData{
		{Timestamp: m1.UnixNano() + 5_000_000_000, Code: 4},
		{Timestamp: m1.UnixNano() + 50_000_000_000, Code: 5},
	})

	got, err := r.Fetch("KX90", m0.UnixNano()+10_000_000_000, m2.Add(time.Minute).UnixNano())
	if err != nil {
		t.Fatal(err)
	}
	want := []uint16{1, 2, 3, 4, 5}
	if len(got) != len(want) {
		t.Fatalf("件数 %d, 期待 %d: %+v", len(got), len(want), got)
	}
	for i, c := range want {
		if got[i].Code != c {
			t.Errorf("[%d] code = %d, 期待 %d", i, got[i].Code, c)
		}
	}
	// 上限は含まない
	got, err = r.Fetch("KX90", m0.UnixNano(), m1.UnixNano()+5_000_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Errorf("[start, end) の end ちょうどのレコードが含まれている: %d 件", len(got))
	}
}

// TestAdataFetchMinuteBoundary は分の先頭 20.3 µs にある応答（F2 時刻では
// その分、F1 時刻では前の分）を前の分の範囲で取りこぼさないことを確認する。
func TestAdataFetchMinuteBoundary(t *testing.T) {
	r := &AdataRepository{Root: t.TempDir(), SortInput: true}
	m0 := time.Date(2026, 6, 10, 0, 47, 0, 0, JST)
	m1 := m0.Add(time.Minute)
	// F1 が m1 の 10 µs 前 → F2 は m1 の 10.3 µs 後で、ファイルは m1 の分
	edge := AData{Timestamp: m1.UnixNano() - 10_000, Code: 7}
	writeApkx(t, r, "KX90", m1, []AData{edge, {Timestamp: m1.UnixNano() + 5_000_000_000, Code: 8}})

	got, err := r.Fetch("KX90", m0.UnixNano(), m1.UnixNano())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Code != 7 || got[0].Timestamp != edge.Timestamp {
		t.Errorf("前の分の範囲で境界の応答を取りこぼした: %+v", got)
	}
	got, err = r.Fetch("KX90", m1.UnixNano(), m1.Add(time.Minute).UnixNano())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Code != 8 {
		t.Errorf("次の分の範囲に境界の応答が混ざった: %+v", got)
	}
}

// TestIntgFetchReadsBackSave は Save が書いたものを Fetch が同じ順で読み戻す
// ことを確認する。前の分へこぼれたレコードも、その分のファイルから拾う。
func TestIntgFetchReadsBackSave(t *testing.T) {
	r := &IntgRepository{Root: t.TempDir(), Log: discardLogger()}
	cur := time.Date(2026, 6, 10, 0, 48, 0, 0, JST).UnixNano()
	in := []Intg{
		{Timestamp: cur - 1000, Azimuth: 1.0, Mode: 3}, // 前の分へこぼれる
		{Timestamp: cur + 1000, Azimuth: 2.0, Mode: 5},
		{Timestamp: cur + OneMinute + 700, Azimuth: 3.0, Mode: 3},
	}
	if err := r.Save("KX90S", in); err != nil {
		t.Fatal(err)
	}
	got, err := r.Fetch("KX90S", cur-OneMinute, cur+2*OneMinute)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("件数 %d, 期待 3", len(got))
	}
	for i := range in {
		if got[i].Timestamp != in[i].Timestamp || got[i].Mode != in[i].Mode {
			t.Errorf("[%d] %+v -> %+v", i, in[i], got[i])
		}
	}
	got, err = r.Fetch("KX90S", cur, cur+OneMinute)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Timestamp != cur+1000 {
		t.Errorf("範囲の絞り込みが効いていない: %+v", got)
	}
}

// TestQuantizeIntgMatchesFileRoundTrip はメモリ渡し用の量子化が、ファイルに
// 書いて読み戻した値とビット単位で一致することを確認する。
func TestQuantizeIntgMatchesFileRoundTrip(t *testing.T) {
	r := &IntgRepository{Root: t.TempDir(), Log: discardLogger()}
	cur := time.Date(2026, 6, 10, 0, 48, 0, 0, JST).UnixNano()
	in := []Intg{ // Save は時刻順に書くので、ここも時刻順
		{Timestamp: cur + 99, Azimuth: 2*math.Pi - 1e-9, Mode: 5},
		{Timestamp: cur + 12345, Azimuth: 0.123456789, Mode: 3},
		{Timestamp: cur + OneMinute - 1, Azimuth: 3.3, Mode: 3},
	}
	if err := r.Save("S", in); err != nil {
		t.Fatal(err)
	}
	fromFile, err := r.Fetch("S", cur, cur+OneMinute)
	if err != nil {
		t.Fatal(err)
	}
	q := QuantizeIntg(in)
	if len(fromFile) != len(q) {
		t.Fatalf("件数 %d != %d", len(fromFile), len(q))
	}
	for i := range q {
		if q[i] != fromFile[i] {
			t.Errorf("[%d] 量子化 %+v != ファイル %+v", i, q[i], fromFile[i])
		}
	}
}
