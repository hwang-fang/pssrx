// Package simtest は PSSR の段を検証するための合成データを作る。
//
// 既知の質問予定と既知の機体の振る舞いから応答を組み立てる。テストは
// 生成に使った値を正解として突き合わせる。乱数は使わず、生成は
// 入力から一意に決まる。
package simtest

import (
	"math"
	"slices"

	"pssrx/internal/pattern"
	"pssrx/internal/store"
)

// Schedule は SSR の質問予定の作り方。
type Schedule struct {
	Start        int64 // 最初の質問の時刻 [ns]
	Count        int
	Pattern      *pattern.Pattern
	AroundTimeNs int64   // 走査周期。方位はこれで 1 回転する
	Azimuth0     float64 // 最初の質問の方位 [rad]
	Clockwise    bool
}

// Intg は質問予定を合成する。方位は時刻に比例して回る。
func (s Schedule) Intg() []store.Intg {
	out := make([]store.Intg, s.Count)
	sign := 1.0
	if !s.Clockwise {
		sign = -1
	}
	for i := range out {
		t := s.Start + s.Pattern.Cumulative(int64(i))
		az := s.Azimuth0 + sign*2*math.Pi*float64(t-s.Start)/float64(s.AroundTimeNs)
		out[i] = store.Intg{
			Timestamp: t,
			Azimuth:   wrap(az),
			Mode:      s.Pattern.ModeAt(int64(i)),
		}
	}
	return out
}

// Aircraft は 1 機体が 1 ドウェルでどう応答するか。
type Aircraft struct {
	TauNs int64  // 質問から受信までの遅延 [ns]。ドウェル中は一定
	ModeA uint16 // Mode A 質問への応答符号
	// ModeC は Mode C 質問への応答符号。応答するたびに順に使い、尽きたら
	// 最後を繰り返す。上昇中の境界またぎは 2 要素で表す。
	ModeC []uint16
	// First, Last は応答する質問の番号（質問予定の添字）の範囲。両端を含む。
	First, Last int
	// Skip は範囲内で応答しない質問の番号。途切れを表す。
	Skip []int
	WH   uint16
}

// Replies は質問予定と機体から応答を合成する。受信時刻順。
func Replies(intg []store.Intg, aircraft ...Aircraft) []store.AData {
	var out []store.AData
	for _, a := range aircraft {
		nc := 0
		for i := a.First; i <= a.Last && i < len(intg); i++ {
			if slices.Contains(a.Skip, i) {
				continue
			}
			q := intg[i]
			r := store.AData{Timestamp: q.Timestamp + a.TauNs, WH: a.WH}
			switch q.Mode {
			case pattern.ModeCode['A']:
				r.Code = a.ModeA
			case pattern.ModeCode['C']:
				r.Code = a.ModeC[min(nc, len(a.ModeC)-1)]
				nc++
			}
			out = append(out, r)
		}
	}
	slices.SortStableFunc(out, func(a, b store.AData) int {
		switch {
		case a.Timestamp < b.Timestamp:
			return -1
		case a.Timestamp > b.Timestamp:
			return 1
		}
		return 0
	})
	return out
}

// Fruit は質問予定と無関係な時刻の応答（他の SSR への応答）を作る。
func Fruit(timestamps []int64, code uint16) []store.AData {
	out := make([]store.AData, len(timestamps))
	for i, t := range timestamps {
		out[i] = store.AData{Timestamp: t, Code: code, WH: 40000}
	}
	return out
}

func wrap(x float64) float64 {
	const twoPi = 2 * math.Pi
	r := math.Mod(x, twoPi)
	if r < 0 {
		r += twoPi
	}
	return r
}
