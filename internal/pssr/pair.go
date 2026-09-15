package pssr

import (
	"fmt"
	"log/slog"
	"math"
	"slices"
	"sort"

	"pssrx/internal/numeric"
	"pssrx/internal/store"
)

// PairState は対応づけがブロックをまたいで持ち越す記録。ゼロ値から使える。
//
// 応答は質問の一定時間後（応答遅延 3 µs + 伝搬）に返るので、受信時刻から
// 質問を逆引きできる。同じ機体は 1 ドウェルの間に十数の質問へ連続して
// 応答するので、遅延 τ がほぼ一定の応答列としてまとまる。列の最初と最後の
// 質問方位の中点がビーム中心、τ の平均が双基地距離に対応する。
//
// 質問予定はブロック N の分が N+1 で確定するため、対応する質問予定が
// まだ無い応答は保留する。
type PairState struct {
	// 質問予定の緩衝。seq = baseSeq + index で質問に通し番号を振る。
	// 列の途切れの判定はこの番号の差で行う。
	intg    []store.Intg
	baseSeq int64
	// intgFinalUpTo より前の質問予定は出揃っている。
	intgFinalUpTo int64

	// pending は対応する質問予定がまだ確定していない応答。時刻順。
	pending []store.AData
	// processedUpTo より前の応答はすべて処理済み。
	processedUpTo int64

	runs []*run
}

// run は開いている応答列。
type run struct {
	replies  []PairedReply
	lastSeq  int64
	lastTau  int64
	modeA    uint16
	hasModeA bool
}

// Pair は応答を質問予定と対応づけ、閉じた列のプロットを返す。
//
// intg は新たに確定した質問予定で、時刻順・前回の続きでなければならない。
// intgFinalUpTo より前の質問予定はこれで出揃ったものとして扱い、遅延の
// 窓がその内側に収まる応答だけを対応づける。残りは次回まで保留する。
// last が真なら保留と開いている列をすべて処理して返す。
//
// 手順:
//
//  1. 質問予定を緩衝に足し、応答を保留に足して時刻順にする
//  2. 遡る範囲の質問予定が確定している応答から順に、質問を逆引きして列に入れる
//  3. もう応答の来ない列を閉じてプロットにする
//  4. 参照されなくなった質問予定を緩衝から落とす
func Pair(st *PairState, stats *Stats, params Params, cfg Config, log *slog.Logger,
	replies []store.AData, intg []store.Intg, intgFinalUpTo int64, last bool) ([]Plot, error) {
	if log == nil {
		log = slog.Default()
	}
	for i, d := range intg {
		if len(st.intg) > 0 && d.Timestamp < st.intg[len(st.intg)-1].Timestamp {
			return nil, fmt.Errorf("質問予定の時刻が逆行: [%d] %d < %d", i, d.Timestamp, st.intg[len(st.intg)-1].Timestamp)
		}
		st.intg = append(st.intg, d)
	}
	st.intgFinalUpTo = max(st.intgFinalUpTo, intgFinalUpTo)

	stats.Replies += len(replies)
	st.pending = append(st.pending, replies...)
	slices.SortStableFunc(st.pending, func(a, b store.AData) int {
		return compareInt64(a.Timestamp, b.Timestamp)
	})

	var plots []Plot
	n := 0
	for ; n < len(st.pending); n++ {
		r := st.pending[n]
		// 遡る質問は t_q <= t_r − TauMin。その範囲が確定していなければ待つ
		if !last && r.Timestamp-params.TauMinNs >= st.intgFinalUpTo {
			break
		}
		if r.Timestamp < st.processedUpTo {
			// 時刻順の前提が崩れている。保留から抜けた後に古い応答が
			// 来た場合で、列の判定を狂わせるので落として記録する
			log.Warn("処理済みより古い応答を受け取った", "reply", r.Timestamp, "processed_up_to", st.processedUpTo)
			continue
		}
		st.processedUpTo = r.Timestamp
		plots = closeFinished(st, stats, params, cfg, plots)
		if pr, seq, ok := pairReply(st, stats, params, r); ok {
			assignToRun(st, cfg, pr, seq)
		}
	}
	st.pending = slices.Delete(st.pending, 0, n)

	plots = closeFinished(st, stats, params, cfg, plots)
	if last {
		for _, ru := range st.runs {
			plots = emit(stats, params, cfg, plots, ru)
		}
		st.runs = nil
	}
	trimIntg(st, params)
	return plots, nil
}

