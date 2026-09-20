package pipeline_test

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"pssrx/internal/archive"
	"pssrx/internal/interrogator"
	"pssrx/internal/pipeline"
	"pssrx/internal/record"
)

// このファイルは質問受信（qpkx）が欠けたときの挙動を固定する。
//
//	欠落が MaxBridgeRotations を超える: その区間だけ質問予定が出ず、前後は
//	  欠落が無いときと同じ
//	欠落がドウェル 1 本ぶん: 隣のドウェル対が 2 回転を内挿で埋め、レコード数は
//	  変わらない
//
// 入力はゴールデン rounding（3 分）を一時ディレクトリに写し、qpkx を
// 加工して作る。

// gapRoot はゴールデンの qpkx を dst に写す。mutate が nil でなければ各分の
// ファイルの中身を書き換えられる（nil を返すとその分のファイルを置かない）。
func gapRoot(t *testing.T, c goldenCase, mutate func(minute time.Time, raw []byte) []byte) string {
	t.Helper()
	dst := t.TempDir()
	for cur := c.from; cur.Before(c.to); cur = cur.Add(time.Minute) {
		rel := filepath.Join(cur.Format("200601"), "KX90", cur.Format("20060102"), "qpkx", cur.Format("200601021504")+"KX90.qpkx")
		raw, err := os.ReadFile(filepath.Join(goldenDir, c.name, "data", rel))
		if err != nil {
			t.Fatal(err)
		}
		if mutate != nil {
			if raw = mutate(cur, raw); raw == nil {
				continue
			}
		}
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dst, rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, rel), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dst
}

// runIntg は root の qpkx を既定の定数で解析して質問予定と統計を返す。
func runIntg(t *testing.T, c goldenCase, root string) ([]record.Interrogation, interrogator.Stats) {
	t.Helper()
	return runIntgWith(t, c, root, interrogator.DefaultConfig())
}

func runIntgWith(t *testing.T, c goldenCase, root string, cfg interrogator.Config) ([]record.Interrogation, interrogator.Stats) {
	t.Helper()
	params, dist, azimuth, _ := golden(t)
	src := archive.FileSource{QpkxRoot: root, QpkxStation: "KX90", From: c.from, To: c.to}
	var out memIntg
	res, err := pipeline.RunInterrogator(src.Blocks(), pipeline.InterrogatorStage{
		SSRID: "KX90S", StationID: "KX90",
		Params: params, Config: cfg, Dist: dist, Azimuth: azimuth,
		Intg: &out,
	})
	if err != nil {
		t.Fatal(err)
	}
	return out.recs, res.Stats
}

// TestGapBeyondBridgeSkipsOnlyThatInterval は真ん中の 1 分が無いとき、その
// 区間の質問予定だけが抜けて前後が変わらないことを確認する。
func TestGapBeyondBridgeSkipsOnlyThatInterval(t *testing.T) {
	c := findCase(t, "rounding")
	whole, _ := runIntg(t, c, filepath.Join(goldenDir, c.name, "data"))
	missing := c.from.Add(time.Minute)
	gapped, s := runIntg(t, c, gapRoot(t, c, func(m time.Time, raw []byte) []byte {
		if m.Equal(missing) {
			return nil
		}
		return raw
	}))
	if s.RotationMismatch != 1 {
		t.Errorf("RotationMismatch = %d, 期待 1（欠落をまたぐドウェル対を 1 つ棄却）", s.RotationMismatch)
	}
	if len(gapped) == 0 || len(gapped) >= len(whole) {
		t.Fatalf("欠落ありのレコード数 %d、無し %d", len(gapped), len(whole))
	}
	// 欠落前: 欠落ありの出力は、欠落無しの先頭部分とレコード単位で一致する
	before := 0
	for before < len(gapped) && gapped[before].Timestamp < missing.UnixNano() {
		if gapped[before] != whole[before] {
			t.Fatalf("欠落前のレコード %d が違う: %+v / %+v", before, gapped[before], whole[before])
		}
		before++
	}
	// 欠落区間にレコードが無い
	gapEnd := missing.Add(time.Minute).UnixNano()
	for _, r := range gapped[before:] {
		if r.Timestamp < gapEnd {
			t.Fatalf("欠落区間に質問予定がある: %+v", r)
		}
	}
	// 欠落後: 欠落ありの残りは、欠落無しの同じ時刻以降と一致する
	k := 0
	for k < len(whole) && whole[k].Timestamp < gapped[before].Timestamp {
		k++
	}
	after := gapped[before:]
	if len(whole)-k != len(after) {
		t.Fatalf("欠落後のレコード数 %d、欠落無しの同区間 %d", len(after), len(whole)-k)
	}
	for i := range after {
		if after[i] != whole[k+i] {
			t.Fatalf("欠落後のレコード %d が違う: %+v / %+v", i, after[i], whole[k+i])
		}
	}
	// 欠落前の最後は、欠落した分の直前のドウェルまで。欠落無しより少ない
	if before >= len(whole) || whole[before].Timestamp >= missing.UnixNano() {
		t.Error("欠落前の出力が欠落無しと同じ長さ。最後のドウェル以降が出ている")
	}
	t.Logf("欠落無し %d、欠落あり %d（前 %d、後 %d）", len(whole), len(gapped), before, len(after))
}

