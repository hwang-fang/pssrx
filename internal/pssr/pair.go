package pssr

import (
	"math"
	"slices"

	"pssrx/internal/numeric"
	"pssrx/internal/record"
)

// RunState は対応づけが取り出しの境界をまたいで持ち越す記録。ゼロ値から使える。
//
// 応答は質問の一定時間後（応答遅延 3 µs + 伝搬）に返るので、受信時刻から
// 質問を逆引きできる。同じ機体は 1 ドウェルの間に十数の質問へ連続して
// 応答するので、遅延 τ がほぼ一定の応答列としてまとまる。列の最初と最後の
// 質問方位の中点がビーム中心、τ の平均が双基地距離に対応する。
//
// ドウェルは取り出しの境界をまたぐので、開いている列だけを持ち越す。
// 質問の通し番号は途切れの判定に使い、境界をまたいで数え続ける。
type RunState struct {
	runs []*run
	seq  int64 // 次の質問に振る通し番号
}

// run は開いている応答列。
type run struct {
	replies  []PairedReply
	lastSeq  int64
	lastTau  int64
	modeA    uint16
	hasModeA bool
}

// Pair は Synchronizer が切り出した範囲の質問予定と応答を対応づけ、閉じた
// 列のプロットを時刻順に返す。
//
// 範囲は閉じているので、質問列と応答列を時刻順にマージするだけで対応が
// 決まる。応答 r が属する質問は t_q ≤ t_r − TauMin を満たす最後の質問で、
// τ = t_r − t_q が TauMax 以内なら対になる。範囲の最初の質問より前の応答は、
// 取り出し済みのどの質問からも TauMax 以上離れているので対にならない。
//
// 列は、その最後の質問から MaxGap + 1 個先の質問まで消費し終えた時点で閉じる。
func Pair(st *RunState, stats *Stats, params Params, cfg Config, intg []record.Interrogation, replies []record.Reply) []Plot {
	var plots []Plot
	j := 0
	for i, q := range intg {
		seq := st.seq + int64(i)
		// この質問の区間: t_q + TauMin ≤ t_r < t_q(次) + TauMin
		start := q.Timestamp + params.TauMinNs
		end := int64(math.MaxInt64)
		if i+1 < len(intg) {
			end = intg[i+1].Timestamp + params.TauMinNs
		}
		for ; j < len(replies) && replies[j].Timestamp < start; j++ {
			stats.Unpaired++ // 直前の質問から TauMax 超、または遡る質問が無い
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
			assignToRun(st, cfg, PairedReply{Interrogation: q, Reply: r, TauNs: tau}, seq)
		}
		// この質問まで消費したので、MaxGap+1 個手前より前で終わった列は閉じる
		plots = closeRuns(st, stats, cfg, plots, seq-int64(cfg.MaxGap)-1)
	}
	st.seq += int64(len(intg))
	if len(intg) == 0 {
		// 質問の無い範囲の応答は対にならない
		stats.Unpaired += len(replies) - j
	}
	slices.SortStableFunc(plots, func(a, b Plot) int { return compareInt64(a.Timestamp, b.Timestamp) })
	return plots
}

// CloseRuns は開いている列をすべて閉じてプロットを返す。処理の終わりに呼ぶ。
func CloseRuns(st *RunState, stats *Stats, cfg Config) []Plot {
	var plots []Plot
	for _, ru := range st.runs {
		plots = emit(stats, cfg, plots, ru)
	}
	st.runs = nil
	slices.SortStableFunc(plots, func(a, b Plot) int { return compareInt64(a.Timestamp, b.Timestamp) })
	return plots
}

