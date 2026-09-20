package pssr

import (
	"math"
	"slices"
)

// TrackState は連続性の判定がブロックをまたいで持ち越す記録。ゼロ値から使える。
//
// 位置を時間・空間の連続性で便（track）にまとめ、便として確定しなかった
// 点を棄却する。前段までで残る偽の位置は、FRUIT の偶然の一致で組まれた
// 3 応答の孤立した点が大半で、これは次の走査に相手が無い。実機は走査ごとに
// 同じスコークで位置と高度が連続する。
//
// この段はエコー（反射で生成される位置）を判定しない。エコーは実機と同じ
// スコーク・同じ高度で走査ごとに連続するので、実機と同じように便になる。
// ただし門は位置で切るので、反射体の方向にずれたエコーは実機の便には
// 入らず、同じスコークの別の便として確定する。後続のエコー判定は、時間の
// 重なる同じスコークの便の組を τ と方位で見分けられる（Fix に残してある）。
//
// 判定には未来の点が要る（確定は 3 点、打ち切りは数走査の欠測）ので、
// 点を保留して時刻順に出す。
type TrackState struct {
	tracks    []track   // 開いている便（確定・仮の両方）
	held      []heldFix // 時刻順。判定待ち、または時刻順の出力待ち
	nextID    int64     // 次に開く便の ID
	watermark int64     // 受け取った点の最新の時刻
}

type track struct {
	id        int64
	squawk    uint16
	last      Fix // 最後に繋いだ点
	hits      int
	confirmed bool
}

type heldFix struct {
	fix      Fix
	assigned bool // 便に繋いだ（Track が付いた）
	decided  bool // Status が決まった
}

// Track は位置を受け取り、便に繋いで判定し、判定の決まった点を時刻順に返す。
// 棄却した点も返す（Status で区別）。last が真なら保留を全部判定して返す。
//
// 点 f を既存の便 T に繋ぐ条件（すべて満たす）:
//
//	スコークが一致
//	走査周期/2 < Δt ≤ (MaxMissedScans + 1) × 走査周期 + 走査周期/4
//	  （同じ走査に 2 点は入れない。欠測は MaxMissedScans まで許す）
//	水平距離 ≤ MaxSpeedMps × Δt + GateSigmas × (σ_f + σ_T)
//	高度差 ≤ MaxClimbFtps × Δt + 100 ft
//
// 候補が複数なら正規化距離（水平距離 / 門の幅）が最小の便。同じ走査の
// 別の点が同じ便をより近くで求めていれば f は譲り、新しい便を開く。
// 候補が無ければ新しい便を開く。
//
// 便は点が ConfirmHits に達したら確定し、点はすべて ok になる。最後の点から
// (MaxMissedScans + 1) 走査 + 余裕の間に点が来なければ打ち切り、確定して
// いなければ点はすべて unconfirmed になる。
//
// 便 ID は処理の開始からの連番で、打ち切った便の ID は再利用しない。
func Track(st *TrackState, stats *Stats, params Params, cfg Config, fixes []Fix, last bool) []Fix {
	halfScan := params.AroundTimeNs / 2
	dropAfter := int64(cfg.TrackMaxMissedScans+1)*params.AroundTimeNs + params.AroundTimeNs/4

	for _, f := range fixes {
		i, _ := slices.BinarySearchFunc(st.held, f.Timestamp, func(h heldFix, t int64) int {
			return compareInt64(h.fix.Timestamp, t)
		})
		for i < len(st.held) && st.held[i].fix.Timestamp == f.Timestamp {
			i++ // 同時刻は後ろに入れて到着順を保つ
		}
		st.held = slices.Insert(st.held, i, heldFix{fix: f})
		st.watermark = max(st.watermark, f.Timestamp)
	}
	stats.TrackHeldMax = max(stats.TrackHeldMax, len(st.held))

	// 繋ぐ。同じ走査の競合を見るため、点の走査周期/2 先まで届いてから
	for i := range st.held {
		h := &st.held[i]
		if h.assigned {
			continue
		}
		if !last && h.fix.Timestamp+halfScan > st.watermark {
			break
		}
		st.assign(stats, params, cfg, i, halfScan, dropAfter)
	}

	// 打ち切る。最後の点から dropAfter 以上点が来ない便。点は走査周期/2
	// 先まで届いてから繋ぐので、繋がりうる点が全部繋がれてから打ち切る
	// （そうでないと投入の刻みで結果が変わる）
	for k := 0; k < len(st.tracks); {
		t := &st.tracks[k]
		if !last && t.last.Timestamp+dropAfter+halfScan > st.watermark {
			k++
			continue
		}
		if !t.confirmed {
			st.decide(t.id, FixUnconfirmed, stats)
		}
		st.tracks = slices.Delete(st.tracks, k, k+1)
	}

	// 出す。時刻順を保つため、先頭から判定済みの分だけ
	n := 0
	for n < len(st.held) && st.held[n].decided {
		n++
	}
	out := make([]Fix, n)
	for k := range n {
		out[k] = st.held[k].fix
	}
	st.held = slices.Delete(st.held, 0, n)
	return out
}

