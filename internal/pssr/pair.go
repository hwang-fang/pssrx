package pssr

import (
	"math"
	"slices"

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
//
// 開いている列は「走査が読む値」と「候補になった列だけが読む値」に分けて
// 持つ。応答 1 件の割り当てで開いている列（数十本）を全部見るが、τ の
// 許容差を通るのは平均 0.05 本しかない。したがって走査で読むのは各列の
// 最後の τ だけにし、それを連続した配列 tau に置く。数十本なら数百バイトで
// キャッシュに収まり、列ごとに散らばった構造体を辿らずに済む。列の残り
// （応答、最後の質問番号、Mode A 符号）は cold に置き、許容差を通った
// ときだけ id 経由で読む。
//
// tau / id / cold の並びは列を開いた順で、閉じるときは順序を保って詰める。
// 走査順が生成順であることで、τ の差が同点なら生成の古い列が選ばれ、
// 同じ質問で閉じた列のプロットも生成順に出る。
//
// cold の空き slot は free で再利用する。ただし応答のスライスはプロットに
// なった列（Plot.Replies がそのまま持つ）では手放し、捨てた列でだけ
// 使い回す。
type RunState struct {
	tau  []int64   // 開いている列の最後の τ。割り当ての走査が読むのはこれだけ
	id   []int32   // tau と同じ並びで、cold への添字
	cold []runCold // 列の本体。free の slot は空き
	free []int32   // cold の空き slot
	seq  int64     // 次の質問に振る通し番号
}

// runCold は開いている応答列のうち、走査では読まない部分。
type runCold struct {
	replies  []PairedReply
	lastSeq  int64
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
			assignToRun(st, stats, cfg, PairedReply{Interrogation: q, Reply: r, TauNs: tau}, seq)
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
	for _, k := range st.id {
		plots = emitAndFree(st, stats, cfg, plots, k)
	}
	st.tau, st.id = st.tau[:0], st.id[:0]
	slices.SortStableFunc(plots, func(a, b Plot) int { return compareInt64(a.Timestamp, b.Timestamp) })
	return plots
}

// assignToRun は対応づいた応答を列へ加える。合う列が無ければ新しく開く。
//
// 合う条件: τ の差が許容内、列がこの質問の応答をまだ持っていない、
// Mode A 応答なら符号が列のものと一致。複数合えば τ の差が最小の列
// （同点なら生成の古い列）。Mode C の符号は一致を求めない。上昇・降下中は
// 1 ドウェルの間に 100 ft の境界をまたぐことがあり、それを落とさないため。
//
// 判定は τ の許容差を先に置く。ほとんどの列はここで外れ、tau 配列だけを
// 読んで済む。cold を読むのは許容差を通った列だけ。
//
// 途切れが MaxGap 以内かはここでは見ない。Pair は質問ごとに closeRuns で
// 途切れの大きい列を閉じてから次の質問へ進むので、開いている列は
// すべて条件を満たしている。
func assignToRun(st *RunState, stats *Stats, cfg Config, pr PairedReply, seq int64) {
	stats.Assignments++
	stats.OpenRunsTotal += int64(len(st.tau))
	mode := pr.Interrogation.Mode
	best := -1
	var bestDiff int64
	for i, t := range st.tau {
		diff := absInt64(pr.TauNs - t)
		if diff > cfg.TauToleranceNs {
			continue
		}
		c := &st.cold[st.id[i]]
		if c.lastSeq == seq {
			continue // 同じ質問への 2 つ目の応答は別の列
		}
		if mode == ModeA && c.hasModeA && c.modeA != pr.Reply.Code {
			continue
		}
		if best < 0 || diff < bestDiff {
			best, bestDiff = i, diff
		}
	}
	var c *runCold
	if best < 0 {
		k := st.newRun()
		st.tau = append(st.tau, pr.TauNs)
		st.id = append(st.id, k)
		c = &st.cold[k]
	} else {
		st.tau[best] = pr.TauNs
		c = &st.cold[st.id[best]]
	}
	c.replies = append(c.replies, pr)
	c.lastSeq = seq
	if mode == ModeA && !c.hasModeA {
		c.modeA, c.hasModeA = pr.Reply.Code, true
	}
}

// newRun は cold の空き slot を 1 つ確保して添字を返す。応答のスライスは
// 捨てた列のものを容量ごと引き継ぐ。
func (st *RunState) newRun() int32 {
	if n := len(st.free); n > 0 {
		k := st.free[n-1]
		st.free = st.free[:n-1]
		return k
	}
	st.cold = append(st.cold, runCold{})
	return int32(len(st.cold) - 1)
}

// closeRuns は最後の質問番号が lastSeqAtMost 以下の列を閉じてプロットにする。
// 残る列は順序を保って詰める。
func closeRuns(st *RunState, stats *Stats, cfg Config, plots []Plot, lastSeqAtMost int64) []Plot {
	n := 0
	for i, k := range st.id {
		if st.cold[k].lastSeq <= lastSeqAtMost {
			plots = emitAndFree(st, stats, cfg, plots, k)
			continue
		}
		st.tau[n], st.id[n] = st.tau[i], k
		n++
	}
	st.tau, st.id = st.tau[:n], st.id[:n]
	return plots
}

// emitAndFree は列 k を閉じてプロットにし、slot を空きに返す。
func emitAndFree(st *RunState, stats *Stats, cfg Config, plots []Plot, k int32) []Plot {
	c := &st.cold[k]
	plots, kept := emit(stats, cfg, plots, c)
	if kept {
		c.replies = nil // Plot.Replies が持つ。使い回さない
	} else {
		c.replies = c.replies[:0]
	}
	c.lastSeq, c.modeA, c.hasModeA = 0, 0, false
	st.free = append(st.free, k)
	return plots
}

// emit は閉じた列をプロットにして plots に足す。短い列、Mode A の無い列、
// 高度の決まらない列、高度が実在の機体の上限を超える列は捨てる。
// 第 2 戻り値はプロットにしたか（応答のスライスを Plot が持つか）。
func emit(stats *Stats, cfg Config, plots []Plot, c *runCold) ([]Plot, bool) {
	stats.Runs++
	if len(c.replies) == 1 {
		stats.RunsSingle++
	}
	if len(c.replies) < cfg.MinReplies {
		stats.RunsTooShort++
		return plots, false
	}
	if !c.hasModeA {
		stats.NoModeA++
		return plots, false
	}
	pl := makePlot(c.replies, c.modeA)
	alt, res := resolveAltitude(pl.Timestamp, c.replies)
	switch res {
	case altitudeNone:
		stats.NoAltitude++
		return plots, false
	case altitudeSpread:
		stats.AltitudeSpread++
		return plots, false
	}
	if alt > cfg.MaxAltitudeFt {
		stats.AltitudeTooHigh++
		return plots, false
	}
	pl.AltitudeFt = alt
	stats.Plots++
	return append(plots, pl), true
}

// makePlot は応答列を 1 プロットに要約する。高度は決めない。
func makePlot(replies []PairedReply, modeA uint16) Plot {
	first, last := replies[0].Interrogation, replies[len(replies)-1].Interrogation
	// τ の平均は整数の総和を 1 回だけ割る（numeric.MeanInt64 と同じ規約）。
	// 列は高々数十件、τ は数 ms なので int64 は溢れない
	var sum int64
	for _, r := range replies {
		sum += r.TauNs
	}
	return Plot{
		// 差は非負なので / は床除算
		Timestamp: first.Timestamp + (last.Timestamp-first.Timestamp)/2,
		Azimuth:   midAngle(first.Azimuth, last.Azimuth),
		TauNs:     int64(math.RoundToEven(float64(sum) / float64(len(replies)))),
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
