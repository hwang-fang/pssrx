package pssr

import (
	"math"
	"slices"
)

// LinkState は便の断片をフライトに連結する段が持ち越す記録。ゼロ値から使える。
//
// Track の便は 1 機体の飛行の断片で、ガーブルで割れた列、欠測が
// MaxMissedScans を超えた区間、上昇中の高度の門などで切れる。真値との
// 比較では、相手のいない確定便の多くと、3 点に届かず unconfirmed になった
// 実機の点のほとんどが、こうした断片だった。
//
// 断片の末尾から外挿した位置に次の断片の先頭が乗れば同じフライトとみなし、
// フライト ID を付ける。unconfirmed の断片も、既存のフライトに連結できれば
// ok にする（前後が確定した飛行の一部であることが分かるため）。
//
// 連結は保守的にする。スコーク一致、切れ目が Track の打ち切り幅より長く
// LinkMaxGapNs 以内、外挿位置からの距離が速度の不確かさ × 切れ目 + 3σ
// 以内、高度が変化率の傾向に沿う。切れ目が Track の打ち切り幅より短い
// 断片は Track が同じ便に入れなかったもの（位置が合わない）なので繋がない。
// 外挿には末尾の複数点から出した速度を使うので、点が 1 つしか無い断片
// からは連結しない。旋回中は外挿が外れて新しいフライトになるが、誤って
// 別の機体を繋ぐよりよい。
//
// 判定に未来の点は要らない（外挿の元は過去の点）ので保留せず、点はそのまま
// 通す。同じフライトに同時に 2 本の便が属することは無い（それは像で、
// Resolve が扱う）。
type LinkState struct {
	flights   map[int64]*flight   // 開いているフライト。ID → 記録
	byTrack   map[int64]trackLink // 便 ID → 所属
	nextID    int64
	watermark int64
}

type trackLink struct {
	flight  int64
	rescued bool // unconfirmed の便を既存のフライトに連結した
}

// flight は開いているフライト。外挿に使う末尾の数点と、属する便を持つ。
type flight struct {
	id     int64
	squawk uint16
	tail   []Fix   // 時刻順。末尾 linkTail 点まで
	tracks []int64 // 属する便
}

// linkTail は外挿に使う末尾の点数。速度は最初と最後の差で出す。
const linkTail = 3

// Link は Track の出力（便 ID と判定の付いた点）を受け取り、フライト ID を
// 付けて返す。順序は変えない。
func Link(st *LinkState, stats *Stats, params Params, cfg Config, fixes []Fix, last bool) []Fix {
	if st.flights == nil {
		st.flights, st.byTrack = map[int64]*flight{}, map[int64]trackLink{}
	}
	out := make([]Fix, 0, len(fixes))
	for _, f := range fixes {
		tl, seen := st.byTrack[f.Track]
		if !seen {
			// 便の最初の点。既存のフライトの続きか
			if fl := st.match(params, cfg, f); fl != nil {
				tl = trackLink{flight: fl.id, rescued: f.Status == FixUnconfirmed}
				fl.tracks = append(fl.tracks, f.Track)
				stats.Links++
			} else {
				st.nextID++
				st.flights[st.nextID] = &flight{id: st.nextID, squawk: f.Squawk, tracks: []int64{f.Track}}
				tl = trackLink{flight: st.nextID}
				stats.Flights++
			}
			st.byTrack[f.Track] = tl
		}
		f.Flight = tl.flight
		if tl.rescued && f.Status == FixUnconfirmed {
			f.Status = FixOK
			stats.LinkedRescued++
		}
		st.flights[tl.flight].push(f)
		st.watermark = max(st.watermark, f.Timestamp)
		out = append(out, f)
	}
	st.forget(cfg)
	stats.FlightsOpenMax = max(stats.FlightsOpenMax, len(st.flights))
	if last {
		st.flights, st.byTrack = map[int64]*flight{}, map[int64]trackLink{}
	}
	return out
}

// match は点 f（便の最初の点）が続きになりうるフライトを返す。
// 候補が複数なら正規化距離が最小のもの。
func (st *LinkState) match(params Params, cfg Config, f Fix) *flight {
	var best *flight
	bestDist := math.Inf(1)
	for _, fl := range st.flights {
		if d, ok := linkGate(params, cfg, fl, f); ok && (d < bestDist || (d == bestDist && fl.id < best.id)) {
			best, bestDist = fl, d
		}
	}
	return best
}

// linkGate は点 f がフライト fl の続きになる条件を確かめ、正規化距離を返す。
func linkGate(params Params, cfg Config, fl *flight, f Fix) (float64, bool) {
	n := len(fl.tail)
	if n < 2 || f.Squawk != fl.squawk {
		return 0, false
	}
	a, b := fl.tail[0], fl.tail[n-1]
	dt := f.Timestamp - b.Timestamp
	dropAfter := int64(cfg.TrackMaxMissedScans+1)*params.AroundTimeNs + params.AroundTimeNs/4
	if dt <= dropAfter || dt > cfg.LinkMaxGapNs {
		return 0, false
	}
	span := float64(b.Timestamp-a.Timestamp) / 1e9
	if span <= 0 {
		return 0, false
	}
	sec := float64(dt) / 1e9
	// 末尾の速度で外挿する
	ve := (b.Position.ENU.E - a.Position.ENU.E) / span
	vn := (b.Position.ENU.N - a.Position.ENU.N) / span
	vz := float64(b.AltitudeFt-a.AltitudeFt) / span
	if math.Abs(float64(f.AltitudeFt)-(float64(b.AltitudeFt)+vz*sec)) > cfg.LinkClimbToleranceFtps*sec+100 {
		return 0, false
	}
	pe, pn := b.Position.ENU.E+ve*sec, b.Position.ENU.N+vn*sec
	width := cfg.LinkVelocityToleranceMps*sec + cfg.TrackGateSigmas*(horizontalSigma(f)+horizontalSigma(b))
	dist := math.Hypot(f.Position.ENU.E-pe, f.Position.ENU.N-pn)
	if dist > width {
		return 0, false
	}
	return dist / width, true
}

// push は点をフライトの末尾に足す。
func (fl *flight) push(f Fix) {
	fl.tail = append(fl.tail, f)
	if len(fl.tail) > linkTail {
		fl.tail = slices.Delete(fl.tail, 0, len(fl.tail)-linkTail)
	}
}

// forget は続きが来なくなったフライトと、その便の所属を忘れる。
func (st *LinkState) forget(cfg Config) {
	for id, fl := range st.flights {
		if n := len(fl.tail); n > 0 && st.watermark-fl.tail[n-1].Timestamp > cfg.LinkMaxGapNs {
			for _, t := range fl.tracks {
				delete(st.byTrack, t)
			}
			delete(st.flights, id)
		}
	}
}
