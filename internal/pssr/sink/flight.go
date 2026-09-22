package sink

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"pssrx/internal/pssr/tracking"
	"pssrx/internal/record"
)

// FlightSink は便（tracking.Flight が束ねた 1 機体の 1 回の飛行）ごとに
// 1 つの CSV を書く。列は CSVSink と同じ。
//
// ファイル名は {最初の点の時刻 JST}_{スコーク}_{便 ID}.csv で、時刻順に
// 並ぶ。便の点はブロックをまたいで届くので、書くたびに追記で開いて閉じる
// （開いたまま持たない。2 時間で 500 本になる）。echo / ambiguous の点も
// その便のファイルに入る（status 列で分かる）。
//
// unconfirmed と noise の点は Close まで溜め、便ごとに置き場を決める。
//
//   - 便のファイルがある（点数の足りた便の unconfirmed）: その便のファイル
//   - 個別スコークの noise の便: 同じスコークの便のファイルのうち時間の
//     最も近いものに status: noise のまま同梱する。同じスコークの便が
//     無ければ、IncludeNoise のときだけ {時刻}_{スコーク}_noise.csv
//   - 非個別スコークの noise の便: IncludeNoise のときだけ
//     {時刻}_{スコーク}_noise.csv。既定では書かない（デバッグ用）
//
// 遅れて足した行があるファイルは Close で時刻順に並べ直す。
type FlightSink struct {
	dir          string
	ssrID        string
	stationID    string
	includeNoise bool
	nonUnique    map[uint16]bool
	files        map[int64]*flightFile // 便 ID → ファイル
	deferred     map[int64][]tracking.Fix
	order        []int64 // deferred の便 ID を届いた順に
}

// flightFile は便のファイルの記録。
type flightFile struct {
	name        string
	squawk      uint16
	first, last int64
	resort      bool
}

// NewFlightSink は dir（無ければ作る）に便ごとの CSV を書く Sink を作る。
// nonUniqueSquawks は機体の識別子にならないスコーク（8 進 4 桁）。
// includeNoise が真なら、便のファイルに同梱できない noise / unconfirmed の
// 点も {時刻}_{スコーク}_noise.csv に書く。
func NewFlightSink(dir, ssrID, stationID string, nonUniqueSquawks []string, includeNoise bool) (*FlightSink, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	nonUnique := map[uint16]bool{}
	for _, c := range nonUniqueSquawks {
		v, err := strconv.ParseUint(c, 8, 16)
		if err != nil {
			return nil, fmt.Errorf("スコーク %q が 8 進でない", c)
		}
		nonUnique[uint16(v)] = true
	}
	return &FlightSink{
		dir: dir, ssrID: ssrID, stationID: stationID, includeNoise: includeNoise, nonUnique: nonUnique,
		files: map[int64]*flightFile{}, deferred: map[int64][]tracking.Fix{},
	}, nil
}

// Write は点を便ごとのファイルに追記する。同じ呼び出しの中の順序は保つ。
func (s *FlightSink) Write(fixes []tracking.Fix) error {
	// ファイルごとにまとめて、開く回数を減らす
	groups := map[int64][]tracking.Fix{}
	var order []int64
	for _, f := range fixes {
		if f.Status == tracking.FixUnconfirmed || f.Status == tracking.FixNoise {
			if _, ok := s.deferred[f.Flight]; !ok {
				s.order = append(s.order, f.Flight)
			}
			s.deferred[f.Flight] = append(s.deferred[f.Flight], f)
			continue
		}
		if _, ok := groups[f.Flight]; !ok {
			order = append(order, f.Flight)
		}
		groups[f.Flight] = append(groups[f.Flight], f)
	}
	for _, id := range order {
		ff := s.files[id]
		if ff == nil {
			f0 := groups[id][0]
			ff = &flightFile{name: fileName(f0, strconv.FormatInt(id, 10)), squawk: f0.Squawk, first: f0.Timestamp, last: f0.Timestamp}
			s.files[id] = ff
		}
		for _, f := range groups[id] {
			ff.first, ff.last = min(ff.first, f.Timestamp), max(ff.last, f.Timestamp)
		}
		if err := s.appendRows(ff.name, groups[id]); err != nil {
			return err
		}
	}
	return nil
}

// fileName は {時刻}_{スコーク}_{suffix}.csv。
func fileName(f tracking.Fix, suffix string) string {
	return fmt.Sprintf("%s_%04o_%s.csv", record.ToTime(f.Timestamp).Format("20060102T150405"), f.Squawk, suffix)
}

// Close は溜めていた unconfirmed / noise の点を置き、遅れて足したファイルを
// 時刻順に並べ直す。
func (s *FlightSink) Close() error {
	for _, id := range s.order {
		rows := s.deferred[id]
		if ff := s.files[id]; ff != nil {
			// 点数の足りた便の unconfirmed
			ff.resort = true
			if err := s.appendRows(ff.name, rows); err != nil {
				return err
			}
			continue
		}
		f0 := rows[0]
		if !s.nonUnique[f0.Squawk] {
			if ff := s.nearest(f0); ff != nil {
				ff.resort = true
				if err := s.appendRows(ff.name, rows); err != nil {
					return err
				}
				continue
			}
		}
		if s.includeNoise {
			if err := s.appendRows(fileName(f0, "noise"), rows); err != nil {
				return err
			}
		}
	}
	s.deferred, s.order = map[int64][]tracking.Fix{}, nil
	for _, ff := range s.files {
		if ff.resort {
			if err := sortFile(filepath.Join(s.dir, ff.name)); err != nil {
				return err
			}
			ff.resort = false
		}
	}
	return nil
}

// nearest は同じスコークの便のファイルのうち、時刻の最も近いもの。
func (s *FlightSink) nearest(f tracking.Fix) *flightFile {
	var best *flightFile
	var bestGap int64
	for _, ff := range s.files {
		if ff.squawk != f.Squawk {
			continue
		}
		gap := max(ff.first-f.Timestamp, f.Timestamp-ff.last, 0)
		if best == nil || gap < bestGap {
			best, bestGap = ff, gap
		}
	}
	return best
}

// appendRows は行を追記する。無ければヘッダから書く。
func (s *FlightSink) appendRows(name string, fixes []tracking.Fix) error {
	path := filepath.Join(s.dir, name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	w := csv.NewWriter(f)
	if st.Size() == 0 {
		if err := w.Write(csvHeader); err != nil {
			return err
		}
	}
	for _, x := range fixes {
		if err := w.Write(csvRow(s.ssrID, s.stationID, x)); err != nil {
			return err
		}
	}
	w.Flush()
	return w.Error()
}

// sortFile は CSV の行をヘッダを除いて時刻（先頭列）で並べ直す。
func sortFile(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	rows, err := csv.NewReader(bytes.NewReader(b)).ReadAll()
	if err != nil {
		return err
	}
	if len(rows) < 3 {
		return nil
	}
	body := rows[1:]
	slices.SortStableFunc(body, func(a, b []string) int { return strings.Compare(a[0], b[0]) })
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	if err := w.WriteAll(rows); err != nil {
		return err
	}
	return w.Error()
}

// Multi は複数の Sink に同じ点を書く。
type Multi []Sink

// Write は全部に書く。最初のエラーで止まる。
func (m Multi) Write(fixes []tracking.Fix) error {
	for _, s := range m {
		if err := s.Write(fixes); err != nil {
			return err
		}
	}
	return nil
}

// Close は全部を閉じ、最初のエラーを返す。
func (m Multi) Close() error {
	var first error
	for _, s := range m {
		if err := s.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}
