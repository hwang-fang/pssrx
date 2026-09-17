package archive

import (
	"cmp"
	"fmt"
	"iter"
	"slices"
	"time"

	"pssrx/internal/record"
)

// FileSource は 1 分 1 ファイルの qpkx / apkx / intg から、1 ファイルを
// 1 ブロックとして作る。種別ごとにルートと ID を持ち、ルートが空の種別は
// 読まない。ファイルが無い分は空のブロックになる。
type FileSource struct {
	QpkxRoot, QpkxStation string
	ApkxRoot, ApkxStation string
	IntgRoot, IntgSSR     string

	// IntgLeadNs は最初のブロックの Interrogations に、前の分のファイルから Start より
	// これだけ前までのレコードを足す幅。期間の先頭の応答は期間より前の
	// 質問に属しうるので、pssr 段を単独で走らせるときに遅延の上限（TauMax）
	// を渡す。1 分より短いこと。
	IntgLeadNs int64

	From, To time.Time // 分境界で指定する
}

// Blocks は From から To までの分を順に返す。
func (s FileSource) Blocks() iter.Seq2[record.Block, error] {
	return func(yield func(record.Block, error) bool) {
		if err := s.validate(); err != nil {
			yield(record.Block{}, err)
			return
		}
		qpkx := &QpkxDir{Root: s.QpkxRoot}
		apkx := &ApkxDir{Root: s.ApkxRoot}
		intg := &IntgDir{Root: s.IntgRoot}

		first := true
		for dt := s.From; dt.Before(s.To); dt = dt.Add(time.Minute) {
			next := dt.Add(time.Minute)
			blk := record.Block{Start: dt.UnixNano(), End: next.UnixNano(), Last: !next.Before(s.To)}
			var err error
			if s.QpkxRoot != "" {
				if blk.Received, err = qpkx.ReadMinute(s.QpkxStation, dt); err != nil {
					yield(record.Block{}, err)
					return
				}
			}
			if s.ApkxRoot != "" {
				if blk.Replies, err = apkx.ReadMinute(s.ApkxStation, dt); err != nil {
					yield(record.Block{}, err)
					return
				}
			}
			if s.IntgRoot != "" {
				if blk.Interrogations, err = intg.ReadMinute(s.IntgSSR, dt); err != nil {
					yield(record.Block{}, err)
					return
				}
				if first && s.IntgLeadNs > 0 {
					lead, err := s.intgLead(intg, dt)
					if err != nil {
						yield(record.Block{}, err)
						return
					}
					blk.Interrogations = append(lead, blk.Interrogations...)
				}
			}
			first = false
			if !yield(blk, nil) {
				return
			}
		}
	}
}

// intgLead は dt の前の分のファイルから、dt − IntgLeadNs 以降のレコードを返す。
func (s FileSource) intgLead(d *IntgDir, dt time.Time) ([]record.Interrogation, error) {
	prev, err := d.ReadMinute(s.IntgSSR, dt.Add(-time.Minute))
	if err != nil {
		return nil, err
	}
	cut := dt.UnixNano() - s.IntgLeadNs
	i, _ := slices.BinarySearchFunc(prev, cut, func(x record.Interrogation, t int64) int { return cmp.Compare(x.Timestamp, t) })
	return slices.Clone(prev[i:]), nil
}

func (s FileSource) validate() error {
	if !s.To.After(s.From) {
		return fmt.Errorf("to (%s) は from (%s) より後である必要があります", s.To, s.From)
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
	if s.IntgLeadNs < 0 || s.IntgLeadNs >= OneMinute {
		return fmt.Errorf("intg の先読み幅は 0 以上 1 分未満: %d ns", s.IntgLeadNs)
	}
	if s.QpkxRoot == "" && s.ApkxRoot == "" && s.IntgRoot == "" {
		return fmt.Errorf("読む種別が 1 つも無い")
	}
	return nil
}