// assign は held[i] を便に繋ぐ。
func (st *TrackState) assign(stats *Stats, params Params, cfg Config, i int, halfScan, dropAfter int64) {
	h := &st.held[i]
	f := h.fix
	best, bestDist := -1, 0.0
	for k := range st.tracks {
		if d, ok := gate(params, cfg, &st.tracks[k], f, halfScan, dropAfter); ok && (best < 0 || d < bestDist) {
			best, bestDist = k, d
		}
	}
	if best >= 0 {
		t := &st.tracks[best]
		// 同じ走査の別の点が、この便をより近くで求めていれば譲る。前の点は
		// 既に繋いだ後なので、見るのは後ろだけ（前の点が勝っていれば Δt の
		// 条件で既に候補から外れている）
		for k := i + 1; k < len(st.held) && st.held[k].fix.Timestamp-f.Timestamp <= halfScan; k++ {
			g := st.held[k].fix
			if d, ok := gate(params, cfg, t, g, halfScan, dropAfter); ok && d < bestDist {
				best = -1
				break
			}
		}
	}

	h.assigned = true
	if best < 0 {
		st.nextID++
		st.tracks = append(st.tracks, track{id: st.nextID, squawk: f.Squawk, last: f, hits: 1})
		stats.Tracks++
		h.fix.Track = st.nextID
		if cfg.TrackConfirmHits <= 1 {
			st.tracks[len(st.tracks)-1].confirmed = true
			stats.TracksConfirmed++
			st.decide(st.nextID, FixOK, stats)
		}
		return
	}
	t := &st.tracks[best]
	t.last = f
	t.hits++
	h.fix.Track = t.id
	if t.confirmed {
		h.decided = true
		h.fix.Status = FixOK
		stats.FixesOK++
	} else if t.hits >= cfg.TrackConfirmHits {
		t.confirmed = true
		stats.TracksConfirmed++
		st.decide(t.id, FixOK, stats)
	}
}

// gate は点 f が便 t に繋がる条件を確かめ、正規化距離を返す。
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
	width := cfg.TrackMaxSpeedMps*sec + cfg.TrackGateSigmas*(horizontalSigma(f)+horizontalSigma(t.last))
	de, dn := f.Position.ENU.E-t.last.Position.ENU.E, f.Position.ENU.N-t.last.Position.ENU.N
	dist := math.Hypot(de, dn)
	if dist > width {
		return 0, false
	}
	return dist / width, true
}

// horizontalSigma は位置の水平方向の標準偏差 [m]。
func horizontalSigma(f Fix) float64 {
	return math.Sqrt(f.Position.Cov[0][0] + f.Position.Cov[1][1])
}

// decide は便 id の保留中の点に判定を付ける。
func (st *TrackState) decide(id int64, status FixStatus, stats *Stats) {
	for i := range st.held {
		h := &st.held[i]
		if h.decided || h.fix.Track != id {
			continue
		}
		h.decided = true
		h.fix.Status = status
		if status == FixOK {
			stats.FixesOK++
		} else {
			stats.FixesUnconfirmed++
		}
	}
}
