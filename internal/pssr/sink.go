package pssr

import (
	"encoding/csv"
	"fmt"
	"io"
	"strconv"

	"pssrx/internal/store"
)

// Sink は位置の出力先。CSV のほか、時系列 DB などを後から足す。
type Sink interface {
	Write(fixes []Fix) error
	Close() error
}

// CSVSink は位置を CSV で書く。
//
// 列: time_jst, ssr, station, squawk, pressure_alt_ft, lat, lon, alt_m,
// azimuth_rad, tau_ns, replies。緯度経度は小数 7 桁（約 1 cm）、
// 標高は mm。時刻は JST の ns まで。
type CSVSink struct {
	w      *csv.Writer
	closer io.Closer
}

// NewCSVSink は w に CSV を書く Sink を作る。closer が nil でなければ
// Close で閉じる。
func NewCSVSink(w io.Writer, closer io.Closer) (*CSVSink, error) {
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{
		"time_jst", "ssr", "station", "squawk", "pressure_alt_ft",
		"lat", "lon", "alt_m", "azimuth_rad", "tau_ns", "replies",
	}); err != nil {
		return nil, err
	}
	return &CSVSink{w: cw, closer: closer}, nil
}

// Write は位置を 1 行ずつ書く。
func (s *CSVSink) Write(fixes []Fix) error {
	for _, f := range fixes {
		squawk := ""
		if f.HasModeA {
			squawk = fmt.Sprintf("%04o", f.Squawk)
		}
		if err := s.w.Write([]string{
			store.ToTime(f.Timestamp).Format("2006-01-02T15:04:05.000000000"),
			f.SSRID, f.StationID, squawk,
			strconv.Itoa(f.AltitudeFt),
			strconv.FormatFloat(f.Position.Lat, 'f', 7, 64),
			strconv.FormatFloat(f.Position.Lon, 'f', 7, 64),
			strconv.FormatFloat(f.Position.Alt, 'f', 3, 64),
			strconv.FormatFloat(f.Azimuth, 'f', 9, 64),
			strconv.FormatInt(f.TauNs, 10),
			strconv.Itoa(len(f.Replies)),
		}); err != nil {
			return err
		}
	}
	s.w.Flush()
	return s.w.Error()
}

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
