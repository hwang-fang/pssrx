package pssr

import (
	"testing"

	"pssrx/internal/record"
	"pssrx/internal/ssr"
)

// TestBufferIsBounded は常駐運転で待ち行列と列が増え続けないことを確認する。
// 取り出した範囲は削除され、閉じた列は捨てられる。
func TestBufferIsBounded(t *testing.T) {
	modes, _ := ssr.ParseModes("AC")
	pat, _ := ssr.PatternFromStagger([]int64{2_949_900}, modes)
	params := Params{SSRID: "S", StationID: "T", TauMinNs: 7_253, TauMaxNs: 2_676_000, AroundTimeNs: 4_040_000_000, MaxRangeM: 400_000}
	mgr := NewSynchronizer(params, DefaultConfig())
	var st RunState
	stats := NewStats(params)
	const perBlock = 20_000 // 約 1 分
	start := int64(1_781_000_000_000_000_000)
	maxLen := 0
	for blk := range 30 {
		intg := make([]record.Interrogation, perBlock)
		var replies []record.Reply
		for i := range intg {
			ts := start + pat.Cumulative(int64(blk*perBlock+i))
			intg[i] = record.Interrogation{Timestamp: ts, Mode: pat.ModeAt(int64(i))}
			if i%40 < 8 { // 8 質問ずつ応答する機体
				replies = append(replies, record.Reply{Timestamp: ts + 1_000_000, Code: 0o1200})
			}
		}
		mgr.PushInterrogations(&stats, intg)
		mgr.PushReplies(&stats, replies)
		qs, rs := mgr.Extract(false)
		Pair(&st, &stats, params, DefaultConfig(), qs, rs)
		maxLen = max(maxLen, len(mgr.intg)+len(mgr.replies))
		if len(st.tau) > 10 || len(st.cold) > 10 {
			t.Fatalf("ブロック %d で開いている列が %d 本、slot が %d 個", blk, len(st.tau), len(st.cold))
		}
	}
	// 残るのは最後の質問 + TauMax 以降の応答と、応答の揃っていない末尾の質問だけ
	if maxLen > 100 {
		t.Errorf("待ち行列が %d 件まで膨らんだ", maxLen)
	}
}
