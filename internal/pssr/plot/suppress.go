package plot

import (
	"math"
	"slices"
)

// SuppressState は幽霊抑圧がブロックをまたいで持ち越す記録。ゼロ値から使える。
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
// 判定には同じ走査の相手が出揃っている必要があるので、プロットを窓の
// 幅だけ保留する。
type SuppressState struct {
	// held は時刻順のプロット。判定済みでも、後続の判定の相手として
	// 必要な間は残す。
	held      []heldPlot
	watermark int64 // 受け取ったプロットの最新の時刻
}

type heldPlot struct {
	plot    Plot
	decided bool
}

// Suppress はプロットを受け取り、同じ走査の相手が出揃ったものから判定して
// 生き残りを返す。last が真なら全部判定する。
//
// 同じ走査・同じスコーク・同じ高度のプロットを 1 機体の群とみなし、
// τ が最小のものから DirectTauToleranceNs 以内を直接照射の候補とし、
// それより τ の大きいものを経路の長い反射として落とす（物理だけで決まる）。
//
// 直接照射の候補が複数あるときは方位差で扱いを分ける。
// ResolveAzimuthSeparationRad 以内の候補どうしは同じドウェルの断片（主ビーム
// と第 1 サイドローブ、ガーブルで割れた列）なので応答数最多の 1 件を残す。
// それを超えて離れた候補は SSR 近傍の反射体経由の像で、τ が同じため
// ここでは決められない（応答数で選ぶと実データで 26% 誤る）。両方を通し、
// 位置が離れて別の便になったものを Resolve が便の文脈で決める。
//
// 別の機体が同じスコーク・同じ高度・同じ走査にいれば、τ の大きい方が
// 落ちる。稀だが起こりうるので件数に含まれる。
func Suppress(st *SuppressState, stats *Stats, params Params, cfg Config, plots []Plot, last bool) []Plot {
	window := int64(float64(params.AroundTimeNs) * cfg.SameScanFraction)
	// 列が閉じる順は時刻順と数質問ぶんずれうる。走査周期の 1/10 あれば十分
	slack := params.AroundTimeNs / 10

	for _, p := range plots {
		i, _ := slices.BinarySearchFunc(st.held, p.Timestamp, func(h heldPlot, t int64) int {
			return compareInt64(h.plot.Timestamp, t)
		})
		// 同時刻は後ろに入れて到着順を保つ
		for i < len(st.held) && st.held[i].plot.Timestamp == p.Timestamp {
			i++
		}
		st.held = slices.Insert(st.held, i, heldPlot{plot: p})
		st.watermark = max(st.watermark, p.Timestamp)
	}

	var out []Plot
	for i := range st.held {
		h := &st.held[i]
		if h.decided {
			continue
		}
		if !last && h.plot.Timestamp+window+slack > st.watermark {
			break
		}
		h.decided = true
		if survives(st.held, i, cfg, window, stats) {
			stats.Kept++
			out = append(out, h.plot)
		}
	}
	// 判定済みで、もう誰の相手にもならないものを落とす
	cut := st.watermark - 2*window - slack
	if last {
		cut = st.watermark + 1
	}
	st.held = slices.DeleteFunc(st.held, func(h heldPlot) bool {
		return h.decided && h.plot.Timestamp < cut
	})
	return out
}

// survives は held[i] を残すかを決める。
func survives(held []heldPlot, i int, cfg Config, window int64, stats *Stats) bool {
	p := held[i].plot
	// 同じ機体とみなす群。時刻順なので窓の両側を走査する
	minTau := p.TauNs
	var group []Plot
	for k := i; k >= 0 && p.Timestamp-held[k].plot.Timestamp <= window; k-- {
		if q := held[k].plot; sameAircraft(cfg, p, q) {
			group = append(group, q)
			minTau = min(minTau, q.TauNs)
		}
	}
	for k := i + 1; k < len(held) && held[k].plot.Timestamp-p.Timestamp <= window; k++ {
		if q := held[k].plot; sameAircraft(cfg, p, q) {
			group = append(group, q)
			minTau = min(minTau, q.TauNs)
		}
	}
	if p.TauNs > minTau+cfg.DirectTauToleranceNs {
		stats.Multipath++
		return false
	}
	// 方位の近い直接照射の候補の中で応答数最多か。同数なら早い方
	for _, q := range group {
		if q.TauNs > minTau+cfg.DirectTauToleranceNs {
			continue
		}
		if angleDiff(p.Azimuth, q.Azimuth) > cfg.ImageAzimuthSeparationRad {
			continue // 像の候補。便の文脈で決める
		}
		if len(q.Replies) > len(p.Replies) ||
			(len(q.Replies) == len(p.Replies) && q.Timestamp < p.Timestamp) {
			stats.Sidelobe++
			return false
		}
	}
	return true
}

// angleDiff は 2 つの方位の差の絶対値 [rad]（0〜π）。
func angleDiff(a, b float64) float64 {
	d := math.Mod(math.Abs(a-b), 2*math.Pi)
	if d > math.Pi {
		d = 2*math.Pi - d
	}
	return d
}

func sameAircraft(cfg Config, p, q Plot) bool {
	if q.Squawk != p.Squawk {
		return false
	}
	d := p.AltitudeFt - q.AltitudeFt
	if d < 0 {
		d = -d
	}
	return d <= cfg.AltitudeToleranceFt
}
