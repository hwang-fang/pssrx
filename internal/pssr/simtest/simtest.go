// Package simtest は PSSR の段を検証するための合成データを作る。
//
// 既知の質問予定と既知の機体の振る舞いから応答を組み立てる。テストは
// 生成に使った値を正解として突き合わせる。乱数は使わず、生成は
// 入力から一意に決まる。
package simtest

import (
	"math"
	"slices"

	"pssrx/internal/config"
	"pssrx/internal/geodesy"
	"pssrx/internal/record"
)

// Schedule は SSR の質問予定の作り方。
type Schedule struct {
	Start        int64 // 最初の質問の時刻 [ns]
	Count        int
	Pattern      *config.Pattern
	AroundTimeNs int64   // 走査周期。方位はこれで 1 回転する
	Azimuth0     float64 // 最初の質問の方位 [rad]
	Clockwise    bool
}

// Interrogations は質問予定を合成する。方位は時刻に比例して回る。
func (s Schedule) Interrogations() []record.Interrogation {
	out := make([]record.Interrogation, s.Count)
	sign := 1.0
	if !s.Clockwise {
		sign = -1
	}
	for i := range out {
		t := s.Start + s.Pattern.Cumulative(int64(i))
		az := s.Azimuth0 + sign*2*math.Pi*float64(t-s.Start)/float64(s.AroundTimeNs)
		out[i] = record.Interrogation{
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
func Replies(intg []record.Interrogation, aircraft ...Aircraft) []record.Reply {
	var out []record.Reply
	for _, a := range aircraft {
		nc := 0
		for i := a.First; i <= a.Last && i < len(intg); i++ {
			if slices.Contains(a.Skip, i) {
				continue
			}
			q := intg[i]
			r := record.Reply{Timestamp: q.Timestamp + a.TauNs, WH: a.WH}
			switch q.Mode {
			case config.ModeCode['A']:
				r.Code = a.ModeA
			case config.ModeCode['C']:
				r.Code = a.ModeC[min(nc, len(a.ModeC)-1)]
				nc++
			}
			out = append(out, r)
		}
	}
	slices.SortStableFunc(out, func(a, b record.Reply) int {
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
func Fruit(timestamps []int64, code uint16) []record.Reply {
	out := make([]record.Reply, len(timestamps))
	for i, t := range timestamps {
		out[i] = record.Reply{Timestamp: t, Code: code, WH: 40000}
	}
	return out
}

// GillhamCode は気圧高度 [ft] を Mode C 応答符号（apkx のビット配置）にする。
// 100 ft 刻みでない高度は切り捨てる。
func GillhamCode(ft int) uint16 {
	n := (ft + 1200) / 100 // -1200 ft を 0 とする 100 ft 単位の段
	n500, n100 := n/5, n%5+1
	if n500%2 == 1 {
		n100 = 6 - n100
	}
	if n100 == 5 {
		n100 = 7
	}
	g500 := uint16(n500 ^ (n500 >> 1))
	g100 := uint16(n100 ^ (n100 >> 1))
	// MSB から D1 D2 D4 A1 A2 A4 B1 B2 B4 C1 C2 C4。g500 は D2 D4 A1 A2 A4 B1 B2 B4
	return (g500>>7&1)<<10 | (g500>>6&1)<<9 |
		(g500>>5&1)<<8 | (g500>>4&1)<<7 | (g500>>3&1)<<6 |
		(g500>>2&1)<<5 | (g500>>1&1)<<4 | (g500&1)<<3 |
		(g100>>2&1)<<2 | (g100>>1&1)<<1 | (g100 & 1)
}

func wrap(x float64) float64 {
	const twoPi = 2 * math.Pi
	r := math.Mod(x, twoPi)
	if r < 0 {
		r += twoPi
	}
	return r
}

// Observation は既知の位置の機体を SSR と局が観測したときの値。
type Observation struct {
	TauNs   int64   // 応答遅延を含む受信遅延
	Azimuth float64 // SSR から見た機体の方位 [rad], [0, 2pi)
}

// Observe は機体の位置から τ と方位を作る（位置推定の順方向モデル）。
//
// 位置推定と同じ ENU（SSR 原点）で、双基地和 |P−SSR| + |P−局| を光速で
// 割って応答遅延を足す。τ は ns に偶数丸めする。
func Observe(ssr, station, aircraft geodesy.OrthometricLLA, geoid geodesy.GeoidHeightProvider) (Observation, error) {
	conv, err := geodesy.NewENUConverter(ssr, geoid)
	if err != nil {
		return Observation{}, err
	}
	st, err := conv.LLAToENU(station)
	if err != nil {
		return Observation{}, err
	}
	p, err := conv.LLAToENU(aircraft)
	if err != nil {
		return Observation{}, err
	}
	rs := math.Sqrt(p.E*p.E + p.N*p.N + p.U*p.U)
	rt := math.Sqrt((p.E-st.E)*(p.E-st.E) + (p.N-st.N)*(p.N-st.N) + (p.U-st.U)*(p.U-st.U))
	return Observation{
		TauNs:   config.TransponderDelayNs + int64(math.RoundToEven((rs+rt)/config.SpeedOfLightMPerNs)),
		Azimuth: wrap(math.Atan2(p.E, p.N)),
	}, nil
}
