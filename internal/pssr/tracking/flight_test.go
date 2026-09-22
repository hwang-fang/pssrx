package tracking_test

import (
	"slices"
	"testing"

	"pssrx/internal/pssr/tracking"
)

// chainFix は Flight のテスト用の点。連鎖 ID とスコーク、位置を付ける。
func chainFix(scan float64, chain int64, squawk uint16, altFt int, e, n float64, status tracking.FixStatus) tracking.Fix {
	f := fixAt(scan, squawk, altFt, e, n)
	f.Track, f.Chain, f.Status = chain, chain, status
	return f
}

func flightAll(t *testing.T, cfg tracking.Config, fixes []tracking.Fix, chunk int) ([]tracking.Fix, tracking.Stats) {
	t.Helper()
	if err := tracking.Validate(testParams, cfg); err != nil {
		t.Fatal(err)
	}
	var st tracking.FlightState
	var stats tracking.Stats
	if chunk <= 0 {
		chunk = len(fixes)
	}
	var out []tracking.Fix
	for i := 0; i < len(fixes); i += chunk {
		end := min(i+chunk, len(fixes))
		out = append(out, tracking.Flight(&st, &stats, testParams, cfg, fixes[i:end], end == len(fixes))...)
	}
	return out, stats
}

// line は連鎖 chain の点を走査 from から n 個、東へ 800 m/走査で作る。
func line(chain int64, squawk uint16, altFt int, from, n int, e0 float64, status tracking.FixStatus) []tracking.Fix {
	var v []tracking.Fix
	for k := range n {
		v = append(v, chainFix(float64(from+k), chain, squawk, altFt, e0+800*float64(k), 0, status))
	}
	return v
}

func byTime(fixes []tracking.Fix) []tracking.Fix {
	out := slices.Clone(fixes)
	slices.SortFunc(out, func(a, b tracking.Fix) int {
		if a.Timestamp != b.Timestamp {
			return int(a.Timestamp - b.Timestamp)
		}
		return int(a.Chain - b.Chain)
	})
	return out
}

func flightOf(out []tracking.Fix, chain int64) int64 {
	for _, f := range out {
		if f.Chain == chain {
			return f.Flight
		}
	}
	return 0
}

// TestFlightJoinsDiscreteSquawk は個別スコークの連鎖が、3 分の切れ目を
// またいでも、時間が重なって（像）いても、高度が違っても同じ便になり、
// 10 分を超えて空けば別の便になることを確認する。
func TestFlightJoinsDiscreteSquawk(t *testing.T) {
	var fixes []tracking.Fix
	fixes = append(fixes, line(1, 0o4321, 20000, 0, 12, 0, tracking.FixOK)...)               // 走査 0〜11
	fixes = append(fixes, line(2, 0o4321, 20000, 5, 12, 50_000, tracking.FixEcho)...)        // 重なる像
	fixes = append(fixes, line(3, 0o4321, 32000, 5, 12, -50_000, tracking.FixOK)...)         // 同時に高度 32,000: ガーブルとみなし同じ便
	fixes = append(fixes, line(4, 0o4321, 21000, 56, 12, 800*56+2000, tracking.FixOK)...)    // 45 走査（3 分）後の続き
	fixes = append(fixes, line(5, 0o4321, 21000, 220, 12, 800*220, tracking.FixOK)...)       // 11 分後: 別の便
	fixes = append(fixes, line(6, 0o4321, 21000, 20, 2, 800*20, tracking.FixUnconfirmed)...) // 便の中の unconfirmed
	fixes = byTime(fixes)
	out, s := flightAll(t, tracking.DefaultConfig(), fixes, 0)
	if len(out) != len(fixes) {
		t.Fatalf("出力 %d, 入力 %d", len(out), len(fixes))
	}
	f1, f2, f3, f4, f5, f6 := flightOf(out, 1), flightOf(out, 2), flightOf(out, 3), flightOf(out, 4), flightOf(out, 5), flightOf(out, 6)
	if f1 != f2 || f1 != f3 || f1 != f4 || f1 != f6 {
		t.Errorf("同じ便になるべき: 直接 %d, 像 %d, 高度違い %d, 続き %d, unconfirmed %d", f1, f2, f3, f4, f6)
	}
	if f5 == f1 {
		t.Errorf("別の便になるべき: 11 分後 %d (本体 %d)", f5, f1)
	}
	if s.Flights != 2 || s.FlightsNoise != 0 {
		t.Errorf("stats %+v", s)
	}
	for _, f := range out {
		if f.Chain == 6 && f.Status != tracking.FixUnconfirmed {
			t.Errorf("unconfirmed の判定が変わった: %v", f.Status)
		}
	}
}

