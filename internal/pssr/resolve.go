package pssr

import (
	"slices"
)

// ResolveState は同一機体のフライトの重複を解消する段が持ち越す記録。
// ゼロ値から使える。
//
// SSR 近傍の反射体経由の像は、τ（双基地距離）が直接波と同じで方位だけが
// 反射体の方向に固定される。Suppress は τ の一致する候補のうち方位の離れた
// ものを両方通し、Track で別の便（フライト）になる。ここで 2 本のフライトが
// 同じ機体であること（一意性の破れ）を検出し、実位置のフライトを決める。
//
// 同じ機体: スコーク一致、高度差が ResolveAltitudeToleranceFt 以内、同時刻に
// 内挿した τ の差が ResolveTauToleranceNs 以内、方位差が
// ResolveAzimuthSeparationRad 超（同じ機体の直接波の断片は組にしない）、を
// ResolveConfirmScans 走査以上満たす組。別の機体が同時に同じスコーク・
// 高度・双基地距離を持ち続ける偶然は無視できる。
//
// 実位置: 像は反射の幾何が成り立つ間しか存在せず、直接波は機体が覆域に
// 居る間ずっと存在するので、像の存在区間は実位置のフライトの存在区間に
// 含まれる。重なりの外（後、無ければ前）に点を持つフライトが実位置、
// 持たない方が像（echo）。後に続く方と前から在った方が食い違う、両方
// 続く、両方に外の点が無い、のときは決めず、重なりの点を ambiguous に
// する。決めたあと同じ組がまた重なれば改めて判定する。
//
// 判定はどちらかのフライトの行方が決まってから。便は Link が繋ぐ幅
// （LinkMaxGapNs）まで途切れうるので、その幅を過ぎるまで終わったとは
// 見なさない。真値との比較では、実機の便の短い途切れの間に像が 1 走査
// 長く残っただけで実機を像にした例があった。
//
// 判定は組の重なり（一致した走査）の開始以降の点に付ける。像のフライトの
// 重なりより前の点は判定の材料が無く、投入の刻みによらず ok のまま。
// 重なりが ResolveMaxHoldScans 走査を超えて続く間は、それより古い点を
// ambiguous として先に出す（出力の遅れを抑える）。
//
// 応答数の多寡や反射体の方位は使わない。反射の無い局では同じ機体の組が
// 生じず、段は何もしない。
type ResolveState struct {
	held      []heldFix
	flights   map[int64]*rflight
	pairs     map[[2]int64]*pair
	watermark int64
}

// rflight は開いているフライト。τ の内挿と存在区間の判定に使う。
type rflight struct {
	id          int64
	squawk      uint16
	pts         []Fix // 時刻順の直近の点（ok のみ）
	first, last int64
}

// pair は同じ機体と疑われるフライトの組。
type pair struct {
	a, b       int64 // a < b
	scans      []int64
	confirmed  bool
	decided    bool
	echo       int64 // 像のフライト。0 なら ambiguous
	start, end int64 // 一致した走査の範囲
}

// resolveTail はフライトごとに持つ直近の点数。τ の内挿に要るのは判定する
// 時刻（透かしから 1 走査あまり前）の前後だけで、存在区間は first/last で
// 持つ。
const resolveTail = 32

