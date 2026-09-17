package pipeline_test

import (
	"bytes"
	"cmp"
	"math"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"pssrx/internal/archive"
	"pssrx/internal/pipeline"
	"pssrx/internal/pssr"
	"pssrx/internal/record"
)

// memIntg は intg をメモリに溜める IntgSink。
type memIntg struct{ recs []record.Interrogation }

func (m *memIntg) Save(_ string, data []record.Interrogation) error {
	m.recs = append(m.recs, data...)
	return nil
}

// split は 1 分のブロックを width 刻みに切り直した Source を返す。
// 実時間で 1 秒ごとに来る状況をファイルから模擬する。各列は時刻で
// 振り分け、先頭の小ブロックは Start より前を、末尾の小ブロックは End
// 以降をそれぞれ引き受ける（apkx の F1 ずれと intg の先読みのため）。
// Last は元のブロックの最後の小ブロックだけに付く。
func split(src pipeline.Source, width time.Duration) pipeline.Source {
	w := width.Nanoseconds()
	return func(yield func(record.Block, error) bool) {
		for blk, err := range src {
			if err != nil {
				yield(blk, err)
				return
			}
			for s := blk.Start; s < blk.End; s += w {
				e := min(s+w, blk.End)
				sub := record.Block{Start: s, End: e, Last: blk.Last && e == blk.End}
				lo, hi := s, e
				if s == blk.Start {
					lo = math.MinInt64
				}
				if e == blk.End {
					hi = math.MaxInt64
				}
				sub.Received = within(blk.Received, lo, hi, func(q record.ReceivedInterrogation) int64 { return q.Timestamp })
				sub.Replies = within(blk.Replies, lo, hi, func(a record.Reply) int64 { return a.Timestamp })
				sub.Interrogations = within(blk.Interrogations, lo, hi, func(d record.Interrogation) int64 { return d.Timestamp })
				if !yield(sub, nil) {
					return
				}
			}
		}
	}
}

func within[T any](sorted []T, lo, hi int64, ts func(T) int64) []T {
	i, _ := slices.BinarySearchFunc(sorted, lo, func(x T, t int64) int { return cmp.Compare(ts(x), t) })
	j, _ := slices.BinarySearchFunc(sorted, hi, func(x T, t int64) int { return cmp.Compare(ts(x), t) })
	return sorted[i:j]
}

// runInBlocks はゴールデンの qpkx を block 刻みで RunInterrogator に流し、
// 出力 intg を返す。
func runInBlocks(t *testing.T, c goldenCase, block time.Duration) []record.Interrogation {
	t.Helper()
	params, dist, azimuth, _ := golden(t)
	src := archive.FileSource{
		QpkxRoot: filepath.Join(goldenDir, c.name, "data"), QpkxStation: "KX90",
		From: c.from, To: c.to,
	}
	var out memIntg
	_, err := pipeline.RunInterrogator(split(src.Blocks(), block), pipeline.InterrogatorStage{
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
// またぐドウェルが割れて 1 秒刻みで崩れる。1 分刻みは FileSource そのもの
// （切り直しは恒等）。
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

// runInBlocksBoth はゴールデンの qpkx + apkx を block 刻みで Run に
// 流し、位置の CSV を返す。
func runInBlocksBoth(t *testing.T, c goldenCase, block time.Duration) []byte {
	t.Helper()
	params, dist, azimuth, _ := golden(t)
	var out bytes.Buffer
	sink, _ := pssr.NewCSVSink(&out, nil, "KX90S", "KX90")
	ps := pssrStage(t, sink)
	is := pipeline.InterrogatorStage{
		SSRID: "KX90S", StationID: "KX90",
		Params: params, Dist: dist, Azimuth: azimuth, Log: ps.Log,
	}
	src := split(rawSource(c, ps).Blocks(), block)
	if _, err := pipeline.Run(src, is, ps); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// TestRunIsBlockSizeInvariant は 2 段の直列でも投入の刻みで位置が
// 変わらないことを確認する。pssr 段は PairManager が刻みを吸収する。
func TestRunIsBlockSizeInvariant(t *testing.T) {
	c := findCase(t, "rounding")
	want := runInBlocksBoth(t, c, time.Minute)
	for _, block := range blockSizes {
		got := runInBlocksBoth(t, c, block)
		if !bytes.Equal(got, want) {
			t.Errorf("block=%v: 位置が 1 分刻みと不一致%s", block, firstLineDiff(want, got))
		}
	}
}
