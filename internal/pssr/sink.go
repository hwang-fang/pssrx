package pssr

import (
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"strconv"

	"pssrx/internal/record"
)

// Sink は位置の出力先。CSV のほか、時系列 DB などを後から足す。
type Sink interface {
	Write(fixes []Fix) error
	Close() error
}

// CSVSink は位置を CSV で書く。
//
// 列: time_jst, ssr, station, squawk, pressure_alt_ft, lat, lon, alt_m,
// azimuth_rad, tau_ns, replies, sigma_e_m, sigma_n_m, sigma_u_m, track, flight,
// status, sm_lat, sm_lon, sm_alt_m, sm_sigma_e_m, sm_sigma_n_m, sm_sigma_u_m,
// vel_e_mps, vel_n_mps, vel_u_mps, turn_rate_dps。
// 緯度経度は小数 7 桁（約 1 cm）、標高は mm、時刻は JST の ns まで。
// sigma_* は SSR の ENU 系での位置の標準偏差 [m]。ssr / station は
// 処理の文脈で、全行に同じ値が入る。track は便 ID、status は判定
// （ok / unconfirmed / echo / ambiguous）で、棄却した点も書く。flight は便を
// 連結したフライトの ID。lat / lon / alt_m は観測値（Locate）のまま、sm_* と
// vel_* は平滑化した位置・その標準偏差・ENU の速度 [m/s] で、平滑化して
// いない点は空欄。turn_rate_dps は協調旋回モデルの旋回率 [deg/s]（右旋回が
// 正）で、等速モデルでは空欄。
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
	if err := cw.Write([]string{
		"time_jst", "ssr", "station", "squawk", "pressure_alt_ft",
		"lat", "lon", "alt_m", "azimuth_rad", "tau_ns", "replies",
		"sigma_e_m", "sigma_n_m", "sigma_u_m", "track", "flight", "status",
		"sm_lat", "sm_lon", "sm_alt_m", "sm_sigma_e_m", "sm_sigma_n_m", "sm_sigma_u_m",
		"vel_e_mps", "vel_n_mps", "vel_u_mps", "turn_rate_dps",
	}); err != nil {
		return nil, err
	}
	return &CSVSink{w: cw, closer: closer, ssrID: ssrID, stationID: stationID}, nil
}

// Write は位置を 1 行ずつ書く。
func (s *CSVSink) Write(fixes []Fix) error {
	for _, f := range fixes {
		sm := make([]string, 10)
		if k := f.Smoothed; k != nil {
			sm = []string{
				strconv.FormatFloat(k.Lat, 'f', 7, 64),
				strconv.FormatFloat(k.Lon, 'f', 7, 64),
				strconv.FormatFloat(k.Alt, 'f', 3, 64),
				strconv.FormatFloat(math.Sqrt(k.Cov[0][0]), 'f', 1, 64),
				strconv.FormatFloat(math.Sqrt(k.Cov[1][1]), 'f', 1, 64),
				strconv.FormatFloat(math.Sqrt(k.Cov[2][2]), 'f', 1, 64),
				strconv.FormatFloat(k.Velocity.E, 'f', 1, 64),
				strconv.FormatFloat(k.Velocity.N, 'f', 1, 64),
				strconv.FormatFloat(k.Velocity.U, 'f', 2, 64),
				"",
			}
			if k.HasTurnRate {
				// ENU の反時計回り正を、航空の慣例の右旋回正にする
				sm[9] = strconv.FormatFloat(-k.TurnRate*180/math.Pi, 'f', 2, 64)
			}
		}
		if err := s.w.Write(append([]string{
			record.ToTime(f.Timestamp).Format("2006-01-02T15:04:05.000000000"),
			s.ssrID, s.stationID, fmt.Sprintf("%04o", f.Squawk),
			strconv.Itoa(f.AltitudeFt),
			strconv.FormatFloat(f.Position.Lat, 'f', 7, 64),
			strconv.FormatFloat(f.Position.Lon, 'f', 7, 64),
			strconv.FormatFloat(f.Position.Alt, 'f', 3, 64),
			strconv.FormatFloat(f.Azimuth, 'f', 9, 64),
			strconv.FormatInt(f.TauNs, 10),
			strconv.Itoa(len(f.Replies)),
			strconv.FormatFloat(math.Sqrt(f.Position.Cov[0][0]), 'f', 1, 64),
			strconv.FormatFloat(math.Sqrt(f.Position.Cov[1][1]), 'f', 1, 64),
			strconv.FormatFloat(math.Sqrt(f.Position.Cov[2][2]), 'f', 1, 64),
			strconv.FormatInt(f.Track, 10),
			strconv.FormatInt(f.Flight, 10),
			f.Status.String(),
		}, sm...)); err != nil {
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
