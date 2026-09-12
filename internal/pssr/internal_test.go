package pssr

import (
	"log/slog"
	"testing"

	"pssrx/internal/pattern"
	"pssrx/internal/store"
)

// TestBufferIsBounded は常駐運転で質問予定の緩衝が増え続けないことを確認する。
// 参照されなくなった質問予定は Feed のたびに落とす。
func TestBufferIsBounded(t *testing.T) {
	modes, _ := pattern.ParseModes("AC")
	pat, _ := pattern.FromStagger([]int64{2_949_900}, modes)
	p, err := New(Params{SSRID: "S", StationID: "T", TauMinNs: 7_253, TauMaxNs: 2_676_000}, DefaultConfig(), slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	const perBlock = 20_000 // 約 1 分
	start := int64(1_781_000_000_000_000_000)
	maxLen := 0
	for blk := range 30 {
		intg := make([]store.Intg, perBlock)
		var replies []store.AData
		for i := range intg {
			ts := start + pat.Cumulative(int64(blk*perBlock+i))
			intg[i] = store.Intg{Timestamp: ts, Mode: pat.ModeAt(int64(i))}
			if i%40 < 8 { // 8 質問ずつ応答する機体
				replies = append(replies, store.AData{Timestamp: ts + 1_000_000, Code: 0o1200})
			}
		}
		upTo := intg[len(intg)-1].Timestamp + 1
		if _, err := p.Feed(replies, intg, upTo, false); err != nil {
			t.Fatal(err)
		}
		maxLen = max(maxLen, len(p.intg))
		if len(p.pending) > 100 {
			t.Fatalf("ブロック %d で保留が %d 件", blk, len(p.pending))
		}
	}
	// 参照が要るのは高々 TauMax（1 質問ぶん）+ 開いている列の分
	if maxLen > 100 {
		t.Errorf("質問予定の緩衝が %d 件まで膨らんだ", maxLen)
	}
}