// TestFlightNonUniqueSquawkIsChain は 1200 の連鎖がそれぞれ別の便になることを
// 確認する。
func TestFlightNonUniqueSquawkIsChain(t *testing.T) {
	var fixes []tracking.Fix
	fixes = append(fixes, line(1, 0o1200, 3000, 0, 12, 0, tracking.FixOK)...)
	fixes = append(fixes, line(2, 0o1200, 3000, 14, 12, 800*14, tracking.FixOK)...) // 8 s 後の続きでも別
	out, s := flightAll(t, tracking.DefaultConfig(), byTime(fixes), 0)
	if flightOf(out, 1) == flightOf(out, 2) || s.Flights != 2 {
		t.Errorf("1200 の連鎖が同じ便: %d %d, stats %+v", flightOf(out, 1), flightOf(out, 2), s)
	}
}

// TestFlightMarksSmallAsNoise は点数の足りない便が閉じたとき noise になり、
// 足りた便は届いた順に出ることを確認する。
func TestFlightMarksSmallAsNoise(t *testing.T) {
	var fixes []tracking.Fix
	fixes = append(fixes, line(1, 0o4321, 20000, 0, 12, 0, tracking.FixOK)...)      // 12 点: 便
	fixes = append(fixes, line(2, 0o5555, 20000, 0, 5, 100_000, tracking.FixOK)...) // 5 点: noise
	fixes = append(fixes, line(3, 0o5555, 20000, 3, 2, 100_000, tracking.FixUnconfirmed)...)
	fixes = append(fixes, line(4, 0o4321, 20000, 200, 12, 800*200, tracking.FixOK)...) // 13 分後。便 2 を閉じる
	fixes = byTime(fixes)
	out, s := flightAll(t, tracking.DefaultConfig(), fixes, 7)
	if len(out) != len(fixes) {
		t.Fatalf("出力 %d, 入力 %d", len(out), len(fixes))
	}
	for _, f := range out {
		switch f.Chain {
		case 2:
			if f.Status != tracking.FixNoise {
				t.Errorf("小さい便の点が noise でない: %v", f.Status)
			}
		case 3:
			if f.Status != tracking.FixUnconfirmed {
				t.Errorf("unconfirmed が変わった: %v", f.Status)
			}
		default:
			if f.Status != tracking.FixOK {
				t.Errorf("便 %d の点が %v", f.Chain, f.Status)
			}
		}
	}
	if s.FlightsNoise != 1 || s.FixesNoise != 5 || s.Flights != 3 {
		t.Errorf("stats %+v", s)
	}
	// 足りた便の点は時刻順、noise は遅れて出る
	var okTimes []int64
	for _, f := range out {
		if f.Chain == 1 {
			okTimes = append(okTimes, f.Timestamp)
		}
	}
	if !slices.IsSorted(okTimes) {
		t.Error("便の点が時刻順でない")
	}
}

// TestFlightIsChunkInvariant は投入の刻みによらず、便の割り当てと判定が
// 同じになることを確認する（出力の順序は noise の遅れで変わりうる）。
func TestFlightIsChunkInvariant(t *testing.T) {
	var fixes []tracking.Fix
	fixes = append(fixes, line(1, 0o4321, 20000, 0, 12, 0, tracking.FixOK)...)
	fixes = append(fixes, line(2, 0o4321, 20000, 5, 12, 50_000, tracking.FixEcho)...)
	fixes = append(fixes, line(3, 0o5555, 20000, 0, 5, 100_000, tracking.FixOK)...)
	fixes = append(fixes, line(4, 0o4321, 21000, 56, 12, 800*56+2000, tracking.FixOK)...)
	fixes = append(fixes, line(5, 0o1200, 3000, 0, 12, 0, tracking.FixOK)...)
	fixes = append(fixes, line(6, 0o4321, 21000, 220, 12, 800*220, tracking.FixOK)...)
	fixes = byTime(fixes)
	type key struct {
		t, chain, flight int64
		status           tracking.FixStatus
	}
	keys := func(out []tracking.Fix) []key {
		v := make([]key, len(out))
		for i, f := range byTime(out) {
			v[i] = key{f.Timestamp, f.Chain, f.Flight, f.Status}
		}
		return v
	}
	whole, ws := flightAll(t, tracking.DefaultConfig(), fixes, 0)
	for _, chunk := range []int{1, 4, 13} {
		got, gs := flightAll(t, tracking.DefaultConfig(), fixes, chunk)
		if !slices.Equal(keys(got), keys(whole)) {
			t.Errorf("chunk=%d: 割り当てが違う", chunk)
		}
		gs.FlightHeldMax, ws.FlightHeldMax = 0, 0
		gs.FlightsOpenMax, ws.FlightsOpenMax = 0, 0
		if gs != ws {
			t.Errorf("chunk=%d: stats %+v / %+v", chunk, gs, ws)
		}
	}
}
