package pipeline_test

import (
	"bytes"
	"flag"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/goccy/go-yaml"
	"pssrx/internal/geodesy"
	"pssrx/internal/pipeline"
	"pssrx/internal/pssr"
)

// updatePSSRGolden は fixes.csv を現在の出力で書き換える。仕様を意図して
// 変えたときだけ使い、差分を確かめて記録する。
var updatePSSRGolden = flag.Bool("update-pssr-golden", false, "PSSR のゴールデン fixes.csv を現在の出力で更新する")

// pssrManifest は golden.yaml の pssr 節。
type pssrManifest struct {
	Station    string  `yaml:"station"`
	TauMinNs   int64   `yaml:"tau_min_ns"`
	TauMaxNs   int64   `yaml:"tau_max_ns"`
	MaxRangeM  float64 `yaml:"max_range_m"`
	SSR        lla     `yaml:"ssr"`
	StationPos lla     `yaml:"station_pos"`
}

type lla struct {
	Lat float64 `yaml:"lat"`
	Lon float64 `yaml:"lon"`
	Alt float64 `yaml:"alt"`
}

func (l lla) geo() geodesy.OrthometricLLA {
	return geodesy.OrthometricLLA{Lat: l.Lat, Lon: l.Lon, Alt: l.Alt}
}

// pssrJob は golden.yaml のリテラル値から PSSRJob を組む。設定ファイルや
// 幾何計算は通さない。
func pssrJob(t *testing.T, c goldenCase, sink pssr.Sink) pipeline.PSSRJob {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(goldenDir, "golden.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var mf manifest
	if err := yaml.UnmarshalWithOptions(raw, &mf, yaml.Strict()); err != nil {
		t.Fatal(err)
	}
	m := mf.PSSR
	params, _, _, _ := golden(t)
	return pipeline.PSSRJob{
		Params: pssr.Params{
			SSRID: "KX90S", StationID: m.Station,
			TauMinNs: m.TauMinNs, TauMaxNs: m.TauMaxNs,
			AroundTimeNs: params.AroundTimeNs, MaxRangeM: m.MaxRangeM,
		},
		Config:   pssr.DefaultConfig(),
		IntgRoot: filepath.Join(goldenDir, c.name, "intg"),
		DataRoot: filepath.Join(goldenDir, c.name, "data"),
		From:     c.from,
		To:       c.to,
		Log:      slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
		SSR:      m.SSR.geo(),
		Station:  m.StationPos.geo(),
		Sink:     sink,
	}
}

// TestPSSRMatchesGolden はファイル経由（golden の intg + apkx）の位置が
// fixes.csv と一致することを確認する。
func TestPSSRMatchesGolden(t *testing.T) {
	c := findCase(t, "rounding")
	var buf bytes.Buffer
	sink, err := pssr.NewCSVSink(&buf, nil, "KX90S", "KX90")
	if err != nil {
		t.Fatal(err)
	}
	res, err := pipeline.RunPSSRJob(pssrJob(t, c, sink))
	if err != nil {
		t.Fatal(err)
	}
	if res.Stats.Fixes == 0 {
		t.Fatal("位置が 1 件も出ない")
	}

	path := filepath.Join(goldenDir, c.name, "fixes.csv")
	if *updatePSSRGolden {
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s を更新 (%d 件)", path, res.Stats.Fixes)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, buf.Bytes()) {
		t.Errorf("fixes.csv と一致しない。意図した変更なら -update-pssr-golden で更新する%s",
			firstLineDiff(want, buf.Bytes()))
	}
}

// TestRunBothMatchesFileMode はメモリ直列（qpkx + apkx から 2 段）が
// ファイル経由（intg + apkx）と同じ位置を出すことを確認する。
//
// メモリ直列では intg をファイル形式と同じに量子化して渡すので、
// バイト単位で一致しなければならない。ここが崩れると、ファイルからの
// 再処理が本番の結果を再現しなくなる。
func TestRunBothMatchesFileMode(t *testing.T) {
	c := findCase(t, "rounding")
	params, dist, azimuth, _ := golden(t)

	var fileOut bytes.Buffer
	fileSink, _ := pssr.NewCSVSink(&fileOut, nil, "KX90S", "KX90")
	if _, err := pipeline.RunPSSRJob(pssrJob(t, c, fileSink)); err != nil {
		t.Fatal(err)
	}

	var memOut bytes.Buffer
	memSink, _ := pssr.NewCSVSink(&memOut, nil, "KX90S", "KX90")
	pj := pssrJob(t, c, memSink)
	ij := pipeline.Job{
		SSRID: "KX90S", StationID: "KX90",
		Params: params, Dist: dist, Azimuth: azimuth,
		QpkxRoot: pj.DataRoot, IntgRoot: "", // intg は書かない
		From: c.from, To: c.to, Log: pj.Log,
	}
	res, err := pipeline.RunBoth(ij, pj)
	if err != nil {
		t.Fatal(err)
	}
	if res.PSSR.Stats.Fixes == 0 {
		t.Fatal("位置が 1 件も出ない")
	}
	if !bytes.Equal(fileOut.Bytes(), memOut.Bytes()) {
		t.Errorf("メモリ直列とファイル経由の位置が一致しない%s", firstLineDiff(fileOut.Bytes(), memOut.Bytes()))
	}
}

// TestGoldenIntgMatchesRunBoth はメモリ直列で書いた intg もゴールデンと
// 一致することを確認する。RunBoth が interrogator 段の出力を変えていない
// ことの確認。
func TestGoldenIntgMatchesRunBoth(t *testing.T) {
	c := findCase(t, "rounding")
	params, dist, azimuth, _ := golden(t)
	out := t.TempDir()
	pj := pssrJob(t, c, nil)
	ij := pipeline.Job{
		SSRID: "KX90S", StationID: "KX90",
		Params: params, Dist: dist, Azimuth: azimuth,
		QpkxRoot: pj.DataRoot, IntgRoot: out,
		From: c.from, To: c.to, Log: pj.Log,
	}
	if _, err := pipeline.RunBoth(ij, pj); err != nil {
		t.Fatal(err)
	}
	compareTrees(t, filepath.Join(goldenDir, c.name, "intg"), out, "メモリ直列での intg 書き出し")
}

func firstLineDiff(want, got []byte) string {
	w := bytes.Split(want, []byte("\n"))
	g := bytes.Split(got, []byte("\n"))
	for i := range min(len(w), len(g)) {
		if !bytes.Equal(w[i], g[i]) {
			return "\n  最初の相違 行 " + itoa(i+1) + "\n    ゴールデン " + string(w[i]) + "\n    出力       " + string(g[i])
		}
	}
	return "\n  行数のみ相違 " + itoa(len(w)) + " vs " + itoa(len(g))
}

func itoa(i int) string { return strconv.Itoa(i) }
