// Package pssr は測定局で受信した Mode A/C 応答を SSR の質問予定表と
// 対応づけ、機体ごと・ドウェルごとのプロットにまとめる。
//
// 応答は質問の一定時間後（応答遅延 3 µs + 伝搬）に返るので、受信時刻から
// 質問を逆引きできる。同じ機体は 1 ドウェルの間に十数の質問へ連続して
// 応答するので、遅延 τ がほぼ一定の応答列としてまとまる。列の最初と最後の
// 質問方位の中点がビーム中心、τ の平均が双基地距離に対応する。
//
// 段の入口は Feed で、ブロックごとに応答と質問予定を受け、閉じた列の
// プロットを返す。質問予定はブロック N の分が N+1 で確定するため、
// 対応する質問予定がまだ無い応答は内部に保留する。
package pssr

import (
	"fmt"
	"log/slog"
	"math"
	"slices"
	"sort"

	"pssrx/internal/numeric"
	"pssrx/internal/pattern"
	"pssrx/internal/store"
)

// 質問種別のデータ上のコード。応答符号の意味（スコークか高度か）を決める。
var (
	ModeA = pattern.ModeCode['A']
	ModeC = pattern.ModeCode['C']
)

// Params は対応づけに必要な、局と SSR の組に固有の値。設定からの導出は
// pipeline が担う。
type Params struct {
	SSRID     string
	StationID string // 応答を受信した局
	// TauMinNs は取りうる最小の遅延 [ns]。応答遅延 + 基線長 / c。
	// これより短い経路は無いので、遅延がこれを下回る質問は候補にならない。
	TauMinNs int64
	// TauMaxNs は取りうる最大の遅延 [ns]。応答遅延 + (2·覆域 + 基線長) / c。
	// これを超える応答は捨てる。PRI より小さくなければ前後の質問と取り違える。
	TauMaxNs int64
	// AroundTimeNs は SSR の走査周期 [ns]。同じ走査のプロットの判定に使う。
	AroundTimeNs int64
	// MaxRangeM は SSR の覆域 [m]。位置の探索範囲の上限に使う。
	MaxRangeM float64
}

// Config は対応づけと列の形成に使う定数。局や SSR によらない。
type Config struct {
	// TransponderDelayNs は質問（P3）から応答（F1）までの応答遅延 [ns]。
	// Mode A/C の公称値 3.0 µs。公差 ±0.5 µs は TauToleranceNs で吸収する。
	TransponderDelayNs int64
	// TauToleranceNs は同じ列とみなす τ の差の上限 [ns]。応答遅延の公差
	// ±0.5 µs が支配的で、1 ドウェル内の機体の移動は 100 ns に満たない。
	TauToleranceNs int64
	// MaxGap は列の途中で応答の無い質問を何回まで許すか。これを超えて
	// 途切れたら列を閉じる。
	MaxGap int
	// MinReplies は列として残す最小の応答数。これ未満は FRUIT とみなして捨てる。
	MinReplies int

	// 以下は幽霊抑圧（Suppressor）の閾値。
	//
	// SameScanFraction は同じ走査とみなす時刻差の上限を走査周期に対する
	// 割合で表す。反射体の方向は機体と無関係なので方位では絞らない。
	SameScanFraction float64
	// AltitudeToleranceFt は同じ機体とみなす高度差の上限 [ft]。同じ走査の
	// 中でも最大 2 秒ずれるので、上昇・降下中は 100 ft の境界をまたぐ。
	AltitudeToleranceFt int
	// DirectTauToleranceNs は直接照射（主ビーム・サイドローブ・ガーブルで
	// 割れた列）とみなす τ の幅 [ns]。同じ走査内の機体の移動（2 秒 ×
	// 250 m/s ≈ 1.7 µs）を吸収し、反射（経路差 6〜30 km ≈ 20〜100 µs）
	// とは十分離れる値にする。
	DirectTauToleranceNs int64
}

// DefaultConfig は既定の閾値。実データの分布を見て調整する。
func DefaultConfig() Config {
	return Config{
		TransponderDelayNs: 3000, TauToleranceNs: 1000, MaxGap: 2, MinReplies: 3,
		SameScanFraction: 0.75, AltitudeToleranceFt: 200, DirectTauToleranceNs: 5000,
	}
}

// PairedReply は質問と対応づいた応答 1 件。
type PairedReply struct {
	Interrogation store.Intg
	Reply         store.AData
	TauNs         int64 // 受信時刻 − 質問時刻
}

// Plot は 1 機体 × 1 ドウェルの要約。
type Plot struct {
	SSRID     string
	StationID string
	Timestamp int64   // 列の最初と最後の質問時刻の中点 [ns]
	Azimuth   float64 // 列の最初と最後の質問方位の中点 [rad], [0, 2pi)
	TauNs     int64   // τ の平均（偶数丸め）
	ModeA     uint16  // Mode A 応答の生符号。HasModeA が偽なら無効
	HasModeA  bool
	Squawk    uint16   // ModeA を復号したスコーク（8 進 4 桁）
	ModeC     []uint16 // Mode C 応答の生符号。出現順。列の中で変わりうる
	// AltitudeFt は列の Mode C 応答から決めた気圧高度 [ft]。
	// 復号できる符号の高度が 100 ft 以内に収まるとき、プロット時刻に
	// 最も近い応答の高度をとる。決まらない列はプロットにしない。
	AltitudeFt int
	Replies    []PairedReply
}

