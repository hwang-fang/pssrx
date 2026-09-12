package pssr

import (
	"fmt"
	"slices"
)

// Suppressor は同じ機体の幽霊プロットを落とす。
//
// 実データには 1 機が同じ走査に複数の方位で現れる幽霊が、実プロットと
// 同じ程度の数ある。正体は 2 つで、どちらも SSR で知られた現象。
//
//	サイドローブ  近い機体が SSR のサイドローブ質問にも応答する。τ は
//	              主ビームと同じで、方位は無関係、列は短いことが多い
//	反射          SSR のビームが反射体に当たって遠くの機体を照射する。
//	              τ は主ビームより経路差ぶん大きく（6〜30 km ≈ 20〜100 µs）、
//	              方位は反射体の方向。列が主ビームより長いこともある
//
// 同じ走査・同じスコーク・同じ高度のプロットを 1 機体の群とみなし、
// τ が最小のものから DirectTauToleranceNs 以内を直接照射の候補として
// 応答数が最多の 1 件を残し、それより τ の大きいものを反射として落とす。
// 方位は使わない。反射体の方向は機体と無関係で、角度で絞ると反射が通る。
//
// 別の機体が同じスコーク・同じ高度・同じ走査にいれば、τ の大きい方が
// 落ちる。稀だが起こりうるので件数を数える。航跡処理が入れば救える。
type Suppressor struct {
	cfg    Config
	window int64 // 同じ走査とみなす時刻差
	slack  int64 // プロットの到着順の乱れを吸収する余裕

	// buf は時刻順のプロット。判定済みでも、後続の判定の相手として
	// 必要な間は残す。
	buf       []held
	watermark int64 // 受け取ったプロットの最新の時刻
	stats     SuppressStats
}

type held struct {
	plot     Plot
	decided  bool
	survived bool
}

// SuppressStats は抑圧の件数。
type SuppressStats struct {
	In        int
	Sidelobe  int // 直接照射の候補のうち応答数で負けた
	Multipath int // τ が直接照射より大きい
	Out       int
}

// NewSuppressor は Suppressor を作る。
func NewSuppressor(params Params, cfg Config) (*Suppressor, error) {
	if params.AroundTimeNs <= 0 {
		return nil, fmt.Errorf("走査周期が不正: %d", params.AroundTimeNs)
	}
	if !(cfg.SameScanFraction > 0 && cfg.SameScanFraction < 1) || cfg.AltitudeToleranceFt < 0 || cfg.DirectTauToleranceNs <= 0 {
		return nil, fmt.Errorf("抑圧の閾値が不正: %+v", cfg)
	}
	return &Suppressor{
		cfg:    cfg,
		window: int64(float64(params.AroundTimeNs) * cfg.SameScanFraction),
		// 列が閉じる順は時刻順と数質問ぶんずれうる。走査周期の 1/10 あれば十分
		slack: params.AroundTimeNs / 10,
	}, nil
}

// Stats は現時点の集計。
func (s *Suppressor) Stats() SuppressStats { return s.stats }

// Push はプロットを受け取り、同じ走査の相手が出揃ったものから判定して
// 生き残りを返す。last が真なら全部判定する。
func (s *Suppressor) Push(plots []Plot, last bool) []Plot {
	s.stats.In += len(plots)
	for _, p := range plots {
		i, _ := slices.BinarySearchFunc(s.buf, p.Timestamp, func(h held, t int64) int {
			return compareInt64(h.plot.Timestamp, t)
		})
		// 同時刻は後ろに入れて到着順を保つ
		for i < len(s.buf) && s.buf[i].plot.Timestamp == p.Timestamp {
			i++
		}
		s.buf = slices.Insert(s.buf, i, held{plot: p})
		s.watermark = max(s.watermark, p.Timestamp)
	}

	var out []Plot
	for i := range s.buf {
		h := &s.buf[i]
		if h.decided {
			continue
		}
		if !last && h.plot.Timestamp+s.window+s.slack > s.watermark {
			break
		}
		h.decided = true
		h.survived = s.decide(i)
		if h.survived {
			s.stats.Out++
			out = append(out, h.plot)
		}
	}
	// 判定済みで、もう誰の相手にもならないものを落とす
	cut := s.watermark - 2*s.window - s.slack
	if last {
		cut = s.watermark + 1
	}
	s.buf = slices.DeleteFunc(s.buf, func(h held) bool {
		return h.decided && h.plot.Timestamp < cut
	})
	return out
}

// decide は buf[i] を残すかを決める。
func (s *Suppressor) decide(i int) bool {
	p := s.buf[i].plot
	if !p.HasModeA {
		return true
	}
	// 同じ機体とみなす群。時刻順なので窓の両側を走査する
	minTau := p.TauNs
	var group []Plot
	for k := i; k >= 0 && p.Timestamp-s.buf[k].plot.Timestamp <= s.window; k-- {
		if q := s.buf[k].plot; s.sameAircraft(p, q) {
			group = append(group, q)
			minTau = min(minTau, q.TauNs)
		}
	}
	for k := i + 1; k < len(s.buf) && s.buf[k].plot.Timestamp-p.Timestamp <= s.window; k++ {
		if q := s.buf[k].plot; s.sameAircraft(p, q) {
			group = append(group, q)
			minTau = min(minTau, q.TauNs)
		}
	}
	if p.TauNs > minTau+s.cfg.DirectTauToleranceNs {
		s.stats.Multipath++
		return false
	}
	// 直接照射の候補の中で応答数最多か。同数なら早い方
	for _, q := range group {
		if q.TauNs > minTau+s.cfg.DirectTauToleranceNs {
			continue
		}
		if len(q.Replies) > len(p.Replies) ||
			(len(q.Replies) == len(p.Replies) && q.Timestamp < p.Timestamp) {
			s.stats.Sidelobe++
			return false
		}
	}
	return true
}

func (s *Suppressor) sameAircraft(p, q Plot) bool {
	if !q.HasModeA || q.Squawk != p.Squawk {
		return false
	}
	d := p.AltitudeFt - q.AltitudeFt
	if d < 0 {
		d = -d
	}
	return d <= s.cfg.AltitudeToleranceFt
}
