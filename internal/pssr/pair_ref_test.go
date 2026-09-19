package pssr

import (
	"math"
	"math/rand/v2"
	"reflect"
	"slices"
	"testing"

	"pssrx/internal/pssr/simtest"
	"pssrx/internal/record"
	"pssrx/internal/ssr"
)

// このファイルは列の集約（assignToRun / closeRuns / emit と Pair のループ）の
// 参照実装を持つ。性能改修の前の素直な実装をそのまま写したもので、改修後の
// 実装が同じ入力に対して同じ Plot 列と Stats を返すことを、乱数の FRUIT を
// 混ぜた合成データとパラメータの組み合わせで確かめる。
//
// 参照実装は読みやすさだけを目的にしており、性能は気にしない。本体を変える
// ときはこちらは変えない（仕様を意図して変えるときだけ、両方を変える）。

type refRun struct {
	replies  []PairedReply
	lastSeq  int64
	lastTau  int64
	modeA    uint16
	hasModeA bool
}

type refState struct {
	runs []*refRun
	seq  int64
}

func refPair(st *refState, stats *Stats, params Params, cfg Config, intg []record.Interrogation, replies []record.Reply) []Plot {
	var plots []Plot
	j := 0
	for i, q := range intg {
		seq := st.seq + int64(i)
		start := q.Timestamp + params.TauMinNs
		end := int64(math.MaxInt64)
		if i+1 < len(intg) {
			end = intg[i+1].Timestamp + params.TauMinNs
		}
		for ; j < len(replies) && replies[j].Timestamp < start; j++ {
			stats.Unpaired++
		}
		for ; j < len(replies) && replies[j].Timestamp < end; j++ {
			r := replies[j]
			tau := r.Timestamp - q.Timestamp
			histogramAdd(&stats.Tau, tau)
			if tau > params.TauMaxNs {
				stats.Unpaired++
				continue
			}
			stats.Paired++
			refAssign(st, stats, cfg, PairedReply{Interrogation: q, Reply: r, TauNs: tau}, seq)
		}
		plots = refClose(st, stats, cfg, plots, seq-int64(cfg.MaxGap)-1)
	}
	st.seq += int64(len(intg))
	if len(intg) == 0 {
		stats.Unpaired += len(replies) - j
	}
	slices.SortStableFunc(plots, func(a, b Plot) int { return compareInt64(a.Timestamp, b.Timestamp) })
	return plots
}

func refCloseAll(st *refState, stats *Stats, cfg Config) []Plot {
	var plots []Plot
	for _, ru := range st.runs {
		plots = refEmit(stats, cfg, plots, ru)
	}
	st.runs = nil
	slices.SortStableFunc(plots, func(a, b Plot) int { return compareInt64(a.Timestamp, b.Timestamp) })
	return plots
}

// refAssign: 開いている列を先頭から順に見て、同じ質問の応答を持たず、
// τ の差が許容内で、Mode A の符号が合う列のうち τ の差が最小のもの
// （同点なら先に見つかった = 生成が古い列）に加える。無ければ末尾に開く。
func refAssign(st *refState, stats *Stats, cfg Config, pr PairedReply, seq int64) {
	stats.Assignments++
	stats.OpenRunsTotal += int64(len(st.runs))
	mode := pr.Interrogation.Mode
	var best *refRun
	var bestDiff int64
	for _, ru := range st.runs {
		if ru.lastSeq == seq {
			continue
		}
		diff := absInt64(pr.TauNs - ru.lastTau)
		if diff > cfg.TauToleranceNs {
			continue
		}
		if mode == ModeA && ru.hasModeA && ru.modeA != pr.Reply.Code {
			continue
		}
		if best == nil || diff < bestDiff {
			best, bestDiff = ru, diff
		}
	}
	if best == nil {
		best = &refRun{}
		st.runs = append(st.runs, best)
	}
	best.replies = append(best.replies, pr)
	best.lastSeq = seq
	best.lastTau = pr.TauNs
	if mode == ModeA && !best.hasModeA {
		best.modeA, best.hasModeA = pr.Reply.Code, true
	}
}

func refClose(st *refState, stats *Stats, cfg Config, plots []Plot, lastSeqAtMost int64) []Plot {
	kept := st.runs[:0]
	for _, ru := range st.runs {
		if ru.lastSeq <= lastSeqAtMost {
			plots = refEmit(stats, cfg, plots, ru)
			continue
		}
		kept = append(kept, ru)
	}
	st.runs = kept
	return plots
}

