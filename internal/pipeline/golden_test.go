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
// ケースは 2 つあり、それぞれ別の性質を守っている。
//
//	sorting  入力に時刻の逆行を含む。読み込み時の安定ソートを外すと落ちる
//	rounding ブラケット内挿の丸めが 0.5 ちょうどに当たる質問を含む。
//	         偶数丸めを math.Round に変えると落ちる
//
// どちらも 3 分ぶんなので、解析全体をこの 6 ファイルだけで通せる。
// 再生成は tools/gen_golden.sh。このパッケージ自身の出力で上書きしては
// ならない。退行を検出できなくなる。

const goldenDir = "../../testdata/golden"

type goldenCase struct {
	name     string
	from, to time.Time
	// 守っている性質。失敗時のメッセージに出す。
	guards string
}

func goldenCases() []goldenCase {
	d := func(h, m int) time.Time { return time.Date(2026, 6, 10, h, m, 0, 0, nanotime.JST) }
	return []goldenCase{
		{"sorting", d(0, 47), d(0, 50), "読み込み時の安定ソート"},
		{"rounding", d(0, 0), d(0, 3), "ブラケット内挿の偶数丸め"},
	}
}

func runCase(t *testing.T, c goldenCase, out string, sortInput bool) *pipeline.Result {
	t.Helper()
	cfg, err := config.Load(filepath.Join(goldenDir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	ssr, err := cfg.SSR("KX90S")
	if err != nil {
		t.Fatal(err)
	}
	station, err := cfg.Station("KX90")
	if err != nil {
		t.Fatal(err)
	}
	res, err := pipeline.Run(pipeline.Options{
		SSR:       ssr,
		Station:   station,
		QpkxRoot:  filepath.Join(goldenDir, c.name, "qpkx"),
		IntgRoot:  out,
		From:      c.from,
		To:        c.to,
		SortInput: sortInput,
		Log:       slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
	})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestMatchesGoldenOutput(t *testing.T) {
	for _, c := range goldenCases() {
		t.Run(c.name, func(t *testing.T) {
			out := t.TempDir()
			res := runCase(t, c, out, true)

			// ゴールデンに添えた集計とも突き合わせる。バイト比較だけだと、
			// 出力が空でファイルも空という状態を見逃しうる。
			var want struct {
				Records int `json:"records"`
				Blocks  int `json:"blocks"`
			}
			raw, err := os.ReadFile(filepath.Join(goldenDir, c.name, "expected_stats.json"))
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
			compareTrees(t, filepath.Join(goldenDir, c.name, "intg"), out, c.guards)
		})
	}
}

// TestUnsortedInputDivergesFromGolden は、逆行を含む入力をソートせずに
// 流すと出力が変わることを固定する。
//
// これは「ソート無しが間違い」を示すテストではなく、sorting ケースの入力が
// 依然として逆行を含んでいることの確認である。ここが一致するようになったら、
// TestMatchesGoldenOutput は安定ソートを検証しなくなっている。
func TestUnsortedInputDivergesFromGolden(t *testing.T) {
	c := goldenCases()[0]
	out := t.TempDir()
	runCase(t, c, out, false)
	if identicalTrees(t, filepath.Join(goldenDir, c.name, "intg"), out) {
		t.Error("ソート無しでもゴールデンと一致した。" +
			"sorting ケースの入力に時刻の逆行が含まれていない可能性がある")
	}
}

// TestRerunTruncatesInsteadOfAppending は同じ期間を 2 回流しても出力が
// 二重にならないことを確認する。バイト一致の検証は同じ期間を何度も流す
// 作業なので、ここが壊れると偽の不一致でデバッグ時間を溶かす。
func TestRerunTruncatesInsteadOfAppending(t *testing.T) {
	c := goldenCases()[0]
	out := t.TempDir()
	runCase(t, c, out, true)
	runCase(t, c, out, true)
	compareTrees(t, filepath.Join(goldenDir, c.name, "intg"), out, "再実行時の切り詰め")
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

func compareTrees(t *testing.T, want, got, guards string) {
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
		t.Errorf("%s: バイト一致せず（このケースが守っているのは %s）%s",
			rel, guards, firstMismatch(rel, w, g))
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
