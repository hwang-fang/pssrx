package pipeline_test

import (
	"bytes"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/goccy/go-yaml"
	"pssrx/internal/archive"
	"pssrx/internal/interrogator"
	"pssrx/internal/pipeline"
	"pssrx/internal/record"
	"pssrx/internal/ssr"
)

// testdata/golden に、既知の正しい出力を入力の qpkx ごと固定してある。
// ケースは 2 つあり、それぞれ別の性質を守っている。
//
//	sorting  入力に時刻の逆行を含む。読み込み時の整列を外すと落ちる
//	rounding ブラケット内挿の丸めが 0.5 ちょうどに当たる質問を含む。
//	         偶数丸めを math.Round に変えると落ちる
//
// どちらも 3 分ぶんなので、解析全体をこの 6 ファイルだけで通せる。
//
// 解析パラメータは golden.yaml にリテラルで固定し、pipeline.RunInterrogator へ直接
// 渡す。設定ファイルや緯度経度からの幾何計算は通さないので、それらの
// 仕様が変わってもゴールデンは変えずに済む。ゴールデンをこのパッケージ
// 自身の出力で上書きしてはならない。退行を検出できなくなる。

const goldenDir = "../../testdata/golden"

// manifest は golden.yaml の形。
type manifest struct {
	Params struct {
		AroundTimeNs int64   `yaml:"around_time_ns"`
		Pattern      string  `yaml:"pattern"`
		StaggerNs    []int64 `yaml:"stagger_ns"`
		Clockwise    bool    `yaml:"clockwise"`
		DistHex      string  `yaml:"st_dist_hex"`
		AzimuthHex   string  `yaml:"st_azimuth_hex"`
	} `yaml:"params"`
	PSSR  pssrManifest `yaml:"pssr"`
	Cases map[string]struct {
		From    string `yaml:"from"`
		To      string `yaml:"to"`
		Guards  string `yaml:"guards"` // 守っている性質。失敗時のメッセージに出す
		Blocks  int    `yaml:"blocks"`
		Records int    `yaml:"records"`
	} `yaml:"cases"`
}

type goldenCase struct {
	name            string
	from, to        time.Time
	guards          string
	blocks, records int
}

// golden は golden.yaml を読み、解析パラメータと幾何、ケース一覧を返す。
func golden(t *testing.T) (interrogator.Params, float64, float64, []goldenCase) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(goldenDir, "golden.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var m manifest
	if err := yaml.UnmarshalWithOptions(raw, &m, yaml.Strict()); err != nil {
		t.Fatalf("golden.yaml: %v", err)
	}

	modes, err := ssr.ParseModes(m.Params.Pattern)
	if err != nil {
		t.Fatal(err)
	}
	pat, err := ssr.PatternFromStagger(m.Params.StaggerNs, modes)
	if err != nil {
		t.Fatal(err)
	}
	params := interrogator.Params{
		AroundTimeNs: m.Params.AroundTimeNs,
		Pattern:      pat,
		Clockwise:    m.Params.Clockwise,
	}
	// 距離と方位はビット単位で固定したいので 16 進浮動小数で持つ
	dist, err := strconv.ParseFloat(m.Params.DistHex, 64)
	if err != nil {
		t.Fatalf("st_dist_hex: %v", err)
	}
	azimuth, err := strconv.ParseFloat(m.Params.AzimuthHex, 64)
	if err != nil {
		t.Fatalf("st_azimuth_hex: %v", err)
	}

	var cases []goldenCase
	for _, name := range slices.Sorted(maps.Keys(m.Cases)) {
		c := m.Cases[name]
		from, err := time.ParseInLocation("2006-01-02T15:04", c.From, record.JST)
		if err != nil {
			t.Fatalf("cases.%s.from: %v", name, err)
		}
		to, err := time.ParseInLocation("2006-01-02T15:04", c.To, record.JST)
		if err != nil {
			t.Fatalf("cases.%s.to: %v", name, err)
		}
		cases = append(cases, goldenCase{name, from, to, c.Guards, c.Blocks, c.Records})
	}
	return params, dist, azimuth, cases
}

func findCase(t *testing.T, name string) goldenCase {
	t.Helper()
	_, _, _, cases := golden(t)
	for _, c := range cases {
		if c.name == name {
			return c
		}
	}
	t.Fatalf("golden.yaml にケース %s が無い", name)
	return goldenCase{}
}

func runCase(t *testing.T, c goldenCase, out string) *pipeline.InterrogatorResult {
	t.Helper()
	params, dist, azimuth, _ := golden(t)
	src := archive.FileSource{
		QpkxRoot: filepath.Join(goldenDir, c.name, "data"), QpkxStation: "KX90",
		From: c.from, To: c.to,
	}
	res, err := pipeline.RunInterrogator(src.Blocks(), pipeline.InterrogatorStage{
		SSRID:     "KX90S",
		StationID: "KX90",
		Params:    params,
		Dist:      dist,
		Azimuth:   azimuth,
		Intg:      &archive.IntgDir{Root: out},
		Log:       slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
	})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestMatchesGoldenOutput(t *testing.T) {
	_, _, _, cases := golden(t)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := t.TempDir()
			res := runCase(t, c, out)
			if res.Stats.RecordsEmitted != c.records {
				t.Errorf("出力レコード数 %d, 期待 %d", res.Stats.RecordsEmitted, c.records)
			}
			if res.Stats.Blocks != c.blocks {
				t.Errorf("ブロック数 %d, 期待 %d", res.Stats.Blocks, c.blocks)
			}
			compareTrees(t, filepath.Join(goldenDir, c.name, "intg"), out, c.guards)
		})
	}
}

// TestSortingCaseHasInversions は sorting ケースの入力 qpkx が依然として
// 時刻の逆行を含むことを確認する。読み込み時の整列はこのケースで守られて
// いるので、逆行が無くなればゴールデンは整列を検証しなくなっている。
func TestSortingCaseHasInversions(t *testing.T) {
	c := findCase(t, "sorting")
	root := filepath.Join(goldenDir, c.name, "data")
	inversions := 0
	for cur := c.from; cur.Before(c.to); cur = cur.Add(time.Minute) {
		path := filepath.Join(root, cur.Format("200601"), "KX90", cur.Format("20060102"), "qpkx",
			cur.Format("200601021504")+"KX90.qpkx")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		recs, err := archive.DecodeQpkx(raw, cur.UnixNano())
		if err != nil {
			t.Fatal(err)
		}
		for i := 1; i < len(recs); i++ {
			if recs[i].Timestamp < recs[i-1].Timestamp {
				inversions++
			}
		}
	}
	if inversions == 0 {
		t.Error("sorting ケースの入力に時刻の逆行が無い。ゴールデンが整列を検証しなくなっている")
	}
}

// TestRerunTruncatesInsteadOfAppending は同じ期間を 2 回流しても出力が
// 二重にならないことを確認する。バイト一致の検証は同じ期間を何度も流す
// 作業なので、ここが壊れると偽の不一致でデバッグ時間を溶かす。
func TestRerunTruncatesInsteadOfAppending(t *testing.T) {
	c := findCase(t, "sorting")
	out := t.TempDir()
	runCase(t, c, out)
	runCase(t, c, out)
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
		t.Fatalf("ゴールデン %s が空", want)
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
	base, err := time.ParseInLocation("200601021504", filepath.Base(rel)[:12], record.JST)
	if err != nil {
		return ""
	}
	wr, err1 := archive.DecodeIntg(want, base.UnixNano())
	gr, err2 := archive.DecodeIntg(got, base.UnixNano())
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