// assignToRun は対応づいた応答を列へ加える。合う列が無ければ新しく開く。
//
// 合う条件: 列がこの質問の応答をまだ持っていない、τ の差が許容内、
// Mode A 応答なら符号が列のものと一致。複数合えば τ の差が最小の列。
// Mode C の符号は一致を求めない。上昇・降下中は 1 ドウェルの間に 100 ft の
// 境界をまたぐことがあり、それを落とさないため。
//
// 途切れが MaxGap 以内かはここでは見ない。Pair は質問ごとに closeRuns で
// 途切れの大きい列を閉じてから次の質問へ進むので、開いている列は
// すべて条件を満たしている。
func assignToRun(st *RunState, cfg Config, pr PairedReply, seq int64) {
	mode := pr.Interrogation.Mode
	var best *run
	var bestDiff int64
	for _, ru := range st.runs {
		if ru.lastSeq == seq {
			continue // 同じ質問への 2 つ目の応答は別の列
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
		best = &run{}
		st.runs = append(st.runs, best)
	}
	best.replies = append(best.replies, pr)
	best.lastSeq = seq
	best.lastTau = pr.TauNs
	if mode == ModeA && !best.hasModeA {
		best.modeA, best.hasModeA = pr.Reply.Code, true
	}
}

// closeRuns は最後の質問番号が lastSeqAtMost 以下の列を閉じてプロットにする。
func closeRuns(st *RunState, stats *Stats, cfg Config, plots []Plot, lastSeqAtMost int64) []Plot {
	kept := st.runs[:0]
	for _, ru := range st.runs {
		if ru.lastSeq <= lastSeqAtMost {
			plots = emit(stats, cfg, plots, ru)
			continue
		}
		kept = append(kept, ru)
	}
	st.runs = kept
	return plots
}

// emit は閉じた列をプロットにして plots に足す。短い列、Mode A の無い列、
// 高度の決まらない列は捨てる。
func emit(stats *Stats, cfg Config, plots []Plot, ru *run) []Plot {
	stats.Runs++
	if len(ru.replies) < cfg.MinReplies {
		stats.RunsTooShort++
		return plots
	}
	if !ru.hasModeA {
		stats.NoModeA++
		return plots
	}
	pl := makePlot(ru.replies, ru.modeA)
	alt, res := resolveAltitude(pl.Timestamp, ru.replies)
	switch res {
	case altitudeNone:
		stats.NoAltitude++
		return plots
	case altitudeSpread:
		stats.AltitudeSpread++
		return plots
	}
	pl.AltitudeFt = alt
	stats.Plots++
	return append(plots, pl)
}

// makePlot は応答列を 1 プロットに要約する。高度は決めない。
func makePlot(replies []PairedReply, modeA uint16) Plot {
	first, last := replies[0].Interrogation, replies[len(replies)-1].Interrogation
	taus := make([]int64, len(replies))
	for i, r := range replies {
		taus[i] = r.TauNs
	}
	return Plot{
		// 差は非負なので / は床除算
		Timestamp: first.Timestamp + (last.Timestamp-first.Timestamp)/2,
		Azimuth:   midAngle(first.Azimuth, last.Azimuth),
		TauNs:     int64(math.RoundToEven(numeric.MeanInt64(taus))),
		Squawk:    Squawk(modeA),
		Replies:   replies,
	}
}

type altitudeResult int

const (
	altitudeOK altitudeResult = iota
	altitudeNone
	altitudeSpread
)

// resolveAltitude は列の Mode C 応答から高度を 1 つに決める。
//
// 復号できない符号（ガーブル）は除く。残りの最大と最小の差が 100 ft
// 以内なら正常な上昇・降下の境界またぎとみなし、プロット時刻に最も近い
// 応答の高度をとる。それより散っていれば符号が壊れているので決めない。
func resolveAltitude(at int64, replies []PairedReply) (int, altitudeResult) {
	var (
		n, lo, hi, nearest int
		nearestDist        int64
	)
	for _, r := range replies {
		if r.Interrogation.Mode != ModeC {
			continue
		}
		ft, ok := Altitude(r.Reply.Code)
		if !ok {
			continue
		}
		dist := absInt64(r.Interrogation.Timestamp - at)
		if n == 0 {
			lo, hi, nearest, nearestDist = ft, ft, ft, dist
		} else {
			lo, hi = min(lo, ft), max(hi, ft)
			if dist < nearestDist {
				nearest, nearestDist = ft, dist
			}
		}
		n++
	}
	switch {
	case n == 0:
		return 0, altitudeNone
	case hi-lo > 100:
		return 0, altitudeSpread
	}
	return nearest, altitudeOK
}

// midAngle は 2 つの方位の中点を [0, 2pi) で返す。差は短い方の弧でとる。
func midAngle(a, b float64) float64 {
	const twoPi = 2 * math.Pi
	d := math.Mod(b-a, twoPi)
	if d > math.Pi {
		d -= twoPi
	} else if d <= -math.Pi {
		d += twoPi
	}
	m := math.Mod(a+d/2, twoPi)
	if m < 0 {
		m += twoPi
	}
	return m
}
