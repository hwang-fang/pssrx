package archive

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"pssrx/internal/config"
	"pssrx/internal/record"
)

// writeQpkx は 1 分ぶんの qpkx を書く。ticks は分先頭からの経過 [100 ns]。
func writeQpkx(t *testing.T, root, station string, dt time.Time, ticks []uint32) {
	t.Helper()
	p := (&QpkxDir{Root: root}).filePath(station, dt)
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
func collect(t *testing.T, s FileSource) []record.Block {
	t.Helper()
	var out []record.Block
	for blk, err := range s.Blocks() {
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, blk)
	}
	return out
}

// TestFileSourceBlocks は分ファイルを 1 ファイル 1 ブロックで読み、区間と
// Last、種別ごとの中身、無い分が空のブロックになることを確認する。
func TestFileSourceBlocks(t *testing.T) {
	root := t.TempDir()
	base := time.Date(2026, 6, 10, 0, 0, 0, 0, record.JST)
	for _, m := range []int{0, 2} { // 分 1 はファイルを置かない
		dt := base.Add(time.Duration(m) * time.Minute)
		writeQpkx(t, root, "ZZ01", dt, []uint32{300_000_000, 100, 599_999_999})
		writeApkx(t, &ApkxDir{Root: root}, "ZZ02", dt, []record.Reply{
			// 分の先頭の応答。F1 ではブロックの Start より前になるが、そのまま渡す
			{Timestamp: dt.UnixNano() - config.ReplyFrameLengthNs + 1000, Code: 1},
			{Timestamp: dt.UnixNano() + 5_000_000_000, Code: 2},
		})
		iRepo := &IntgDir{Root: root, Log: discardLogger()}
		if err := iRepo.Save("S1", []record.Interrogation{{Timestamp: dt.UnixNano() + 1_000_000_000, Mode: 3}}); err != nil {
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
		if i == 1 {
			if len(b.Received) != 0 || len(b.Replies) != 0 || len(b.Interrogations) != 0 {
				t.Errorf("[1] 無い分が空でない: %+v", b)
			}
			continue
		}
		if len(b.Received) != 3 || len(b.Replies) != 2 || len(b.Interrogations) != 1 {
			t.Errorf("[%d] 件数 qpkx=%d apkx=%d intg=%d, 期待 3/2/1", i, len(b.Received), len(b.Replies), len(b.Interrogations))
			continue
		}
		if b.Received[0].Timestamp != start+10_000 || b.Received[2].Timestamp != start+OneMinute-100 {
			t.Errorf("[%d] qpkx が整列していない: %+v", i, b.Received)
		}
		if b.Replies[0].Code != 1 || b.Replies[0].Timestamp >= start {
			t.Errorf("[%d] 分の先頭の応答が F1 の時刻で先頭に無い: %+v", i, b.Replies)
		}
	}
}

// TestFileSourceIntgLead は最初のブロックだけ前の分の intg を Start −
// IntgLeadNs 以降ぶん含み、2 つ目以降は含まないことを確認する。
func TestFileSourceIntgLead(t *testing.T) {
	root := t.TempDir()
	base := time.Date(2026, 6, 10, 0, 1, 0, 0, record.JST)
	iRepo := &IntgDir{Root: root, Log: discardLogger()}
	for m := -1; m < 2; m++ {
		dt := base.Add(time.Duration(m) * time.Minute)
		if err := iRepo.Save("S1", []record.Interrogation{
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
	if n := len(blocks[0].Interrogations); n != 3 {
		t.Errorf("最初のブロックの intg %d 件, 期待 3", n)
	} else if ts := blocks[0].Interrogations[0].Timestamp; ts != base.UnixNano()-500 {
		t.Errorf("先読みの先頭 %d, 期待 %d", ts, base.UnixNano()-500)
	}
	if n := len(blocks[1].Interrogations); n != 2 {
		t.Errorf("2 つ目のブロックの intg %d 件, 期待 2", n)
	}
}

// TestFileSourceRejectsBadConfig は不正な指定がブロックを出す前にエラーに
// なることを確認する。
func TestFileSourceRejectsBadConfig(t *testing.T) {
	base := time.Date(2026, 6, 10, 0, 0, 0, 0, record.JST)
	next := base.Add(time.Minute)
	cases := map[string]FileSource{
		"期間が逆":           {QpkxRoot: "r", QpkxStation: "s", From: base, To: base},
		"qpkx の局が無い":     {QpkxRoot: "r", From: base, To: next},
		"apkx の局が無い":     {ApkxRoot: "r", From: base, To: next},
		"intg の SSR が無い": {IntgRoot: "r", From: base, To: next},
		"種別が無い":          {From: base, To: next},
		"先読みが負":          {IntgRoot: "r", IntgSSR: "s", IntgLeadNs: -1, From: base, To: next},
		"先読みが 1 分以上":     {IntgRoot: "r", IntgSSR: "s", IntgLeadNs: OneMinute, From: base, To: next},
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
