// Package sink は位置（tracking.Fix）を書く出力先。いまは CSV だけで、
// 時系列 DB などを後から足す。
package sink

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"pssrx/internal/pssr/tracking"
)

// Sink は位置の出力先。
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

// CSVSink は全点を 1 つの CSV に、受け取った順で書く。解析用。
// 列は csvHeader（CSVSink と SquawkSink で共通）。
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

// csvHeader は CSV の列。
//
//	time_utc              観測時刻（ドウェルの中心）。UTC、ns まで
//	ssr, station          処理の文脈（SSR と応答局）。全行同じ
//	squawk                Mode A（8 進 4 桁）
//	pressure_alt_ft       Mode C の気圧高度 [ft]
//	track, track_seq      航跡片の ID と、航跡片の中での順番（1 始まり）。判定は付けない
//	lat, lon, height_m    位置（WGS84、標高 [m]。気圧高度をそのまま換算）
//	e_m, n_m, u_m         SSR を原点にした ENU [m]
//	cov_ee … cov_uu       ENU の位置の共分散 [m²] の上三角（ee, en, eu, nn, nu, uu）
//	tau_ns                τ の平均（双基地距離 + 応答遅延）[ns]
//	azimuth_rad           ビーム方位（列の最初と最後の中点）[rad]
//	azimuth_first_rad, azimuth_last_rad  列の最初と最後の質問の方位 [rad]
//	replies               列の応答数
//	mode_a_replies, mode_c_replies       Mode A 応答、復号できた Mode C 応答の数
//	altitude_spread_ft    復号できた高度の最大と最小の差 [ft]
//	siblings              同じ走査で τ・高度が一致し方位の離れた候補の数（像の手がかり）
//	sigma_bistatic_m, sigma_azimuth_rad, sigma_altitude_m  観測量の標準偏差
//	bistatic_range_m, ground_range_m     双基地距離 L、SSR からの地上距離 ρ [m]
//	geometry_factor       ∂L/∂ρ（遠方で 2、基線に近づくと 0）
//	residual_m, iterations               検算値と曲率の反復回数
//	drop                  棄却の理由。通常は空。デバッグ出力でだけ入る
//
// 位置を解けなかった行（drop のある行の一部）は位置・共分散・観測の標準偏差
// 以降が空欄。
var csvHeader = []string{
	"time_utc", "ssr", "station", "squawk", "pressure_alt_ft", "track", "track_seq",
	"lat", "lon", "height_m", "e_m", "n_m", "u_m",
	"cov_ee", "cov_en", "cov_eu", "cov_nn", "cov_nu", "cov_uu",
	"tau_ns", "azimuth_rad", "azimuth_first_rad", "azimuth_last_rad",
	"replies", "mode_a_replies", "mode_c_replies", "altitude_spread_ft", "siblings",
	"sigma_bistatic_m", "sigma_azimuth_rad", "sigma_altitude_m",
	"bistatic_range_m", "ground_range_m", "geometry_factor", "residual_m", "iterations",
	"drop",
}

const timeLayout = "2006-01-02T15:04:05.000000000Z"

func utc(ns int64) time.Time { return time.Unix(0, ns).UTC() }

// csvRow は点 1 つの行。
func csvRow(ssrID, stationID string, f tracking.Fix) []string {
	fl := func(v float64, prec int) string { return strconv.FormatFloat(v, 'f', prec, 64) }
	row := []string{
		utc(f.Timestamp).Format(timeLayout),
		ssrID, stationID, fmt.Sprintf("%04o", f.Squawk),
		strconv.Itoa(f.AltitudeFt),
		strconv.FormatInt(f.Track, 10), strconv.Itoa(f.TrackSeq),
	}
	if f.Located {
		p, c := f.Position, f.Position.Cov
		row = append(row,
			fl(p.Lat, 7), fl(p.Lon, 7), fl(p.Alt, 3),
			fl(p.ENU.E, 3), fl(p.ENU.N, 3), fl(p.ENU.U, 3),
			fl(c[0][0], 1), fl(c[0][1], 1), fl(c[0][2], 1), fl(c[1][1], 1), fl(c[1][2], 1), fl(c[2][2], 1),
		)
	} else {
		row = append(row, make([]string, 12)...)
	}
	row = append(row,
		strconv.FormatInt(f.TauNs, 10),
		fl(f.Azimuth, 9), fl(f.AzimuthFirst, 9), fl(f.AzimuthLast, 9),
		strconv.Itoa(f.Replies), strconv.Itoa(f.ModeAReplies), strconv.Itoa(f.ModeCReplies),
		strconv.Itoa(f.AltitudeSpreadFt), strconv.Itoa(f.Siblings),
	)
	if f.Located {
		m := f.Measure
		row = append(row,
			fl(m.SigmaBistaticM, 1), fl(m.SigmaAzimuthRad, 9), fl(m.SigmaAltitudeM, 2),
			fl(m.BistaticRangeM, 1), fl(m.GroundRangeM, 1), fl(m.GeometryFactor, 6),
			fl(m.ResidualM, 6), strconv.Itoa(m.Iterations),
		)
	} else {
		row = append(row, make([]string, 8)...)
	}
	return append(row, f.Drop)
}
