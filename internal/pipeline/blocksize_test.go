package pipeline_test

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"

	"pssrx/internal/pipeline"
	"pssrx/internal/pssr"
	"pssrx/internal/store"
)

// memIntg は intg をメモリに溜める IntgSink。
type memIntg struct{ recs []store.Intg }

func (m *memIntg) Save(_ string, data []store.Intg) error {
	m.recs = append(m.recs, data...)
	return nil
}

// runInBlocks はゴールデンの qpkx を block 刻みで RunJob に流し、
// 出力 intg を返す。
func runInBlocks(t *testing.T, c goldenCase, block time.Duration) []store.Intg {
	t.Helper()
	params, dist, azimuth, _ := golden(t)
	src := store.FileSource{
		QpkxRoot: filepath.Join(goldenDir, c.name, "data"), QpkxStation: "KX90",
		From: c.from, To: c.to, Block: block,
	}
	var out memIntg
	_, err := pipeline.RunJob(src.Blocks(), pipeline.Job{
		SSRID: "KX90S", StationID: "KX90",
		Params: params, Dist: dist, Azimuth: azimuth,
		Intg: &out,
	})
	if err != nil {
		t.Fatal(err)
	}
	return out.recs
}

// blockSizes は 1 分刻みと比べる刻み。
var blockSizes = []time.Duration{10 * time.Second, time.Second, 100 * time.Millisecond}

// TestFeedIsBlockSizeInvariant は投入の刻みを変えても質問予定表が変わらない
// ことを確認する。実時間化で 1 秒刻みになっても解析結果が同じであるための
// 条件で、先送りの猶予がセグメント分割の間隙より短いと、ブロック境界を
// またぐドウェルが割れて 1 秒刻みで崩れる。
func TestFeedIsBlockSizeInvariant(t *testing.T) {
	_, _, _, cases := golden(t)
	for _, c := range cases {
		want := runInBlocks(t, c, time.Minute)
		for _, block := range blockSizes {
			got := runInBlocks(t, c, block)
			if len(got) != len(want) {
				t.Errorf("%s: block=%v: レコード数 %d、1 分刻みは %d", c.name, block, len(got), len(want))
				continue
			}
			diff := 0
			for i := range want {
				if got[i] != want[i] {
					diff++
				}
			}
			if diff > 0 {
				t.Errorf("%s: block=%v: %d レコードが 1 分刻みと不一致", c.name, block, diff)
			}
		}
	}
}

// runBothInBlocks はゴールデンの qpkx + apkx を block 刻みで RunBoth に
// 流し、位置の CSV を返す。
func runBothInBlocks(t *testing.T, c goldenCase, block time.Duration) []byte {
	t.Helper()
	params, dist, azimuth, _ := golden(t)
	var out bytes.Buffer
	sink, _ := pssr.NewCSVSink(&out, nil, "KX90S", "KX90")
	pj := pssrJob(t, sink)
	ij := pipeline.Job{
		SSRID: "KX90S", StationID: "KX90",
		Params: params, Dist: dist, Azimuth: azimuth, Log: pj.Log,
	}
	src := rawSource(c, pj)
	src.Block = block
	if _, err := pipeline.RunBoth(src.Blocks(), ij, pj); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// TestRunBothIsBlockSizeInvariant は 2 段の直列でも投入の刻みで位置が
// 変わらないことを確認する。pssr 段は PairManager が刻みを吸収する。
func TestRunBothIsBlockSizeInvariant(t *testing.T) {
	c := findCase(t, "rounding")
	want := runBothInBlocks(t, c, time.Minute)
	for _, block := range blockSizes {
		got := runBothInBlocks(t, c, block)
		if !bytes.Equal(got, want) {
			t.Errorf("block=%v: 位置が 1 分刻みと不一致%s", block, firstLineDiff(want, got))
		}
	}
}
