package archive

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	"pssrx/internal/config"
	"pssrx/internal/record"
)

// encodeApkx は DecodeApkx の逆。テストデータの書き出し用で、F1 の時刻に
// F1–F2 間隔を足してファイル上の F2 の時刻にし、baseTime からの経過を
// 100 ns 単位へ切り捨てる。
func encodeApkx(data []record.Reply, baseTime int64) []byte {
	buf := make([]byte, len(data)*apkxRecordSize)
	for i, d := range data {
		b := buf[i*apkxRecordSize:]
		binary.LittleEndian.PutUint32(b[0:4], uint32((d.Timestamp+config.ReplyFrameLengthNs-baseTime)/tsResolution))
		binary.LittleEndian.PutUint16(b[4:6], d.Code)
		binary.LittleEndian.PutUint16(b[6:8], d.WH)
	}
	return buf
}

// writeApkx は 1 分ぶんの apkx を書く。data の時刻は F1。
func writeApkx(t *testing.T, r *ApkxDir, station string, dt time.Time, data []record.Reply) {
	t.Helper()
	p := r.filePath(station, dt)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, encodeApkx(data, dt.UnixNano()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestApkxPathLayout(t *testing.T) {
	r := &ApkxDir{Root: "/root"}
	got := r.filePath("KX90", time.Date(2026, 6, 10, 0, 47, 0, 0, record.JST))
	want := filepath.Join("/root", "202606", "KX90", "20260610", "apkx", "202606100047KX90.apkx")
	if got != want {
		t.Errorf("path = %s, 期待 %s", got, want)
	}
}

// TestDecodeApkx は 8 バイトレコードの復号を確認する。実データで観測した
// 値の桁（100 ns 単位のオフセット、12 ビット符号、0xFFFF 基準の波高値）を
// そのまま使う。ファイル上の時刻は F2 なので、復号した時刻は F1–F2 間隔
// だけ早い。
func TestDecodeApkx(t *testing.T) {
	base := time.Date(2026, 6, 10, 0, 0, 0, 0, record.JST).UnixNano()
	type rec struct {
		tick     uint32 // ファイル上の経過 [100 ns]（F2）
		code, wh uint16
	}
	in := []rec{
		{2545, 0o325, 44859},
		{12768, 0o1432, 46430},
		{599937443, 0o7777, 0xFFFF},
	}
	raw := make([]byte, 0, len(in)*apkxRecordSize)
	for _, r := range in {
		raw = binary.LittleEndian.AppendUint32(raw, r.tick)
		raw = binary.LittleEndian.AppendUint16(raw, r.code)
		raw = binary.LittleEndian.AppendUint16(raw, r.wh)
	}
	out, err := DecodeApkx(raw, base)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(in) {
		t.Fatalf("件数 %d, 期待 %d", len(out), len(in))
	}
	for i, r := range in {
		want := record.Reply{Timestamp: base + int64(r.tick)*tsResolution - config.ReplyFrameLengthNs, Code: r.code, WH: r.wh}
		if out[i] != want {
			t.Errorf("[%d] %+v, 期待 %+v", i, out[i], want)
		}
	}
	if _, err := DecodeApkx(make([]byte, 12), base); err == nil {
		t.Error("8 で割り切れないサイズがエラーにならない")
	}
}

// TestApkxReadMinute は逆行した入力の整列と、欠けた分の読み飛ばしを確認する。
func TestApkxReadMinute(t *testing.T) {
	r := &ApkxDir{Root: t.TempDir()}
	m0 := time.Date(2026, 6, 10, 0, 47, 0, 0, record.JST)
	m1 := m0.Add(time.Minute) // ファイルを置かない分
	// m0 は逆行を含む（先頭に遅い時刻）
	writeApkx(t, r, "KX90", m0, []record.Reply{
		{Timestamp: m0.UnixNano() + 30_000_000_000, Code: 3},
		{Timestamp: m0.UnixNano() + 10_000_000_000, Code: 1},
		{Timestamp: m0.UnixNano() + 20_000_000_000, Code: 2},
	})

	got, err := r.ReadMinute("KX90", m0)
	if err != nil {
		t.Fatal(err)
	}
	want := []uint16{1, 2, 3}
	if len(got) != len(want) {
		t.Fatalf("件数 %d, 期待 %d: %+v", len(got), len(want), got)
	}
	for i, c := range want {
		if got[i].Code != c {
			t.Errorf("[%d] code = %d, 期待 %d", i, got[i].Code, c)
		}
	}
	got, err = r.ReadMinute("KX90", m1)
	if err != nil || len(got) != 0 {
		t.Errorf("無い分が空にならない: %+v, %v", got, err)
	}
}

// TestApkxReadMinuteKeepsFileDivision は分の先頭 20.3 µs にある応答（F2 時刻
// ではその分、F1 時刻では前の分）がファイルの分のまま、F1 の時刻で返る
// ことを確認する。時刻での切り直しはせず、対応づけ側が吸収する。
func TestApkxReadMinuteKeepsFileDivision(t *testing.T) {
	r := &ApkxDir{Root: t.TempDir()}
	m0 := time.Date(2026, 6, 10, 0, 47, 0, 0, record.JST)
	m1 := m0.Add(time.Minute)
	// F1 が m1 の 10 µs 前 → F2 は m1 の 10.3 µs 後で、ファイルは m1 の分
	edge := record.Reply{Timestamp: m1.UnixNano() - 10_000, Code: 7}
	writeApkx(t, r, "KX90", m1, []record.Reply{{Timestamp: m1.UnixNano() + 5_000_000_000, Code: 8}, edge})

	got, err := r.ReadMinute("KX90", m1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Code != 7 || got[0].Timestamp != edge.Timestamp || got[1].Code != 8 {
		t.Errorf("分の先頭の応答が F1 の時刻で先頭に来ない: %+v", got)
	}
	got, err = r.ReadMinute("KX90", m0)
	if err != nil || len(got) != 0 {
		t.Errorf("前の分に混ざった: %+v, %v", got, err)
	}
}