// Stats は処理量と棄却理由ごとの件数。
type Stats struct {
	Replies         int // 受け取った応答
	NoInterrogation int // 遡れる質問が無い（期間の先頭や質問予定の欠け）
	AboveMax        int // 遅延が TauMax を超えた
	Paired          int // 質問と対応づいた
	Runs            int // 閉じた列
	RunsTooShort    int // 閉じたが MinReplies 未満で捨てた
	NoAltitude      int // Mode C 応答が無い、または全部復号できない
	AltitudeSpread  int // 復号した高度が 100 ft を超えて散っている（ガーブル）
	Plots           int
	Tau             Histogram
}

// Histogram は τ の分布。対応づけの窓（TauMin, TauMax）の妥当性を見るためのもの。
type Histogram struct {
	BinNs  int64
	Counts []int // [k*BinNs, (k+1)*BinNs)
	Over   int
}

func (h *Histogram) add(v int64) {
	k := v / h.BinNs
	if k < 0 || k >= int64(len(h.Counts)) {
		h.Over++
		return
	}
	h.Counts[k]++
}

// Pairer は対応づけと列の形成を行う。ブロックをまたぐ状態を持つ。
type Pairer struct {
	params Params
	cfg    Config
	log    *slog.Logger

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

	runs  []*run
	stats Stats
}

type run struct {
	replies  []PairedReply
	lastSeq  int64
	lastTau  int64
	modeA    uint16
	hasModeA bool
}

// New は Pairer を作る。
func New(params Params, cfg Config, log *slog.Logger) (*Pairer, error) {
	if log == nil {
		log = slog.Default()
	}
	if params.TauMinNs < 0 || params.TauMaxNs <= params.TauMinNs {
		return nil, fmt.Errorf("遅延の窓が不正: TauMin=%d TauMax=%d", params.TauMinNs, params.TauMaxNs)
	}
	if cfg.TransponderDelayNs < 0 || cfg.TauToleranceNs <= 0 || cfg.MaxGap < 0 || cfg.MinReplies < 1 {
		return nil, fmt.Errorf("閾値が不正: %+v", cfg)
	}
	const bins = 64
	return &Pairer{
		params: params, cfg: cfg, log: log,
		stats: Stats{Tau: Histogram{
			// 上限の 1.25 倍までを見る。窓のすぐ外に何があるかを確かめるため
			BinNs:  (params.TauMaxNs*5/4 + bins - 1) / bins,
			Counts: make([]int, bins),
		}},
	}, nil
}

// Stats は現時点の集計。
func (p *Pairer) Stats() Stats { return p.stats }

// Feed は応答と質問予定を受け取り、閉じた列のプロットを返す。
//
// intg は新たに確定した質問予定で、時刻順・前回の続きでなければならない。
// intgFinalUpTo より前の質問予定はこれで出揃ったものとして扱い、遅延の
// 窓がその内側に収まる応答だけを対応づける。残りは次回まで保留する。
// last が真なら保留と開いている列をすべて処理して返す。
func (p *Pairer) Feed(replies []store.AData, intg []store.Intg, intgFinalUpTo int64, last bool) ([]Plot, error) {
	if err := p.appendIntg(intg); err != nil {
		return nil, err
	}
	p.intgFinalUpTo = max(p.intgFinalUpTo, intgFinalUpTo)

	p.stats.Replies += len(replies)
	p.pending = append(p.pending, replies...)
	slices.SortStableFunc(p.pending, func(a, b store.AData) int {
		return compareInt64(a.Timestamp, b.Timestamp)
	})

	var plots []Plot
	n := 0
	for ; n < len(p.pending); n++ {
		r := p.pending[n]
		// 遡る質問は t_q <= t_r − TauMin。その範囲が確定していなければ待つ
		if !last && r.Timestamp-p.params.TauMinNs >= p.intgFinalUpTo {
			break
		}
		if r.Timestamp < p.processedUpTo {
			// 時刻順の前提が崩れている。保留から抜けた後に古い応答が
			// 来た場合で、列の判定を狂わせるので落として記録する
			p.log.Warn("処理済みより古い応答を受け取った", "reply", r.Timestamp, "processed_up_to", p.processedUpTo)
			continue
		}
		p.processedUpTo = r.Timestamp
		plots = p.closeFinished(plots)
		if pr, seq, ok := p.pair(r); ok {
			p.assign(pr, seq)
		}
	}
	p.pending = slices.Delete(p.pending, 0, n)

	plots = p.closeFinished(plots)
	if last {
		for _, ru := range p.runs {
			plots = p.emit(plots, ru)
		}
		p.runs = nil
	}
	p.trimIntg()
	return plots, nil
}

