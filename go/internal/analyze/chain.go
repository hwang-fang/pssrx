package analyze

import (
	"fmt"
	"math"

	"pssrx/internal/npcompat"
	"pssrx/internal/pattern"
)

// Chain は質問間隔と質問種別のパターンに合致した一連のデータセット。
type Chain struct {
	Times     []int64   // 送信時刻 [ns]
	Modes     []uint8   // 質問種別
	PowersDbm []float64 // 振幅
	NLocal    []int64   // 局所質問番号（先頭が Phase0 に一致）
	Score     float64
}

// Length は連鎖に含まれる観測数。
func (c *Chain) Length() int { return len(c.Times) }

// Phase0 は連鎖の先頭の位相。
func (c *Chain) Phase0() int64 { return c.NLocal[0] }

// BestChain は最良連鎖を 1 本返す。合致する連鎖が無ければ nil。
//
//	状態    : (観測 i, 位相 p)  p in [0, L)
//	遷移    : i -> j, p -> p' = (p + d) % L, d = 1..D
//	          時刻条件 |t_j - t_i - delta(p, d)| < gate
//	          種別条件 mode(o_j) == pattern.mode(p')
//	評価    : 観測 1 個につき +1、飛ばし 1 段につき -lambda、正規化残差^2 に -mu
//	最適性  : delta(p, d) > 0 なので遷移は必ず時刻を進める。観測を時刻昇順に
//	          走査する 1 パスが位相順序（DAG）になり、前向き緩和のみで
//	          厳密最適解が得られる。
//
// 許容差内に候補が複数ある場合、異なる i から同じ (j, p') へ入るときは
// 親を上書きする。(j, p') に至る最適経路は 1 本だけ保持すれば十分で、
// 後続の判断は p' にしか依存しないため情報は失われない。一方、同じ
// (i, p) から許容差内の 2 発が候補になったときは別の状態になるので
// 両方が生き残り、どちらを使うかは終端の argmax が決める。
// 同点は先に評価した方を残す。走査順は i, p, d, j すべて昇順に固定して
// あるので出力は決定的。
//
// tf は t を float64 に変換した配列で、探索窓の二分探索に使う。
// numpy の searchsorted が int64 配列と float64 スカラを比較するとき
// 共通型 float64 に昇格させるため、Unix ナノ秒（約 1.78e18）では
// タイムスタンプが 256 ns に量子化される。この精度落ちも含めて再現する。
func BestChain(t []int64, tf []float64, m []uint8, pw []float64, pat *pattern.Pattern, cfg *Config) (*Chain, error) {
	n := len(t)
	if n == 0 {
		return nil, nil
	}
	l := int(pat.Length())
	d0 := int(cfg.MaxSkip)
	gate := float64(cfg.GateNs)
	phaseMode := pat.Modes()

	dtTab := make([]float64, l*d0)
	minDt := math.Inf(1)
	for p := 0; p < l; p++ {
		for d := 1; d <= d0; d++ {
			v := float64(pat.Delta(int64(p), int64(d)))
			dtTab[p*d0+d-1] = v
			minDt = math.Min(minDt, v)
		}
	}
	// 前向き 1 パスで厳密最適になる前提: 全ての遷移が必ず時刻を進めること。
	// min(delta) <= gate だと j <= i への遷移が生じ、位相順序が壊れる。
	if minDt <= gate {
		return nil, fmt.Errorf("GateTooWide: gate=%.0f ns >= 最小遷移 %.0f ns。"+
			"この条件では前向き 1 パスの最適性が保証されない", gate, minDt)
	}

	neg := math.Inf(-1)
	score := make([]float64, n*l)
	parI := make([]int32, n*l)
	parP := make([]int32, n*l)
	parD := make([]int8, n*l)
	for k := range score {
		score[k] = neg
		parI[k] = -1
		parP[k] = -1
	}

	for i := 0; i < n; i++ {
		mi := m[i]
		for p := 0; p < l; p++ { // 連鎖の開始
			if phaseMode[p] == mi && score[i*l+p] < 1.0 {
				score[i*l+p] = 1.0
				parI[i*l+p] = -1
			}
		}
		ti := t[i]
		tif := tf[i]
		for p := 0; p < l; p++ { // 前向き緩和
			s := score[i*l+p]
			if s == neg {
				continue
			}
			for d := 1; d <= d0; d++ {
				exp := dtTab[p*d0+d-1]
				j0 := npcompat.SearchSortedLeft(tf, tif+exp-gate)
				j1 := npcompat.SearchSortedRight(tf, tif+exp+gate)
				if j0 >= j1 {
					continue
				}
				pp := (p + d) % l
				mpp := phaseMode[pp]
				base := s + 1.0 - cfg.SkipPenalty*float64(d-1)
				for j := j0; j < j1; j++ {
					if m[j] != mpp {
						continue
					}
					r := (float64(t[j]-ti) - exp) / gate
					cand := base - cfg.ResidualWeight*r*r
					if cand > score[j*l+pp] {
						score[j*l+pp] = cand
						parI[j*l+pp] = int32(i)
						parP[j*l+pp] = int32(p)
						parD[j*l+pp] = int8(d)
					}
				}
			}
		}
	}

	// 同点は早い観測・小さい位相を採る（numpy の argmax と同じ）
	flat := 0
	for k := 1; k < len(score); k++ {
		if score[k] > score[flat] {
			flat = k
		}
	}
	bi, bp := flat/l, flat%l
	if math.IsInf(score[flat], 0) || math.IsNaN(score[flat]) {
		return nil, nil
	}

	pathI := []int{bi}
	pathP := []int{bp}
	var pathD []int
	ci, cp := bi, bp
	for parI[ci*l+cp] >= 0 {
		d := int(parD[ci*l+cp])
		ni, np := int(parI[ci*l+cp]), int(parP[ci*l+cp])
		pathI = append(pathI, ni)
		pathP = append(pathP, np)
		pathD = append(pathD, d)
		ci, cp = ni, np
	}
	reverse(pathI)
	reverse(pathP)
	reverse(pathD)

	nLocal := make([]int64, 0, len(pathI))
	nLocal = append(nLocal, int64(pathP[0]))
	for _, d := range pathD {
		nLocal = append(nLocal, nLocal[len(nLocal)-1]+int64(d))
	}

	ch := &Chain{
		Times:     make([]int64, len(pathI)),
		Modes:     make([]uint8, len(pathI)),
		PowersDbm: make([]float64, len(pathI)),
		NLocal:    nLocal,
		Score:     score[flat],
	}
	for k, idx := range pathI {
		ch.Times[k] = t[idx]
		ch.Modes[k] = m[idx]
		ch.PowersDbm[k] = pw[idx]
	}
	return ch, nil
}

func reverse[T any](s []T) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}
