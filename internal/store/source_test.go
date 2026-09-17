package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"pssrx/internal/config"
)

// writeQpkx は 1 分ぶんの qpkx を書く。ticks は分先頭からの経過 [100 ns]。
func writeQpkx(t *testing.T, root, station string, dt time.Time, ticks []uint32) {
	t.Helper()
	p := (&QdataRepository{Root: root}).filePath(station, dt)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	raw := []byte{}
	for _, tick := range ticks {
		raw = append(raw, byte(tick), byte(tick>>8), byte(tick>>16), byte(tick>>24), 3, 0xFF, 0xFF)
	}
	if err := os.WriteFile(p, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

// collect は Source を流し切ってブロックを集める。
func collect(t *testing.T, s FileSource) []Block {
	t.Helper()
	var out []Block
	for blk, err := range s.Blocks() {
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, blk)
	}
	return out
}

// TestFileSourceBlocks は分ファイル 3 つを 1 分刻みで読み、ブロックの境界と
// Last、種別ごとの中身を確認する。
func TestFileSourceBlocks(t *testing.T) {
	root := t.TempDir()
	base := time.Date(2026, 6, 10, 0, 0, 0, 0, JST)
	for m := range 3 {
		dt := base.Add(time.Duration(m) * time.Minute)
		writeQpkx(t, root, "ZZ01", dt, []uint32{100, 300_000_000, 599_999_999})
		writeApkx(t, &AdataRepository{Root: root}, "ZZ02", dt, []AData{
			{Timestamp: dt.UnixNano() + 5_000_000_000, Code: 1},
		})
	}
	iRepo := &IntgRepository{Root: root, Log: discardLogger()}
	for m := range 3 {
		dt := base.Add(time.Duration(m) * time.Minute)
		if err := iRepo.Save("S1", []Intg{{Timestamp: dt.UnixNano() + 1_000_000_000, Mode: 3}}); err != nil {
			t.Fatal(err)
		}
	}

	blocks := collect(t, FileSource{
		QpkxRoot: root, QpkxStation: "ZZ01",
		ApkxRoot: root, ApkxStation: "ZZ02",
		IntgRoot: root, IntgSSR: "S1",
		From: base, To: base.Add(3 * time.Minute),
	})
	if len(blocks) != 3 {
		t.Fatalf("ブロック数 %d, 期待 3", len(blocks))
	}
	for i, b := range blocks {
		start := base.Add(time.Duration(i) * time.Minute).UnixNano()
		if b.Start != start || b.End != start+OneMinute {
			t.Errorf("[%d] 範囲 [%d, %d), 期待 [%d, %d)", i, b.Start, b.End, start, start+OneMinute)
		}
		if b.Last != (i == 2) {
			t.Errorf("[%d] Last = %v", i, b.Last)
		}
		if len(b.QData) != 3 || len(b.Replies) != 1 || len(b.Intg) != 1 {
			t.Errorf("[%d] 件数 qpkx=%d apkx=%d intg=%d, 期待 3/1/1", i, len(b.QData), len(b.Replies), len(b.Intg))
		}
		for _, q := range b.QData {
			if q.Timestamp < b.Start || q.Timestamp >= b.End {
				t.Errorf("[%d] qpkx %d がブロックの外", i, q.Timestamp)
			}
		}
	}
}

// TestFileSourceFinerBlocksConcatenateToMinute は 1 分より短い刻みで読んだ
// ブロックを繋ぐと 1 分刻みと同じ列になることを確認する。分境界をまたぐ
// apkx（F1 補正で前の分へ移る応答）と、ファイル上の逆行を含める。
func TestFileSourceFinerBlocksConcatenateToMinute(t *testing.T) {
	root := t.TempDir()
	base := time.Date(2026, 6, 10, 0, 0, 0, 0, JST)
	for m := range 2 {
		dt := base.Add(time.Duration(m) * time.Minute)
		// 先頭に後ろの時刻が来る逆行
		writeQpkx(t, root, "ZZ01", dt, []uint32{500_000_000, 100, 10_000_000, 599_999_990})
		// 分の先頭の応答は F1 では前の分に属する
		writeApkx(t, &AdataRepository{Root: root}, "ZZ01", dt, []AData{
			{Timestamp: dt.UnixNano() - config.ReplyFrameLengthNs + 1000, Code: 1},
			{Timestamp: dt.UnixNano() + 30_000_000_000, Code: 2},
		})
	}
	src := FileSource{
		QpkxRoot: root, QpkxStation: "ZZ01",
		ApkxRoot: root, ApkxStation: "ZZ01",
		From: base, To: base.Add(2 * time.Minute),
	}
	var wantQ []QData
	var wantA []AData
	for _, b := range collect(t, src) {
		wantQ = append(wantQ, b.QData...)
		wantA = append(wantA, b.Replies...)
	}
	if len(wantQ) != 8 || len(wantA) != 3 {
		t.Fatalf("1 分刻みの件数 qpkx=%d apkx=%d, 期待 8/3", len(wantQ), len(wantA))
	}
	for _, block := range []time.Duration{10 * time.Second, time.Second, 7 * time.Second} {
		src.Block = block
		var gotQ []QData
		var gotA []AData
		for _, b := range collect(t, src) {
			gotQ = append(gotQ, b.QData...)
			gotA = append(gotA, b.Replies...)
		}
		if len(gotQ) != len(wantQ) || len(gotA) != len(wantA) {
			t.Errorf("block=%v: 件数 qpkx=%d apkx=%d, 期待 %d/%d", block, len(gotQ), len(gotA), len(wantQ), len(wantA))
			continue
		}
		for i := range wantQ {
			if gotQ[i] != wantQ[i] {
				t.Errorf("block=%v: qpkx[%d] = %+v, 期待 %+v", block, i, gotQ[i], wantQ[i])
			}
		}
		for i := range wantA {
			if gotA[i] != wantA[i] {
				t.Errorf("block=%v: apkx[%d] = %+v, 期待 %+v", block, i, gotA[i], wantA[i])
			}
		}
	}
}

// TestFileSourceIntgLead は最初のブロックだけ Intg を Start より前から
// 読み、2 つ目以降は読まないことを確認する。
func TestFileSourceIntgLead(t *testing.T) {
	root := t.TempDir()
	base := time.Date(2026, 6, 10, 0, 1, 0, 0, JST)
	iRepo := &IntgRepository{Root: root, Log: discardLogger()}
	for m := -1; m < 2; m++ {
		dt := base.Add(time.Duration(m) * time.Minute)
		if err := iRepo.Save("S1", []Intg{
			{Timestamp: dt.UnixNano() + OneMinute - 2000, Mode: 3},
			{Timestamp: dt.UnixNano() + OneMinute - 500, Mode: 3},
		}); err != nil {
			t.Fatal(err)
		}
	}
	blocks := collect(t, FileSource{
		IntgRoot: root, IntgSSR: "S1", IntgLeadNs: 1000,
		From: base, To: base.Add(2 * time.Minute),
	})
	if len(blocks) != 2 {
		t.Fatalf("ブロック数 %d, 期待 2", len(blocks))
	}
	// 先頭は前の分の末尾 500 ns 前の 1 件（2000 ns 前は先読み幅の外）を含む
	if n := len(blocks[0].Intg); n != 3 {
		t.Errorf("最初のブロックの intg %d 件, 期待 3", n)
	} else if ts := blocks[0].Intg[0].Timestamp; ts != base.UnixNano()-500 {
		t.Errorf("先読みの先頭 %d, 期待 %d", ts, base.UnixNano()-500)
	}
	if n := len(blocks[1].Intg); n != 2 {
		t.Errorf("2 つ目のブロックの intg %d 件, 期待 2", n)
	}
}

// TestFileSourceRejectsBadConfig は不正な指定がブロックを出す前にエラーに
// なることを確認する。
func TestFileSourceRejectsBadConfig(t *testing.T) {
	base := time.Date(2026, 6, 10, 0, 0, 0, 0, JST)
	cases := map[string]FileSource{
		"期間が逆":           {QpkxRoot: "r", QpkxStation: "s", From: base, To: base},
		"qpkx の局が無い":     {QpkxRoot: "r", From: base, To: base.Add(time.Minute)},
		"apkx の局が無い":     {ApkxRoot: "r", From: base, To: base.Add(time.Minute)},
		"intg の SSR が無い": {IntgRoot: "r", From: base, To: base.Add(time.Minute)},
		"種別が無い":          {From: base, To: base.Add(time.Minute)},
		"先読みが負":          {IntgRoot: "r", IntgSSR: "s", IntgLeadNs: -1, From: base, To: base.Add(time.Minute)},
	}
	for name, s := range cases {
		n := 0
		var got error
		for _, err := range s.Blocks() {
			n++
			got = err
		}
		if n != 1 || got == nil {
			t.Errorf("%s: %d 回 yield, err=%v; エラー 1 回を期待", name, n, got)
		}
	}
}
