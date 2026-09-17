package pipeline_test

import (
	"path/filepath"
	"testing"
	"time"

	"pssrx/internal/interrogator"
	"pssrx/internal/store"
)

// feedInBlocks はゴールデンの qpkx を block 刻みで Analyzer に流し、
// 出力 intg を連結して返す。pipeline.RunJob の 1 分ループを幅だけ変えたもの。
func feedInBlocks(t *testing.T, c goldenCase, block time.Duration) []store.Intg {
	t.Helper()
	params, dist, azimuth, _ := golden(t)
	an, err := interrogator.New(params, interrogator.DefaultConfig(), dist, azimuth, nil)
	if err != nil {
		t.Fatal(err)
	}
	repo := &store.QdataRepository{Root: filepath.Join(goldenDir, c.name, "data")}
	var out []store.Intg
	for cur := c.from; cur.Before(c.to); cur = cur.Add(block) {
		next := cur.Add(block)
		q, err := repo.Fetch("KX90", cur.UnixNano(), next.UnixNano())
		if err != nil {
			t.Fatal(err)
		}
		intg, err := an.Feed(q, next.UnixNano(), !next.Before(c.to))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, intg...)
	}
	return out
}

// TestFeedIsBlockSizeInvariant は投入の刻みを変えても質問予定表が変わらない
// ことを確認する。実時間化で 1 秒刻みになっても解析結果が同じであるための
// 条件で、先送りの猶予がセグメント分割の間隙より短いと、ブロック境界を
// またぐドウェルが割れて 1 秒刻みで崩れる。
func TestFeedIsBlockSizeInvariant(t *testing.T) {
	_, _, _, cases := golden(t)
	for _, c := range cases {
		want := feedInBlocks(t, c, time.Minute)
		for _, block := range []time.Duration{10 * time.Second, time.Second, 100 * time.Millisecond} {
			got := feedInBlocks(t, c, block)
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
