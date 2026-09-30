package sink

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"pssrx/internal/pssr/tracking"
)

// SquawkGapNs は同じスコークの点を別のファイルに分ける切れ目 [ns]（600 s）。
const SquawkGapNs = 600_000_000_000

// SquawkSink はスコークごとに CSV を書く。同じスコークでも、前の点から
// SquawkGapNs 以上離れた点は別のファイルにする。
//
// ファイルは暫定の成果物で実時間性は要らないので、1 ファイル分の点を
// メモリに溜め、受け取った点の最新の時刻がそのファイルの最後の点から
// SquawkGapNs を過ぎたら（もう同じファイルに入る点は来ない）、時刻順に
// 並べて一度に書く。Close で残りを全部書く。
//
// ファイル名は {最初の点の時刻 UTC}_{スコーク}.csv（例
// 20260610T010001Z_2216.csv）で、ディレクトリの中で時刻順に並ぶ。列は
// CSVSink と同じ。
type SquawkSink struct {
	dir       string
	ssrID     string
	stationID string
	open      map[uint16][]tracking.Fix // スコーク → 書き出し待ちの点
	last      map[uint16]int64          // スコーク → 最後の点の時刻
	watermark int64
}

// NewSquawkSink は dir にスコークごとの CSV を書く Sink を作る。dir が
// 無ければ作る。
func NewSquawkSink(dir, ssrID, stationID string) (*SquawkSink, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &SquawkSink{
		dir: dir, ssrID: ssrID, stationID: stationID,
		open: map[uint16][]tracking.Fix{}, last: map[uint16]int64{},
	}, nil
}

// Write は点をスコークごとに溜め、切れ目を過ぎたファイルを書き出す。
func (s *SquawkSink) Write(fixes []tracking.Fix) error {
	for _, f := range fixes {
		if last, ok := s.last[f.Squawk]; ok && f.Timestamp-last >= SquawkGapNs {
			if err := s.flush(f.Squawk); err != nil {
				return err
			}
		}
		s.open[f.Squawk] = append(s.open[f.Squawk], f)
		s.last[f.Squawk] = max(s.last[f.Squawk], f.Timestamp)
		s.watermark = max(s.watermark, f.Timestamp)
	}
	for _, sq := range s.squawks() {
		if s.watermark-s.last[sq] >= SquawkGapNs {
			if err := s.flush(sq); err != nil {
				return err
			}
		}
	}
	return nil
}

// Close は溜めている点を全部書き出す。
func (s *SquawkSink) Close() error {
	for _, sq := range s.squawks() {
		if err := s.flush(sq); err != nil {
			return err
		}
	}
	return nil
}

// squawks は溜めている点のあるスコークを昇順で返す（書き出しの順を決める）。
func (s *SquawkSink) squawks() []uint16 {
	out := make([]uint16, 0, len(s.open))
	for sq := range s.open {
		out = append(out, sq)
	}
	slices.Sort(out)
	return out
}

// flush はスコーク sq の溜めている点を時刻順に 1 つのファイルへ書き、忘れる。
func (s *SquawkSink) flush(sq uint16) error {
	fixes := s.open[sq]
	delete(s.open, sq)
	delete(s.last, sq)
	if len(fixes) == 0 {
		return nil
	}
	slices.SortStableFunc(fixes, func(a, b tracking.Fix) int {
		switch {
		case a.Timestamp < b.Timestamp:
			return -1
		case a.Timestamp > b.Timestamp:
			return 1
		}
		return 0
	})
	name := fmt.Sprintf("%s_%04o.csv", utc(fixes[0].Timestamp).Format("20060102T150405Z"), sq)
	f, err := os.OpenFile(filepath.Join(s.dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	w := csv.NewWriter(f)
	if err := w.Write(csvHeader); err != nil {
		f.Close()
		return err
	}
	for _, fx := range fixes {
		if err := w.Write(csvRow(s.ssrID, s.stationID, fx)); err != nil {
			f.Close()
			return err
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
