package pipeline_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pssrx/internal/config"
	"pssrx/internal/nanotime"
	"pssrx/internal/pipeline"
	"pssrx/internal/store"
)

// testdata/golden に、既知の正しい出力を入力の qpkx ごと固定してある。
// 対象は 2026-06-10 00:47〜00:50 の KX90。この 3 分を選んだのは、00:48 と
// 00:49 の qpkx にそれぞれ時刻の逆行が 1 箇所ずつ含まれるためで、
// 読み込み時の安定ソートが効いていないとテストが落ちる。
//
// ゴールデンの再生成は tools/gen_golden.sh で行う。このパッケージ自身の
// 出力で更新してはならない。退行を検出できなくなる。

const goldenDir = "../../testdata/golden"

func TestMatchesGoldenOutput(t *testing.T) {
	cfg, err := config.Load(filepath.Join(goldenDir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()

	from := time.Date(2026, 6, 10, 0, 47, 0, 0, nanotime.JST)
	to := time.Date(2026, 6, 10, 0, 50, 0, 0, nanotime.JST)

	res, err := pipeline.Run(pipeline.Options{
		Config:    cfg,
		QpkxRoot:  filepath.Join(goldenDir, "qpkx"),
		IntgRoot:  out,
		From:      from,
		To:        to,
		SortInput: true,
		Log:       slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
	})
	if err != nil {
		t.Fatal(err)
	}

	// ゴールデンに添えた集計とも突き合わせる。バイト比較だけだと、
	// 出力が空でファイルも空という状態を見逃しうる。
	var want struct {
		Records      int   `json:"records"`
		Blocks       int   `json:"blocks"`
		AroundTimeNs int64 `json:"around_time_ns"`
		DelayNs      int64 `json:"delay_ns"`
	}
	raw, err := os.ReadFile(filepath.Join(goldenDir, "expected_stats.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if res.Stats.RecordsEmitted != want.Records {
		t.Errorf("出力レコード数 %d, 期待 %d", res.Stats.RecordsEmitted, want.Records)
	}
	if res.Stats.Blocks != want.Blocks {
		t.Errorf("ブロック数 %d, 期待 %d", res.Stats.Blocks, want.Blocks)
	}

	compareTrees(t, filepath.Join(goldenDir, "intg"), out)
}

// TestUnsortedInputDivergesFromGolden は、逆行を含む入力をソートせずに
// 流すと出力が変わることを固定する。
//
// これは「ソート無しが間違い」を示すテストではなく、テストデータが
// 依然として逆行を含んでいることの確認である。ここが一致するように
// なったら、TestMatchesGoldenOutput は安定ソートを検証しなくなっている。
func TestUnsortedInputDivergesFromGolden(t *testing.T) {
	cfg, err := config.Load(filepath.Join(goldenDir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	_, err = pipeline.Run(pipeline.Options{
		Config:    cfg,
		QpkxRoot:  filepath.Join(goldenDir, "qpkx"),
		IntgRoot:  out,
		From:      time.Date(2026, 6, 10, 0, 47, 0, 0, nanotime.JST),
		To:        time.Date(2026, 6, 10, 0, 50, 0, 0, nanotime.JST),
		SortInput: false,
		Log:       slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
	})
	if err != nil {
		t.Fatal(err)
	}
	if identicalTrees(t, filepath.Join(goldenDir, "intg"), out) {
		t.Error("ソート無しでもゴールデンと一致した。テストデータに時刻の逆行が含まれていない可能性がある")
	}
}

// TestRerunTruncatesInsteadOfAppending は同じ期間を 2 回流しても
// 出力が二重にならないことを確認する。バイト一致の検証は同じ期間を
// 何度も流す作業なので、ここが壊れると偽の不一致でデバッグ時間を溶かす。
func TestRerunTruncatesInsteadOfAppending(t *testing.T) {
	cfg, err := config.Load(filepath.Join(goldenDir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	opts := pipeline.Options{
		Config:    cfg,
		QpkxRoot:  filepath.Join(goldenDir, "qpkx"),
		IntgRoot:  out,
		From:      time.Date(2026, 6, 10, 0, 47, 0, 0, nanotime.JST),
		To:        time.Date(2026, 6, 10, 0, 50, 0, 0, nanotime.JST),
		SortInput: true,
		Log:       slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
	}
	if _, err := pipeline.Run(opts); err != nil {
		t.Fatal(err)
	}
	if _, err := pipeline.Run(opts); err != nil {
		t.Fatal(err)
	}
	compareTrees(t, filepath.Join(goldenDir, "intg"), out)
}

func walkIntg(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !e.IsDir() && strings.HasSuffix(p, ".intg") {
			rel, _ := filepath.Rel(dir, p)
			out = append(out, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func compareTrees(t *testing.T, want, got string) {
	t.Helper()
	wantFiles := walkIntg(t, want)
	gotFiles := walkIntg(t, got)
	if len(wantFiles) == 0 {
		t.Fatalf("ゴールデンが空。tools/gen_golden.sh を実行したか確認すること")
	}
	if len(wantFiles) != len(gotFiles) {
		t.Fatalf("ファイル数 %d, ゴールデン = %d\n  got  %v\n  want %v",
			len(gotFiles), len(wantFiles), gotFiles, wantFiles)
	}
	for _, rel := range wantFiles {
		w, err := os.ReadFile(filepath.Join(want, rel))
		if err != nil {
			t.Fatal(err)
		}
		g, err := os.ReadFile(filepath.Join(got, rel))
		if err != nil {
			t.Errorf("%s: %v", rel, err)
			continue
		}
		if bytes.Equal(w, g) {
			continue
		}
		t.Errorf("%s: バイト一致せず (ゴールデン %d byte, 出力 %d byte)%s",
			rel, len(w), len(g), firstMismatch(rel, w, g))
	}
}

func identicalTrees(t *testing.T, a, b string) bool {
	t.Helper()
	af, bf := walkIntg(t, a), walkIntg(t, b)
	if len(af) != len(bf) {
		return false
	}
	for _, rel := range af {
		x, err1 := os.ReadFile(filepath.Join(a, rel))
		y, err2 := os.ReadFile(filepath.Join(b, rel))
		if err1 != nil || err2 != nil || !bytes.Equal(x, y) {
			return false
		}
	}
	return true
}

// firstMismatch は最初に食い違ったレコードをデコードして示す。
// バイト列の差分だけでは原因の見当がつかないため。
func firstMismatch(rel string, want, got []byte) string {
	base, err := time.ParseInLocation("200601021504", filepath.Base(rel)[:12], nanotime.JST)
	if err != nil {
		return ""
	}
	wr, err1 := store.DecodeIntg(want, base.UnixNano())
	gr, err2 := store.DecodeIntg(got, base.UnixNano())
	if err1 != nil || err2 != nil {
		return ""
	}
	for i := range min(len(wr), len(gr)) {
		if wr[i] != gr[i] {
			return fmt.Sprintf("\n  最初の相違 レコード %d\n"+
				"    ゴールデン ts=%d mode=%d az=%.12f\n"+
				"    出力       ts=%d mode=%d az=%.12f",
				i, wr[i].Timestamp, wr[i].Mode, wr[i].Azimuth,
				gr[i].Timestamp, gr[i].Mode, gr[i].Azimuth)
		}
	}
	return fmt.Sprintf("\n  先頭 %d レコードは一致（長さのみ相違）", min(len(wr), len(gr)))
}
