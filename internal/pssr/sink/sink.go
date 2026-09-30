// Package sink は位置（tracking.Fix）を書く出力先。いまは CSV だけで、
// 時系列 DB などを後から足す。
package sink

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"

	"pssrx/internal/pssr/tracking"
	"pssrx/internal/record"
)

// Sink は位置の出力先。CSV のほか、時系列 DB などを後から足す。
type Sink interface {
	Write(fixes []tracking.Fix) error
	Close() error
}

// Multi は複数の Sink に同じ位置を書く。
type Multi []Sink

func (m Multi) Write(fixes []tracking.Fix) error {
	for _, s := range m {
		if err := s.Write(fixes); err != nil {
			return err
		}
	}
	return nil
}

func (m Multi) Close() error {
	var errs []error
	for _, s := range m {
		errs = append(errs, s.Close())
	}
	return errors.Join(errs...)
}

// CSVSink は位置を CSV で書く。
//
// 列: time_jst, ssr, station, squawk, pressure_alt_ft, lat, lon, alt_m,
// azimuth_rad, tau_ns, replies, sigma_e_m, sigma_n_m, sigma_u_m, track, track_seq。
// 緯度経度は小数 7 桁（約 1 cm）、標高は mm、時刻は JST の ns まで。
// sigma_* は SSR の ENU 系での位置の標準偏差 [m]。ssr / station は
// 処理の文脈で、全行に同じ値が入る。track は航跡片の ID、track_seq は
// 航跡片の中での順番（1 始まり）。判定は付けない。
type CSVSink struct {
	w         *csv.Writer
	closer    io.Closer
	ssrID     string
	stationID string
}

// NewCSVSink は w に CSV を書く Sink を作る。ssrID と stationID は各行の
// 文脈として書く。closer が nil でなければ Close で閉じる。
func NewCSVSink(w io.Writer, closer io.Closer, ssrID, stationID string) (*CSVSink, error) {
	cw := csv.NewWriter(w)
	if err := cw.Write(csvHeader); err != nil {
		return nil, err
	}
	return &CSVSink{w: cw, closer: closer, ssrID: ssrID, stationID: stationID}, nil
}

// Write は位置を 1 行ずつ書く。
func (s *CSVSink) Write(fixes []tracking.Fix) error {
	for _, f := range fixes {
		if err := s.w.Write(csvRow(s.ssrID, s.stationID, f)); err != nil {
			return err
		}
	}
	s.w.Flush()
	return s.w.Error()
}

// csvHeader は CSV の列。
var csvHeader = []string{
	"time_jst", "ssr", "station", "squawk", "pressure_alt_ft",
	"lat", "lon", "alt_m", "azimuth_rad", "tau_ns", "replies",
	"sigma_e_m", "sigma_n_m", "sigma_u_m", "track", "track_seq",
}

// csvRow は点 1 つの行。
func csvRow(ssrID, stationID string, f tracking.Fix) []string {
	return []string{
		record.ToTime(f.Timestamp).Format(timeLayout),
		ssrID, stationID, fmt.Sprintf("%04o", f.Squawk),
		strconv.Itoa(f.AltitudeFt),
		strconv.FormatFloat(f.Position.Lat, 'f', 7, 64),
		strconv.FormatFloat(f.Position.Lon, 'f', 7, 64),
		strconv.FormatFloat(f.Position.Alt, 'f', 3, 64),
		strconv.FormatFloat(f.Azimuth, 'f', 9, 64),
		strconv.FormatInt(f.TauNs, 10),
		strconv.Itoa(f.Replies),
		strconv.FormatFloat(math.Sqrt(f.Position.Cov[0][0]), 'f', 1, 64),
		strconv.FormatFloat(math.Sqrt(f.Position.Cov[1][1]), 'f', 1, 64),
		strconv.FormatFloat(math.Sqrt(f.Position.Cov[2][2]), 'f', 1, 64),
		strconv.FormatInt(f.Track, 10),
		strconv.Itoa(f.TrackSeq),
	}
}

const timeLayout = "2006-01-02T15:04:05.000000000"

// Close は書き残しを流し、closer があれば閉じる。
func (s *CSVSink) Close() error {
	s.w.Flush()
	if err := s.w.Error(); err != nil {
		return err
	}
	if s.closer != nil {
		return s.closer.Close()
	}
	return nil
}