// Resolve はフライト ID の付いた点を受け取り、同じ機体のフライトの重複を
// 解消して、判定の決まった点を時刻順に返す。last が真なら保留を全部出す。
func Resolve(st *ResolveState, stats *Stats, params Params, cfg Config, fixes []Fix, last bool) []Fix {
	if st.flights == nil {
		st.flights, st.pairs = map[int64]*rflight{}, map[[2]int64]*pair{}
	}
	halfScan := params.AroundTimeNs / 2
	dropAfter := int64(cfg.TrackMaxMissedScans+1)*params.AroundTimeNs + params.AroundTimeNs/4
	// 同じ機体の判定は相手の次の点が届いてから（1 走査 + 余裕）。点を出すのは
	// さらに組の確定に要る走査数を待ってから。こうすると、点を含む重なりで
	// 確定しうる組は出す前に確定していて、投入の刻みで結果が変わらない
	mature := params.AroundTimeNs + params.AroundTimeNs/4
	release := mature + int64(cfg.ResolveConfirmScans)*params.AroundTimeNs
	maxHold := int64(cfg.ResolveMaxHoldScans) * params.AroundTimeNs

	for _, f := range fixes {
		i, _ := slices.BinarySearchFunc(st.held, f.Timestamp, func(h heldFix, t int64) int {
			return compareInt64(h.fix.Timestamp, t)
		})
		for i < len(st.held) && st.held[i].fix.Timestamp == f.Timestamp {
			i++
		}
		st.held = slices.Insert(st.held, i, heldFix{fix: f})
		st.watermark = max(st.watermark, f.Timestamp)
		if f.Status == FixOK {
			fl := st.flights[f.Flight]
			if fl == nil {
				fl = &rflight{id: f.Flight, squawk: f.Squawk, first: f.Timestamp}
				st.flights[f.Flight] = fl
			}
			fl.last = max(fl.last, f.Timestamp)
			fl.pts = append(fl.pts, f)
			if len(fl.pts) > resolveTail {
				fl.pts = slices.Delete(fl.pts, 0, len(fl.pts)-resolveTail)
			}
		}
	}
	stats.ResolveHeldMax = max(stats.ResolveHeldMax, len(st.held))

	// 同じ機体の検出。相手の前後の点が揃う頃合いになった点から
	for i := range st.held {
		h := &st.held[i]
		if h.assigned || h.fix.Status != FixOK {
			continue
		}
		if !last && h.fix.Timestamp+mature > st.watermark {
			break
		}
		h.assigned = true
		st.matchPoint(stats, cfg, h.fix, halfScan, dropAfter)
	}

	// 組の判定。どちらかが終わり（Link が繋ぎうる幅を過ぎ）、もう一方が
	// それより後の点を持つか同じく終わった組。存在区間のはみ出しで決める
	gone := cfg.LinkMaxGapNs + dropAfter
	for _, p := range st.sortedPairs() {
		if !p.confirmed || p.decided {
			continue
		}
		a, b := st.flights[p.a], st.flights[p.b]
		if a == nil || b == nil {
			continue
		}
		aEnded, bEnded := last || st.watermark-a.last > gone, last || st.watermark-b.last > gone
		if !(aEnded && (bEnded || b.last > a.last+halfScan)) && !(bEnded && (aEnded || a.last > b.last+halfScan)) {
			continue
		}
		aOut := a.first < b.first-halfScan || a.last > b.last+halfScan
		bOut := b.first < a.first-halfScan || b.last > a.last+halfScan
		p.decided = true
		switch {
		case aOut && !bOut:
			p.echo = p.b
		case bOut && !aOut:
			p.echo = p.a
		}
		if p.echo != 0 {
			stats.ResolvedByContinuity++
		} else {
			stats.ResolveAmbiguous++
		}
	}

	// 出す。時刻順を保つため、先頭から出せる分だけ
	n := 0
	for n < len(st.held) {
		h := &st.held[n]
		if !last && h.fix.Status == FixOK {
			if h.fix.Timestamp+release > st.watermark ||
				(h.fix.Timestamp+maxHold > st.watermark && st.blocked(h.fix.Flight, h.fix.Timestamp, halfScan)) {
				break
			}
		}
		n++
	}
	out := make([]Fix, n)
	for k := range n {
		f := st.held[k].fix
		if f.Status == FixOK {
			switch st.verdict(f.Flight, f.Timestamp, halfScan) {
			case FixEcho:
				f.Status = FixEcho
				stats.FixesEcho++
			case FixAmbiguous:
				f.Status = FixAmbiguous
				stats.FixesAmbiguous++
			}
		}
		out[k] = f
	}
	st.held = slices.Delete(st.held, 0, n)
	st.forget(cfg, dropAfter)
	if last {
		st.flights, st.pairs, st.held = map[int64]*rflight{}, map[[2]int64]*pair{}, nil
	}
	return out
}

// matchPoint は点 f（フライト A）と同じ機体に見える他のフライトを探し、
// 組の一致した走査を数える。
func (st *ResolveState) matchPoint(stats *Stats, cfg Config, f Fix, halfScan, dropAfter int64) {
	a := st.flights[f.Flight]
	if a == nil {
		return
	}
	for _, b := range st.sortedFlights() {
		if b.id == a.id || b.squawk != f.Squawk {
			continue
		}
		q, ok := b.at(f.Timestamp, halfScan, dropAfter)
		if !ok {
			continue
		}
		if absInt64(f.TauNs-q.TauNs) > cfg.ResolveTauToleranceNs ||
			absInt(f.AltitudeFt-q.AltitudeFt) > cfg.ResolveAltitudeToleranceFt ||
			angleDiff(f.Azimuth, q.Azimuth) <= cfg.ResolveAzimuthSeparationRad {
			continue
		}
		key := [2]int64{min(a.id, b.id), max(a.id, b.id)}
		p := st.pairs[key]
		if p == nil {
			p = &pair{a: key[0], b: key[1]}
			st.pairs[key] = p
			stats.ResolvePairs++
		}
		// 一致した走査を数える（同じ走査の点は 1 つに数える）
		if n := len(p.scans); n == 0 {
			p.scans = append(p.scans, f.Timestamp)
			p.start, p.end = f.Timestamp, f.Timestamp
		} else if absInt64(f.Timestamp-p.scans[n-1]) > halfScan {
			p.scans = append(p.scans, f.Timestamp)
		}
		p.start, p.end = min(p.start, f.Timestamp), max(p.end, f.Timestamp)
		if !p.confirmed && len(p.scans) >= cfg.ResolveConfirmScans {
			p.confirmed = true
			stats.ResolveConfirmed++
		}
	}
}

