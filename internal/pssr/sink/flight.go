package sink

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"

	"pssrx/internal/pssr/tracking"
	"pssrx/internal/record"
)

// FlightSink はフライト（Link で連結した便）ごとに 1 つの CSV を書く。
// 列は CSVSink と同じ。
//
// ファイル名は {最初の点の時刻 JST}_{スコーク}_{フライト ID}.csv で、
// 時刻順に並ぶ。フライトの点はブロックをまたいで届くので、書くたびに
// 追記で開いて閉じる（開いたまま持たない。2 時間で 1,600 本になる）。
// 判定は区別せず、echo / ambiguous の点もそのフライトのファイルに入る
// （status 列で分かる）。unconfirmed の点は便が 1〜2 点で、フライトとして
// 意味が無いので、まとめて unconfirmed.csv に書く。
type FlightSink struct {
	dir       string
	ssrID     string
	stationID string
	names     map[int64]string // フライト ID → ファイル名
}

// NewFlightSink は dir（無ければ作る）にフライトごとの CSV を書く Sink を作る。
func NewFlightSink(dir, ssrID, stationID string) (*FlightSink, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &FlightSink{dir: dir, ssrID: ssrID, stationID: stationID, names: map[int64]string{}}, nil
}

// Write は点をフライトごとのファイルに追記する。同じ呼び出しの中の順序は
// 保つ。
func (s *FlightSink) Write(fixes []tracking.Fix) error {
	// ファイルごとにまとめて、開く回数を減らす
	groups := map[string][]tracking.Fix{}
	var order []string
	for _, f := range fixes {
		name := s.fileFor(f)
		if _, ok := groups[name]; !ok {
			order = append(order, name)
		}
		groups[name] = append(groups[name], f)
	}
	for _, name := range order {
		if err := s.appendRows(name, groups[name]); err != nil {
			return err
		}
	}
	return nil
}

// fileFor は点を書くファイル名。フライトの最初の点で決める。
func (s *FlightSink) fileFor(f tracking.Fix) string {
	if f.Status == tracking.FixUnconfirmed {
		return "unconfirmed.csv"
	}
	name, ok := s.names[f.Flight]
	if !ok {
		name = fmt.Sprintf("%s_%04o_%06d.csv", record.ToTime(f.Timestamp).Format("20060102T150405"), f.Squawk, f.Flight)
		s.names[f.Flight] = name
	}
	return name
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

// Close は何もしない（ファイルは書くたびに閉じている）。
func (s *FlightSink) Close() error { return nil }

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
