package tracking

import (
	"math"
	"slices"
)

// TrackState は連続性の判定がブロックをまたいで持ち越す記録。ゼロ値から使える。
//
// 位置を時間・空間の連続性で航跡片（track）にまとめ、航跡片 ID と航跡片の
// 中での順番を付ける。判定（確定・棄却）は付けない。前段までで残る偽の位置は
// FRUIT の偶然の一致で組まれた孤立した点が大半で、これは次の走査に相手が
// 無く 1〜2 点の航跡片になる。どこで切るかは後続が決める。
//
// 繋ぐかは過去の点だけで決まる。ただし同じ走査のうち後から来る点が同じ
// 航跡片をより近くで求めるかもしれないので、点は走査周期の半分だけ保留して
// から繋ぎ、繋いだら出す。これが出力の遅れのすべて。
//
// この段はエコー（反射で生成される位置）を判定しない。エコーは実機と同じ
// スコーク・同じ高度で走査ごとに連続するので、実機と同じように航跡片になる。
// 門は位置で切るので、反射体の方向にずれたエコーは実機の航跡片には入らず、
// 同じスコークの別の航跡片になる。見分けるのは後続で、そのために Fix は
// τ・方位・応答数・共分散・Siblings を残し、航跡片 ID は再利用しない。
type TrackState struct {
	tracks    []track // 開いている航跡片
	held      []Fix   // 時刻順。繋ぐ前の点
	nextID    int64   // 次に開く航跡片の ID
	watermark int64   // 受け取った点の最新の時刻
}

type track struct {
	id     int64
	squawk uint16
	last   Fix // 最後に繋いだ点
	hits   int
	kf     trackFilter
}

// Track は位置を受け取り、航跡片に繋いで ID と順番を付け、時刻順に返す。
// last が真なら保留を全部繋いで返す。
//
// 点 f を既存の航跡片 T に繋ぐ条件（すべて満たす）:
//
//	スコークが一致
//	走査周期/2 < Δt ≤ (MaxMissedScans + 1) × 走査周期 + 走査周期/4
//	  （同じ走査に 2 点は入れない。欠測は MaxMissedScans まで許す）
//	水平位置が T のフィルタの予測からマハラノビス距離 GateSigmas 以内
//	  （予測の共分散 + f の共分散で測る。trackFilter）
//	高度差 ≤ MaxClimbFtps × Δt + 100 ft
//
// 候補が複数ならマハラノビス距離が最小の航跡片。同じ走査の
// 別の点が同じ航跡片をより近くで求めていれば f は譲り、新しい航跡片を開く。
// 候補が無ければ新しい航跡片を開く。最後の点から (MaxMissedScans + 1) 走査
// + 余裕の間に点が来なければ打ち切る。
func Track(st *TrackState, stats *Stats, params Params, cfg Config, fixes []Fix, last bool) []Fix {
	halfScan := params.AroundTimeNs / 2
	dropAfter := int64(cfg.TrackMaxMissedScans+1)*params.AroundTimeNs + params.AroundTimeNs/4

	for _, f := range fixes {
		i, _ := slices.BinarySearchFunc(st.held, f.Timestamp, func(h Fix, t int64) int {
			return compareInt64(h.Timestamp, t)
		})
		for i < len(st.held) && st.held[i].Timestamp == f.Timestamp {
			i++ // 同時刻は後ろに入れて到着順を保つ
		}
		st.held = slices.Insert(st.held, i, f)
		st.watermark = max(st.watermark, f.Timestamp)
	}
	stats.TrackHeldMax = max(stats.TrackHeldMax, len(st.held))

	// 繋ぐ。同じ走査の競合を見るため、点の走査周期/2 先まで届いてから。
	// 打ち切りは繋ぐ前に行う（繋がりうる点が全部繋がれてから打ち切るので、
	// 投入の刻みで結果が変わらない）
	var out []Fix
	n := 0
	for n < len(st.held) && (last || st.held[n].Timestamp+halfScan <= st.watermark) {
		st.drop(stats, st.held[n].Timestamp-dropAfter-halfScan)
		out = append(out, st.assign(stats, params, cfg, n, halfScan, dropAfter))
		n++
	}
	st.held = slices.Delete(st.held, 0, n)
	if last {
		st.drop(stats, math.MaxInt64)
	} else {
		st.drop(stats, st.watermark-dropAfter-halfScan)
	}
	return out
}

// drop は最後の点が before より前の航跡片を打ち切る。
func (st *TrackState) drop(stats *Stats, before int64) {
	st.tracks = slices.DeleteFunc(st.tracks, func(t track) bool {
		if t.last.Timestamp >= before {
			return false
		}
		if t.hits >= 3 {
			stats.Tracks3++
		}
		return true
	})
}

// assign は held[i] を航跡片に繋ぎ、ID と順番を付けた点を返す。
func (st *TrackState) assign(stats *Stats, params Params, cfg Config, i int, halfScan, dropAfter int64) Fix {
	f := st.held[i]
	best, bestDist := -1, 0.0
	for k := range st.tracks {
		if d, ok := gate(params, cfg, &st.tracks[k], f, halfScan, dropAfter); ok && (best < 0 || d < bestDist) {
			best, bestDist = k, d
		}
	}
	if best >= 0 {
		t := &st.tracks[best]
		// 同じ走査の別の点が、この航跡片をより近くで求めていれば譲る。前の点は
		// 既に繋いだ後なので、見るのは後ろだけ（前の点が勝っていれば Δt の
		// 条件で既に候補から外れている）
		for k := i + 1; k < len(st.held) && st.held[k].Timestamp-f.Timestamp <= halfScan; k++ {
			if d, ok := gate(params, cfg, t, st.held[k], halfScan, dropAfter); ok && d < bestDist {
				best = -1
				break
			}
		}
	}
	if best < 0 {
		st.nextID++
		stats.Tracks++
		f.Track, f.TrackSeq = st.nextID, 1
		st.tracks = append(st.tracks, track{id: st.nextID, squawk: f.Squawk, last: f, hits: 1, kf: newTrackFilter(cfg, f)})
		return f
	}
	t := &st.tracks[best]
	t.hits++
	f.Track, f.TrackSeq = t.id, t.hits
	t.kf.update(cfg, f)
	t.last = f
	return f
}

// gate は点 f が航跡片 t に繋がる条件を確かめ、マハラノビス距離を返す。
func gate(params Params, cfg Config, t *track, f Fix, halfScan, dropAfter int64) (float64, bool) {
	if f.Squawk != t.squawk {
		return 0, false
	}
	dt := f.Timestamp - t.last.Timestamp
	if dt <= halfScan || dt > dropAfter {
		return 0, false
	}
	sec := float64(dt) / 1e9
	if d := f.AltitudeFt - t.last.AltitudeFt; math.Abs(float64(d)) > cfg.TrackMaxClimbFtps*sec+100 {
		return 0, false
	}
	d, ok := t.kf.distance(cfg, f)
	if !ok || d > cfg.TrackGateSigmas {
		return 0, false
	}
	return d, true
}