// at は時刻 t のフライトの点（τ・高度・方位）を前後の点から内挿する。
// 前後が dropAfter 以内に無ければ、halfScan 以内の点で代える。
func (fl *rflight) at(t, halfScan, dropAfter int64) (Fix, bool) {
	i := slices.IndexFunc(fl.pts, func(x Fix) bool { return x.Timestamp >= t })
	var p, q *Fix
	if i > 0 {
		p = &fl.pts[i-1]
	}
	if i >= 0 {
		q = &fl.pts[i]
	}
	switch {
	case p != nil && q != nil && q.Timestamp-p.Timestamp <= dropAfter:
		w := float64(t-p.Timestamp) / float64(q.Timestamp-p.Timestamp)
		out := *p
		out.TauNs = p.TauNs + int64(w*float64(q.TauNs-p.TauNs))
		out.AltitudeFt = p.AltitudeFt + int(w*float64(q.AltitudeFt-p.AltitudeFt))
		if w > 0.5 {
			out.Azimuth = q.Azimuth
		}
		return out, true
	case q != nil && q.Timestamp-t <= halfScan:
		return *q, true
	case p != nil && t-p.Timestamp <= halfScan:
		return *p, true
	}
	return Fix{}, false
}

// blocked はフライトの時刻 t の点が、判定待ちの組の重なりに入っているか。
func (st *ResolveState) blocked(flight, t, halfScan int64) bool {
	for _, p := range st.pairs {
		if p.confirmed && !p.decided && (p.a == flight || p.b == flight) && t >= p.start-halfScan {
			return true
		}
	}
	return false
}

// verdict はフライトの時刻 t の点の判定。像なら一致した走査の開始以降
// ずっと echo、決められなかった組と判定待ちの組（上限まで待った点）は
// 一致した走査の区間だけ ambiguous、それ以外は ok。
func (st *ResolveState) verdict(flight, t, halfScan int64) FixStatus {
	out := FixOK
	for _, p := range st.pairs {
		if !p.confirmed || (p.a != flight && p.b != flight) || t < p.start-halfScan {
			continue
		}
		if p.decided && p.echo == flight {
			return FixEcho
		}
		if (!p.decided || p.echo == 0) && t <= p.end+halfScan {
			out = FixAmbiguous
		}
	}
	return out
}

func (st *ResolveState) sortedFlights() []*rflight {
	out := make([]*rflight, 0, len(st.flights))
	for _, fl := range st.flights {
		out = append(out, fl)
	}
	slices.SortFunc(out, func(x, y *rflight) int { return compareInt64(x.id, y.id) })
	return out
}

func (st *ResolveState) sortedPairs() []*pair {
	out := make([]*pair, 0, len(st.pairs))
	for _, p := range st.pairs {
		out = append(out, p)
	}
	slices.SortFunc(out, func(x, y *pair) int {
		if c := compareInt64(x.a, y.a); c != 0 {
			return c
		}
		return compareInt64(x.b, y.b)
	})
	return out
}

// forget は続きの来ないフライトと、両方のフライトが消えた組を忘れる。
// 判定済みの組はその重なりの点が出るまで残す。
func (st *ResolveState) forget(cfg Config, dropAfter int64) {
	cut := st.watermark - cfg.LinkMaxGapNs - dropAfter
	for id, fl := range st.flights {
		if fl.last < cut && !st.blocked(id, fl.last, 0) {
			delete(st.flights, id)
		}
	}
	for key, p := range st.pairs {
		_, aOK := st.flights[p.a]
		_, bOK := st.flights[p.b]
		if !aOK && !bOK && p.end < cut {
			delete(st.pairs, key)
		}
	}
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