func (p *Pairer) appendIntg(intg []store.Intg) error {
	for i, d := range intg {
		if len(p.intg) > 0 && d.Timestamp < p.intg[len(p.intg)-1].Timestamp {
			return fmt.Errorf("質問予定の時刻が逆行: [%d] %d < %d", i, d.Timestamp, p.intg[len(p.intg)-1].Timestamp)
		}
		p.intg = append(p.intg, d)
	}
	return nil
}

// pair は応答に対応する質問とその通し番号を決める。
func (p *Pairer) pair(r store.AData) (PairedReply, int64, bool) {
	latest := r.Timestamp - p.params.TauMinNs
	// t_q <= latest を満たす最後の質問
	i := sort.Search(len(p.intg), func(k int) bool { return p.intg[k].Timestamp > latest }) - 1
	if i < 0 {
		p.stats.NoInterrogation++
		return PairedReply{}, 0, false
	}
	q := p.intg[i]
	tau := r.Timestamp - q.Timestamp
	p.stats.Tau.add(tau)
	if tau > p.params.TauMaxNs {
		p.stats.AboveMax++
		return PairedReply{}, 0, false
	}
	p.stats.Paired++
	return PairedReply{Interrogation: q, Reply: r, TauNs: tau}, p.baseSeq + int64(i), true
}

// assign は対応づいた応答を列へ加える。合う列が無ければ新しく開く。
//
// 合う条件: 質問の通し番号が列の最後より後で途切れが MaxGap 以内、
// τ の差が許容内、Mode A 応答なら符号が列のものと一致。複数合えば
// τ の差が最小の列。Mode C の符号は一致を求めない。上昇・降下中は
// 1 ドウェルの間に 100 ft の境界をまたぐことがあり、それを落とさないため。
func (p *Pairer) assign(pr PairedReply, seq int64) {
	mode := pr.Interrogation.Mode
	var best *run
	var bestDiff int64
	for _, ru := range p.runs {
		gap := seq - ru.lastSeq - 1
		if gap < 0 || gap > int64(p.cfg.MaxGap) {
			continue
		}
		diff := absInt64(pr.TauNs - ru.lastTau)
		if diff > p.cfg.TauToleranceNs {
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
		p.runs = append(p.runs, best)
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
func (p *Pairer) closeFinished(plots []Plot) []Plot {
	kept := p.runs[:0]
	for _, ru := range p.runs {
		k := ru.lastSeq + int64(p.cfg.MaxGap) + 1 - p.baseSeq
		if k < int64(len(p.intg)) && p.intg[k].Timestamp+p.params.TauMaxNs <= p.processedUpTo {
			plots = p.emit(plots, ru)
			continue
		}
		kept = append(kept, ru)
	}
	p.runs = kept
	return plots
}

func (p *Pairer) emit(plots []Plot, ru *run) []Plot {
	p.stats.Runs++
	if len(ru.replies) < p.cfg.MinReplies {
		p.stats.RunsTooShort++
		return plots
	}
	pl := p.plot(ru)
	alt, ok := resolveAltitude(pl.Timestamp, ru.replies)
	switch ok {
	case altitudeNone:
		p.stats.NoAltitude++
		return plots
	case altitudeSpread:
		p.stats.AltitudeSpread++
		return plots
	}
	pl.AltitudeFt = alt
	p.stats.Plots++
	return append(plots, pl)
}

func (p *Pairer) plot(ru *run) Plot {
	first, last := ru.replies[0].Interrogation, ru.replies[len(ru.replies)-1].Interrogation
	taus := make([]int64, len(ru.replies))
	var modeC []uint16
	for i, r := range ru.replies {
		taus[i] = r.TauNs
		if r.Interrogation.Mode == ModeC {
			modeC = append(modeC, r.Reply.Code)
		}
	}
	return Plot{
		SSRID:     p.params.SSRID,
		StationID: p.params.StationID,
		// 差は非負なので / は床除算
		Timestamp: first.Timestamp + (last.Timestamp-first.Timestamp)/2,
		Azimuth:   midAngle(first.Azimuth, last.Azimuth),
		TauNs:     int64(math.RoundToEven(numeric.MeanInt64(taus))),
		ModeA:     ru.modeA,
		HasModeA:  ru.hasModeA,
		Squawk:    Squawk(ru.modeA),
		ModeC:     modeC,
		Replies:   ru.replies,
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
func (p *Pairer) trimIntg() {
	keepFrom := p.processedUpTo - p.params.TauMaxNs
	if len(p.pending) > 0 {
		keepFrom = min(keepFrom, p.pending[0].Timestamp-p.params.TauMaxNs)
	}
	k := int64(sort.Search(len(p.intg), func(i int) bool { return p.intg[i].Timestamp >= keepFrom }))
	for _, ru := range p.runs {
		k = min(k, ru.lastSeq-p.baseSeq)
	}
	if k <= 0 {
		return
	}
	p.intg = slices.Delete(p.intg, 0, int(k))
	p.baseSeq += k
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

func absInt64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func compareInt64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