// TestSingleDwellLossIsBridged はドウェル 1 本ぶんの受信を消しても、隣の
// ドウェル対が 2 回転を内挿で埋めてレコード数が変わらないことを確認する。
func TestSingleDwellLossIsBridged(t *testing.T) {
	c := findCase(t, "rounding")
	params, _, azimuth, _ := golden(t)
	whole, ws := runIntg(t, c, filepath.Join(goldenDir, c.name, "data"))

	// 2 分目の中ほどでビームが局を向く時刻（方位が局方位を横切る）を探す
	minute := c.from.Add(time.Minute)
	var center int64
	for i := 1; i < len(whole); i++ {
		a, b := whole[i-1].Azimuth-azimuth, whole[i].Azimuth-azimuth
		if whole[i].Timestamp > minute.UnixNano()+20e9 && a*b < 0 && math.Abs(a-b) < 0.01 {
			center = whole[i].Timestamp
			break
		}
	}
	if center == 0 {
		t.Fatal("ドウェル中心が見つからない")
	}
	// その前後 80 ms の受信を消す（ドウェル 1 本ぶん）
	removed := 0
	root := gapRoot(t, c, func(m time.Time, raw []byte) []byte {
		if !m.Equal(minute) {
			return raw
		}
		out := raw[:0:0]
		for i := 0; i+7 <= len(raw); i += 7 {
			ts := m.UnixNano() + int64(binary.LittleEndian.Uint32(raw[i:]))*100
			if ts > center-80e6 && ts < center+80e6 {
				removed++
				continue
			}
			out = append(out, raw[i:i+7]...)
		}
		return out
	})
	if removed < 5 {
		t.Fatalf("消した受信が %d 件。ドウェルの位置を外している", removed)
	}
	gapped, s := runIntg(t, c, root)
	if s.DwellsDetected != ws.DwellsDetected-1 || s.BracketsEmitted != ws.BracketsEmitted-1 || s.RotationMismatch != 0 {
		t.Errorf("stats: dwells %d→%d, brackets %d→%d, mismatch %d", ws.DwellsDetected, s.DwellsDetected, ws.BracketsEmitted, s.BracketsEmitted, s.RotationMismatch)
	}
	if len(gapped) != len(whole) {
		t.Fatalf("レコード数 %d、欠落無し %d（2 回転の内挿で埋まっていない）", len(gapped), len(whole))
	}
	// 内挿の基準が変わった回転の質問時刻は数 µs 動きうるが、それ以外は同じ
	moved := 0
	for i := range whole {
		if gapped[i] != whole[i] {
			moved++
			if d := gapped[i].Timestamp - whole[i].Timestamp; d < -50_000 || d > 50_000 {
				t.Fatalf("レコード %d の時刻が %d ns 動いた", i, d)
			}
		}
	}
	perRotation := int(float64(params.AroundTimeNs) / params.Pattern.MeanPRI())
	if moved > 2*perRotation {
		t.Errorf("動いたレコード %d 件。2 回転ぶん（%d）を超えている", moved, 2*perRotation)
	}
	t.Logf("消した受信 %d 件、動いたレコード %d / %d", removed, moved, len(whole))
}

// TestBridgeLimitIsConfigurable は MaxBridgeRotations を欠落より大きくすると、
// 欠落区間を内挿で埋めてしまうことを確認する。既定値 2 が欠落を埋めない
// 側にあることの裏付け。
func TestBridgeLimitIsConfigurable(t *testing.T) {
	c := findCase(t, "rounding")
	missing := c.from.Add(time.Minute)
	root := gapRoot(t, c, func(m time.Time, raw []byte) []byte {
		if m.Equal(missing) {
			return nil
		}
		return raw
	})
	cfg := interrogator.DefaultConfig()
	cfg.MaxBridgeRotations = 20 // 1 分 ≈ 15 回転を跨げる
	bridged, s := runIntgWith(t, c, root, cfg)
	if s.RotationMismatch != 0 {
		t.Errorf("RotationMismatch = %d, 期待 0", s.RotationMismatch)
	}
	inGap := 0
	for _, r := range bridged {
		if r.Timestamp >= missing.UnixNano() && r.Timestamp < missing.Add(time.Minute).UnixNano() {
			inGap++
		}
	}
	if inGap == 0 {
		t.Error("上限を広げても欠落区間が内挿されない")
	}
	t.Logf("上限 20 では欠落区間に %d レコードが内挿される", inGap)
}
