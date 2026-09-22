package tracking

import (
	"fmt"
	"math"
	"slices"
	"strconv"
)

// FlightState は連鎖を便に束ねる段が持ち越す記録。ゼロ値から使える。
//
// 便は「同じ機体の 1 回の飛行」で、出力の分割単位。連鎖（Link）は運動学
// だけで繋ぐので、覆域の穴・ガーブル・unconfirmed に落ちた区間、像の分岐で
// 1 回の飛行が何本にも割れる（真値との比較で 1 機体あたり 7 本）。
//
// 個別スコーク（NonUniqueSquawks に無いもの）は機体の識別子とみなし、
// 同じスコークの連鎖を切れ目 FlightMaxGapNs まで同じ便に束ねる。時間の
// 重なる連鎖（像・重複）も同じ便（同じ機体の別の位置）。位置や高度の
// 整合は見ない。1 点どうしの比較は方位の外れ値（遠方で数十 km）や
// ガーブルした高度で外れ、真値では同じ機体なのに便を割った。真値との
// 比較（KX00 2 時間、326 スコーク）では、個別スコークが別の機体に使われた
// 例は 0 で、スコークだけで束ねても別の機体が混ざった便は 0 だった。
// 10 分以内にスコークが別の機体に再割当てされれば 1 つの便に 2 回の飛行が
// 入るが、その頻度は低いとみなす。
//
// 非個別スコーク（1200 など）はスコークで機体を区別できないので、便は
// 連鎖そのもの。
//
// 点数（ok / echo / ambiguous）が FlightMinPoints に満たない便は noise。
// 点数は便が閉じる（最後の点から FlightMaxGapNs）まで決まらないので、
// 満たすまでの点は保留し、満たしたら以後は素通しにする。便が閉じても
// 満たなければ noise として出す。noise の点は保留のぶん遅れて出るので、
// 出力は時刻順にならない（便のファイルは Sink が並べ直す）。
type FlightState struct {
	flights   map[int64]*voyage
	bySquawk  map[uint16][]int64 // 開いている便（個別スコークのみ）
	byChain   map[int64]int64    // 連鎖 → 便
	held      []Fix              // 点数が足りない便の点。時刻順
	nonUnique map[uint16]bool
	nextID    int64
	watermark int64
}

// voyage は開いている便。
type voyage struct {
	id          int64
	squawk      uint16
	first, last int64
	points      int  // ok / echo / ambiguous の点数
	passed      bool // 点数が足りて素通しにした
}

// Flight は連鎖 ID の付いた点を受け取り、便 ID と noise の判定を付けて返す。
// 点数の足りている便の点は届いた順に、足りない便の点は判定が決まってから出す。
func Flight(st *FlightState, stats *Stats, params Params, cfg Config, fixes []Fix, last bool) []Fix {
	if st.flights == nil {
		st.flights, st.bySquawk, st.byChain = map[int64]*voyage{}, map[uint16][]int64{}, map[int64]int64{}
		st.nonUnique, _ = parseSquawks(cfg.NonUniqueSquawks)
	}
	var out []Fix
	for _, f := range fixes {
		st.watermark = max(st.watermark, f.Timestamp)
		v := st.assign(stats, cfg, f)
		f.Flight = v.id
		v.last = max(v.last, f.Timestamp)
		if f.Status != FixUnconfirmed {
			v.points++
		}
		if v.passed {
			out = append(out, f)
			continue
		}
		st.held = append(st.held, f)
		if v.points >= cfg.FlightMinPoints {
			// 足りた。保留していた分をまとめて出す
			v.passed = true
			out, st.held = releaseFlight(out, st.held, v.id, false, stats)
		}
	}
	stats.FlightHeldMax = max(stats.FlightHeldMax, len(st.held))

	// 閉じた便。点数が足りなければ noise
	for _, v := range st.sortedFlights() {
		if !last && st.watermark-v.last <= cfg.FlightMaxGapNs {
			continue
		}
		if !v.passed {
			stats.FlightsNoise++
			out, st.held = releaseFlight(out, st.held, v.id, true, stats)
		}
		st.forget(v)
	}
	stats.FlightsOpenMax = max(stats.FlightsOpenMax, len(st.flights))
	if last {
		st.flights, st.bySquawk, st.byChain, st.held = map[int64]*voyage{}, map[uint16][]int64{}, map[int64]int64{}, nil
	}
	return out
}

// assign は点の便を決める。連鎖が既に便に入っていればそれ、そうでなければ
// 個別スコークなら開いている同じスコークの便（複数あれば確定した点のある
// ものを優先し、次に時間の近いもの）、無ければ新しい便。
func (st *FlightState) assign(stats *Stats, cfg Config, f Fix) *voyage {
	if id, ok := st.byChain[f.Chain]; ok {
		return st.flights[id]
	}
	var best *voyage
	if !st.nonUnique[f.Squawk] {
		bestGap := int64(math.MaxInt64)
		for _, id := range st.bySquawk[f.Squawk] {
			v := st.flights[id]
			gap := absInt64(f.Timestamp - v.last)
			if gap > cfg.FlightMaxGapNs {
				continue
			}
			if best == nil || (v.points > 0 && best.points == 0) || (gap < bestGap && (v.points > 0) == (best.points > 0)) {
				best, bestGap = v, gap
			}
		}
	}
	if best == nil {
		st.nextID++
		best = &voyage{id: st.nextID, squawk: f.Squawk, first: f.Timestamp, last: f.Timestamp}
		st.flights[best.id] = best
		if !st.nonUnique[f.Squawk] {
			st.bySquawk[f.Squawk] = append(st.bySquawk[f.Squawk], best.id)
		}
		stats.Flights++
	}
	st.byChain[f.Chain] = best.id
	return best
}

// releaseFlight は保留から便 id の点を取り出して out に足す。noise なら
// ok / echo / ambiguous の点を noise にする。
func releaseFlight(out, held []Fix, id int64, noise bool, stats *Stats) ([]Fix, []Fix) {
	rest := held[:0]
	for _, h := range held {
		if h.Flight != id {
			rest = append(rest, h)
			continue
		}
		if noise && h.Status != FixUnconfirmed {
			h.Status = FixNoise
			stats.FixesNoise++
		}
		out = append(out, h)
	}
	return out, rest
}

func (st *FlightState) forget(v *voyage) {
	delete(st.flights, v.id)
	if ids := st.bySquawk[v.squawk]; ids != nil {
		ids = slices.DeleteFunc(ids, func(id int64) bool { return id == v.id })
		if len(ids) == 0 {
			delete(st.bySquawk, v.squawk)
		} else {
			st.bySquawk[v.squawk] = ids
		}
	}
	for chain, id := range st.byChain {
		if id == v.id {
			delete(st.byChain, chain)
		}
	}
}

func (st *FlightState) sortedFlights() []*voyage {
	out := make([]*voyage, 0, len(st.flights))
	for _, v := range st.flights {
		out = append(out, v)
	}
	slices.SortFunc(out, func(a, b *voyage) int { return compareInt64(a.id, b.id) })
	return out
}

// parseSquawks は 8 進 4 桁の文字列の一覧を集合にする。
func parseSquawks(codes []string) (map[uint16]bool, error) {
	set := map[uint16]bool{}
	for _, c := range codes {
		v, err := strconv.ParseUint(c, 8, 16)
		if err != nil || len(c) != 4 {
			return nil, fmt.Errorf("スコーク %q が 8 進 4 桁でない", c)
		}
		set[uint16(v)] = true
	}
	return set, nil
}