func refEmit(stats *Stats, cfg Config, plots []Plot, ru *refRun) []Plot {
	stats.Runs++
	if len(ru.replies) == 1 {
		stats.RunsSingle++
	}
	if len(ru.replies) < cfg.MinReplies {
		stats.RunsTooShort++
		return plots
	}
	if !ru.hasModeA {
		stats.NoModeA++
		return plots
	}
	pl := refMakePlot(ru.replies, ru.modeA)
	alt, res := resolveAltitude(pl.Timestamp, ru.replies)
	switch res {
	case altitudeNone:
		stats.NoAltitude++
		return plots
	case altitudeSpread:
		stats.AltitudeSpread++
		return plots
	}
	if alt > cfg.MaxAltitudeFt {
		stats.AltitudeTooHigh++
		return plots
	}
	pl.AltitudeFt = alt
	stats.Plots++
	return append(plots, pl)
}

func refMakePlot(replies []PairedReply, modeA uint16) Plot {
	first, last := replies[0].Interrogation, replies[len(replies)-1].Interrogation
	var sum int64
	for _, r := range replies {
		sum += r.TauNs
	}
	return Plot{
		Timestamp: first.Timestamp + (last.Timestamp-first.Timestamp)/2,
		Azimuth:   midAngle(first.Azimuth, last.Azimuth),
		TauNs:     int64(math.RoundToEven(float64(sum) / float64(len(replies)))),
		Squawk:    Squawk(modeA),
		Replies:   replies,
	}
}

// --- 合成データ ---

var refParams = Params{
	SSRID: "S", StationID: "T", TauMinNs: 7_253, TauMaxNs: 2_676_000,
	AroundTimeNs: 4_040_000_000, MaxRangeM: 400_000,
}

func refSchedule(t testing.TB, count int) []record.Interrogation {
	t.Helper()
	modes, _ := ssr.ParseModes("AC")
	pat, _ := ssr.PatternFromStagger([]int64{2_949_900}, modes)
	return simtest.Schedule{
		Start: 1_781_000_000_000_000_000, Count: count, Pattern: pat,
		AroundTimeNs: refParams.AroundTimeNs, Azimuth0: 1.0, Clockwise: true,
	}.Interrogations()
}

// synthReplies は機体の応答と、質問区間に一様な FRUIT（fruitPerQ 件/質問）を
// 混ぜた応答列を時刻順に返す。FRUIT の符号は無作為で、たまたま同じ τ・同じ
// 符号が続いて列になる偶然も含む。
func synthReplies(rng *rand.Rand, intg []record.Interrogation, aircraft []simtest.Aircraft, fruitPerQ float64) []record.Reply {
	replies := simtest.Replies(intg, aircraft...)
	n := len(intg)
	span := intg[n-1].Timestamp + refParams.TauMaxNs - intg[0].Timestamp
	count := int(fruitPerQ * float64(n))
	for range count {
		replies = append(replies, record.Reply{
			Timestamp: intg[0].Timestamp + rng.Int64N(span),
			Code:      uint16(rng.IntN(1 << 12)),
			WH:        uint16(30000 + rng.IntN(30000)),
		})
	}
	slices.SortStableFunc(replies, func(a, b record.Reply) int { return compareInt64(a.Timestamp, b.Timestamp) })
	return replies
}

func synthAircraft(rng *rand.Rand, n int, count int) []simtest.Aircraft {
	var out []simtest.Aircraft
	for range count {
		first := rng.IntN(n - 20)
		length := 3 + rng.IntN(15)
		a := simtest.Aircraft{
			TauNs: refParams.TauMinNs + rng.Int64N(refParams.TauMaxNs-refParams.TauMinNs-2000),
			ModeA: uint16(rng.IntN(1 << 12)),
			ModeC: []uint16{simtest.GillhamCode(1000 + 100*rng.IntN(400))},
			First: first, Last: min(first+length, n-1), WH: 45000,
		}
		if rng.IntN(3) == 0 {
			a.ModeC = append(a.ModeC, simtest.GillhamCode(1000+100*rng.IntN(400))) // 高度が変わる
		}
		for k := first; k <= a.Last; k++ {
			if rng.IntN(8) == 0 {
				a.Skip = append(a.Skip, k)
			}
		}
		out = append(out, a)
	}
	return out
}

