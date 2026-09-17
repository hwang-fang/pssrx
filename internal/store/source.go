package store

import (
	"fmt"
	"iter"
	"time"
)

// Block は解析へ 1 回に投入するデータ。[Start, End) のデータは揃っている。
//
// 各列は時刻昇順。投入の刻み（1 分でも 1 秒でも）を決めるのはブロックを
// 作る側で、解析はブロックの幅に依存しない。Last は最後のブロックで、
// 解析は持ち越しているものをすべて処理してよい。
//
// Intg は pssr 段を単独で走らせるときの入力で、interrogator 段と直列に
// 流すときは段の出力が使われる。最初のブロックの Intg だけは Start より
// 前のレコードを含みうる（FileSource.IntgLeadNs を参照）。
type Block struct {
	Start, End int64 // Unix ナノ秒
	Last       bool
	QData      []QData // 質問受信（interrogator 段の入力）
	Replies    []AData // 応答（pssr 段の入力）
	Intg       []Intg  // 質問予定（pssr 段を単独で走らせるときの入力）
}

// FileSource は 1 分 1 ファイルの qpkx / apkx / intg からブロックを作る。
//
// 種別ごとにルートと ID を持ち、ルートが空の種別は読まない。ブロックは
// From から To まで Block 刻みで、End は To を超えても切り詰めない
// （From と To は分境界で指定される前提）。
//
// ブロックが 1 分より短くても分ファイルを読み直さないよう、種別ごとに
// 読み込み済みの分を覚えておき、そこから切り出す。
type FileSource struct {
	QpkxRoot, QpkxStation string
	ApkxRoot, ApkxStation string
	IntgRoot, IntgSSR     string

	// IntgLeadNs は最初のブロックの Intg を Start よりこれだけ前から読む幅。
	// 期間の先頭の応答は期間より前の質問に属しうるので、pssr 段を単独で
	// 走らせるときに遅延の上限（TauMax）を渡す。
	IntgLeadNs int64

	From, To time.Time
	// Block はブロックの幅。0 なら 1 分。
	Block time.Duration
}

// Blocks は From から To までのブロックを時刻順に返す。
func (s FileSource) Blocks() iter.Seq2[Block, error] {
	return func(yield func(Block, error) bool) {
		block := s.Block
		if block == 0 {
			block = time.Minute
		}
		if err := s.validate(block); err != nil {
			yield(Block{}, err)
			return
		}
		var (
			qpkx *minuteCache[QData]
			apkx *minuteCache[AData]
			intg *minuteCache[Intg]
		)
		if s.QpkxRoot != "" {
			r := &QdataRepository{Root: s.QpkxRoot}
			qpkx = newMinuteCache(
				func(a, b int64) ([]QData, error) { return r.Fetch(s.QpkxStation, a, b) },
				func(q QData) int64 { return q.Timestamp })
		}
		if s.ApkxRoot != "" {
			r := &AdataRepository{Root: s.ApkxRoot}
			apkx = newMinuteCache(
				func(a, b int64) ([]AData, error) { return r.Fetch(s.ApkxStation, a, b) },
				func(a AData) int64 { return a.Timestamp })
		}
		if s.IntgRoot != "" {
			r := &IntgRepository{Root: s.IntgRoot}
			intg = newMinuteCache(
				func(a, b int64) ([]Intg, error) { return r.Fetch(s.IntgSSR, a, b) },
				func(d Intg) int64 { return d.Timestamp })
		}

		lead := s.IntgLeadNs
		for cur := s.From; cur.Before(s.To); cur = cur.Add(block) {
			next := cur.Add(block)
			blk := Block{Start: cur.UnixNano(), End: next.UnixNano(), Last: !next.Before(s.To)}
			var err error
			if qpkx != nil {
				if blk.QData, err = qpkx.get(blk.Start, blk.End); err != nil {
					yield(Block{}, err)
					return
				}
			}
			if apkx != nil {
				if blk.Replies, err = apkx.get(blk.Start, blk.End); err != nil {
					yield(Block{}, err)
					return
				}
			}
			if intg != nil {
				if blk.Intg, err = intg.get(blk.Start-lead, blk.End); err != nil {
					yield(Block{}, err)
					return
				}
				lead = 0
			}
			if !yield(blk, nil) {
				return
			}
		}
	}
}

func (s FileSource) validate(block time.Duration) error {
	if !s.To.After(s.From) {
		return fmt.Errorf("to (%s) は from (%s) より後である必要があります", s.To, s.From)
	}
	if block < 0 {
		return fmt.Errorf("ブロック幅が負: %s", block)
	}
	if s.QpkxRoot != "" && s.QpkxStation == "" {
		return fmt.Errorf("qpkx を読むには局 ID が必要です")
	}
	if s.ApkxRoot != "" && s.ApkxStation == "" {
		return fmt.Errorf("apkx を読むには局 ID が必要です")
	}
	if s.IntgRoot != "" && s.IntgSSR == "" {
		return fmt.Errorf("intg を読むには SSR ID が必要です")
	}
	if s.IntgLeadNs < 0 {
		return fmt.Errorf("intg の先読み幅が負: %d", s.IntgLeadNs)
	}
	if s.QpkxRoot == "" && s.ApkxRoot == "" && s.IntgRoot == "" {
		return fmt.Errorf("読む種別が 1 つも無い")
	}
	return nil
}

// minuteCache は分境界に広げた範囲を読み込み済みとして持ち、そこから
// [start, end) を切り出す。ブロックが分と揃っていれば毎回読み、分より
// 短ければ同じ分の間は読み直さない。
type minuteCache[T any] struct {
	fetch      func(start, end int64) ([]T, error)
	ts         func(T) int64
	start, end int64 // 読み込み済みの範囲（分境界）。end == 0 なら未読
	recs       []T
}

func newMinuteCache[T any](fetch func(start, end int64) ([]T, error), ts func(T) int64) *minuteCache[T] {
	return &minuteCache[T]{fetch: fetch, ts: ts}
}

func (c *minuteCache[T]) get(start, end int64) ([]T, error) {
	fs := start - start%OneMinute
	fe := end
	if r := end % OneMinute; r != 0 {
		fe += OneMinute - r
	}
	if c.end == 0 || fs != c.start || fe != c.end {
		recs, err := c.fetch(fs, fe)
		if err != nil {
			return nil, err
		}
		c.start, c.end, c.recs = fs, fe, recs
	}
	return clip(c.recs, start, end, c.ts), nil
}