// pairReply は応答に対応する質問とその通し番号を決める。
func pairReply(st *PairState, stats *Stats, params Params, r store.AData) (PairedReply, int64, bool) {
	latest := r.Timestamp - params.TauMinNs
	// t_q <= latest を満たす最後の質問
	i := sort.Search(len(st.intg), func(k int) bool { return st.intg[k].Timestamp > latest }) - 1
	if i < 0 {
		stats.NoInterrogation++
		return PairedReply{}, 0, false
	}
	q := st.intg[i]
	tau := r.Timestamp - q.Timestamp
	histogramAdd(&stats.Tau, tau)
	if tau > params.TauMaxNs {
		stats.AboveMax++
		return PairedReply{}, 0, false
	}
	stats.Paired++
	return PairedReply{Interrogation: q, Reply: r, TauNs: tau}, st.baseSeq + int64(i), true
}

// assignToRun は対応づいた応答を列へ加える。合う列が無ければ新しく開く。
//
// 合う条件: 質問の通し番号が列の最後より後で途切れが MaxGap 以内、
// τ の差が許容内、Mode A 応答なら符号が列のものと一致。複数合えば
// τ の差が最小の列。Mode C の符号は一致を求めない。上昇・降下中は
// 1 ドウェルの間に 100 ft の境界をまたぐことがあり、それを落とさないため。
func assignToRun(st *PairState, cfg Config, pr PairedReply, seq int64) {
	mode := pr.Interrogation.Mode
	var best *run
	var bestDiff int64
	for _, ru := range st.runs {
		gap := seq - ru.lastSeq - 1
		if gap < 0 || gap > int64(cfg.MaxGap) {
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

// closeFinished は、もう応答が来ない列を閉じてプロットにする。
//
// 列の最後の質問から MaxGap+1 個先の質問への応答は、その質問時刻 + TauMax
// までに届く。処理済みの応答がそこを過ぎていれば、列に加わる応答は
// 残っていない。その質問がまだ無ければ（質問予定が未確定）開けておく。
func closeFinished(st *PairState, stats *Stats, params Params, cfg Config, plots []Plot) []Plot {
	kept := st.runs[:0]
	for _, ru := range st.runs {
		k := ru.lastSeq + int64(cfg.MaxGap) + 1 - st.baseSeq
		if k < int64(len(st.intg)) && st.intg[k].Timestamp+params.TauMaxNs <= st.processedUpTo {
			plots = emit(stats, params, cfg, plots, ru)
			continue
		}
		kept = append(kept, ru)
	}
	st.runs = kept
	return plots
}

// emit は閉じた列をプロットにして plots に足す。短い列と高度の決まらない列は捨てる。
func emit(stats *Stats, params Params, cfg Config, plots []Plot, ru *run) []Plot {
	stats.Runs++
	if len(ru.replies) < cfg.MinReplies {
		stats.RunsTooShort++
		return plots
	}
	if !ru.hasModeA {
		stats.NoModeA++
		return plots
	}
	pl := makePlot(params, ru.replies, ru.modeA)
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
func makePlot(params Params, replies []PairedReply, modeA uint16) Plot {
	first, last := replies[0].Interrogation, replies[len(replies)-1].Interrogation
	taus := make([]int64, len(replies))
	for i, r := range replies {
		taus[i] = r.TauNs
	}
	return Plot{
		SSRID:     params.SSRID,
		StationID: params.StationID,
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

// trimIntg は参照されなくなった質問予定を緩衝から落とす。
//
// 残す必要があるのは、開いている列の閉じ判定に使う分（列の最後の質問から
// 先）と、保留中・これから来る応答が遡りうる分（処理済み時刻 − TauMax 以降）。
func trimIntg(st *PairState, params Params) {
	keepFrom := st.processedUpTo - params.TauMaxNs
	if len(st.pending) > 0 {
		keepFrom = min(keepFrom, st.pending[0].Timestamp-params.TauMaxNs)
	}
	k := int64(sort.Search(len(st.intg), func(i int) bool { return st.intg[i].Timestamp >= keepFrom }))
	for _, ru := range st.runs {
		k = min(k, ru.lastSeq-st.baseSeq)
	}
	if k <= 0 {
		return
	}
	st.intg = slices.Delete(st.intg, 0, int(k))
	st.baseSeq += k
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