// runBoth は同じ入力を本体と参照実装に流し、Plot 列と Stats を返す。
// chunk が正なら intg と応答を chunk 質問ぶんずつに分けて呼び、状態の
// 持ち越しも比較の対象にする。
func runBoth(intg []record.Interrogation, replies []record.Reply, cfg Config, chunk int) (got, want []Plot, gotStats, wantStats Stats) {
	var st RunState
	var rst refState
	gotStats, wantStats = NewStats(refParams), NewStats(refParams)
	if chunk <= 0 {
		chunk = len(intg)
	}
	j := 0
	for i := 0; i < len(intg); i += chunk {
		qs := intg[i:min(i+chunk, len(intg))]
		// この範囲の質問に属しうる応答: 次の範囲の先頭質問の TauMin 手前まで
		until := int64(math.MaxInt64)
		if i+chunk < len(intg) {
			until = intg[i+chunk].Timestamp + refParams.TauMinNs
		}
		k := j
		for k < len(replies) && replies[k].Timestamp < until {
			k++
		}
		rs := replies[j:k]
		j = k
		got = append(got, Pair(&st, &gotStats, refParams, cfg, qs, rs)...)
		want = append(want, refPair(&rst, &wantStats, refParams, cfg, qs, rs)...)
	}
	got = append(got, CloseRuns(&st, &gotStats, cfg)...)
	want = append(want, refCloseAll(&rst, &wantStats, cfg)...)
	return
}

func comparePlots(t *testing.T, name string, got, want []Plot, gotStats, wantStats Stats) {
	t.Helper()
	if !reflect.DeepEqual(gotStats, wantStats) {
		t.Errorf("%s: Stats が参照実装と違う\n  got  %+v\n  want %+v", name, gotStats, wantStats)
	}
	if len(got) != len(want) {
		t.Fatalf("%s: プロット数 %d, 参照 %d", name, len(got), len(want))
	}
	for i := range want {
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Fatalf("%s: plots[%d] が参照実装と違う\n  got  %+v\n  want %+v", name, i, got[i], want[i])
		}
	}
}

// TestPairMatchesReference は列の集約が参照実装と Plot 列・Stats の両方で
// 一致することを、パラメータと分割を変えながら確かめる。
func TestPairMatchesReference(t *testing.T) {
	const n = 3000 // 質問数。約 9 秒、2 回転強
	intg := refSchedule(t, n)
	base := DefaultConfig()
	cases := []struct {
		name       string
		minReplies int
		maxGap     int
		tolNs      int64
		fruitPerQ  float64
		aircraft   int
	}{
		{"default", 3, 2, 1000, 12, 8},
		{"dense", 3, 2, 1000, 40, 20},
		{"sparse", 3, 2, 1000, 0.5, 4},
		{"no_fruit", 3, 2, 1000, 0, 6},
		{"min1", 1, 2, 1000, 12, 8},
		{"min2", 2, 2, 1000, 12, 8},
		{"min6", 6, 2, 1000, 12, 8},
		{"gap0", 3, 0, 1000, 12, 8},
		{"gap4", 3, 4, 1000, 12, 8},
		{"tol_wide", 3, 2, 50_000, 12, 8},
		{"tol_narrow", 3, 2, 100, 12, 8},
	}
	for _, c := range cases {
		for seed := range uint64(3) {
			rng := rand.New(rand.NewPCG(seed, 12345))
			cfg := base
			cfg.MinReplies, cfg.MaxGap, cfg.TauToleranceNs = c.minReplies, c.maxGap, c.tolNs
			replies := synthReplies(rng, intg, synthAircraft(rng, n, c.aircraft), c.fruitPerQ)
			for _, chunk := range []int{0, 1, 7, 500} {
				got, want, gs, ws := runBoth(intg, replies, cfg, chunk)
				comparePlots(t, c.name, got, want, gs, ws)
			}
		}
	}
}

// TestPairMatchesReferenceEdgeCases は空の入力と列の無い状態を確かめる。
func TestPairMatchesReferenceEdgeCases(t *testing.T) {
	intg := refSchedule(t, 50)
	cfg := DefaultConfig()
	rng := rand.New(rand.NewPCG(1, 1))
	replies := synthReplies(rng, intg, nil, 5)

	got, want, gs, ws := runBoth(nil, replies, cfg, 0)
	comparePlots(t, "intg 無し", got, want, gs, ws)
	got, want, gs, ws = runBoth(intg, nil, cfg, 0)
	comparePlots(t, "応答無し", got, want, gs, ws)
	got, want, gs, ws = runBoth(intg, replies, cfg, 0)
	comparePlots(t, "機体無し", got, want, gs, ws)
}

// BenchmarkPair は高密度 FRUIT（1 質問あたり 13 件、実データ相当）での
// 列の集約の速度。1 分ぶん（約 20,000 質問）を 1 回とする。
func BenchmarkPair(b *testing.B) {
	const n = 20_000
	intg := refSchedule(b, n)
	rng := rand.New(rand.NewPCG(7, 7))
	replies := synthReplies(rng, intg, synthAircraft(rng, n, 60), 13)
	cfg := DefaultConfig()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		var st RunState
		stats := NewStats(refParams)
		_ = Pair(&st, &stats, refParams, cfg, intg, replies)
		_ = CloseRuns(&st, &stats, cfg)
	}
}
