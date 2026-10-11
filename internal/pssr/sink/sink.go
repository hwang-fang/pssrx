// Package sink は位置（tracking.Fix）を書く出力先。実装は CSV で、時系列 DB
// などは別の実装として足す。
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

// Context は各行に書く処理の文脈。
type Context struct {
	SSR     string // 質問元の SSR の ID
	Station string // 応答を受信した局の ID
	// Source はソース構成の ID（方式:SSR:局。例 pssr1:RJBB1:KX00）。航跡片 ID の
	// 接頭辞になる。
	Source string
	// Run は処理の ID。行ごとにどの処理の点かを残す（1 つのファイルに複数回の
	// 処理の点が入りうる）。
	Run string
}

// SourceID は単局 PSSR のソース構成の ID。
func SourceID(ssrID, stationID string) string { return "pssr1:" + ssrID + ":" + stationID }

// TrackID は航跡片の識別子 {ソース構成}:{最初の点の時刻 UTC}:{最初の点の τ}。
// 処理の開始によらず、同じ航跡片なら同じ値になる。Track が 0 の点（航跡片に
// 入れない棄却した点）は空。
func TrackID(source string, f tracking.Fix) string {
	if f.Track == 0 {
		return ""
	}
	return source + ":" + utc(f.TrackStartNs).Format("20060102T150405.000000000Z") + ":" + strconv.FormatInt(f.TrackStartTauNs, 10)
}

// CSVSink は全点を 1 つの CSV に、受け取った順で書く。解析・デバッグ用。
// 列は csvHeader（CSVSink と SessionStore で共通）。
type CSVSink struct {
	w      *csv.Writer
	closer io.Closer
	ctx    Context
}

// NewCSVSink は w に CSV を書く Sink を作る。closer が nil でなければ Close で閉じる。
func NewCSVSink(w io.Writer, closer io.Closer, ctx Context) (*CSVSink, error) {
	cw := csv.NewWriter(w)
	if err := cw.Write(csvHeader); err != nil {
		return nil, err
	}
	return &CSVSink{w: cw, closer: closer, ctx: ctx}, nil
}

// Write は位置を 1 行ずつ書く。
func (s *CSVSink) Write(fixes []tracking.Fix) error {
	for _, f := range fixes {
		if err := s.w.Write(csvRow(s.ctx, f)); err != nil {
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
//	time_utc              座標点の代表時刻（単局 PSSR はドウェルの中心）。UTC、ns まで
//	ssr, station          質問元の SSR と、応答を受信した局
//	squawk                Mode A（8 進 4 桁）
//	pressure_alt_ft       Mode C の気圧高度 [ft]
//	track                 航跡片の ID（TrackID）。判定は付けない
//	track_seq             航跡片の中での順番（1 始まり）。処理の境目で航跡片は割れる
//	lat, lon, height_m    位置（WGS84、標高 [m]。気圧高度をそのまま換算）
//	e_m, n_m, u_m         SSR を原点にした ENU [m]（位置推定の座標）
//	cov_ee, cov_en, cov_nn  水平の位置の共分散 [m²]。機体の位置での東・北の軸
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
//	drop                  棄却の理由。通常は空。デバッグ出力（CSVSink）でだけ入る
//	run                   この点を出した処理の ID
//
// 共分散（機体の位置の軸）と e_m / n_m / u_m（SSR の軸）は軸が違う。SSR から
// 東西に離れるほど、子午線の収束のぶん東・北の向きがずれる。位置を解けなかった
// 行（drop のある行の一部）は位置・共分散・観測の標準偏差以降が空欄。
var csvHeader = []string{
	"time_utc", "ssr", "station", "squawk", "pressure_alt_ft", "track", "track_seq",
	"lat", "lon", "height_m", "e_m", "n_m", "u_m",
	"cov_ee", "cov_en", "cov_nn",
	"tau_ns", "azimuth_rad", "azimuth_first_rad", "azimuth_last_rad",
	"replies", "mode_a_replies", "mode_c_replies", "altitude_spread_ft", "siblings",
	"sigma_bistatic_m", "sigma_azimuth_rad", "sigma_altitude_m",
	"bistatic_range_m", "ground_range_m", "geometry_factor", "residual_m", "iterations",
	"drop", "run",
}

const timeLayout = "2006-01-02T15:04:05.000000000Z"

func utc(ns int64) time.Time { return time.Unix(0, ns).UTC() }

// csvRow は点 1 つの行。
func csvRow(ctx Context, f tracking.Fix) []string {
	fl := func(v float64, prec int) string { return strconv.FormatFloat(v, 'f', prec, 64) }
	row := []string{
		utc(f.Timestamp).Format(timeLayout),
		ctx.SSR, ctx.Station, fmt.Sprintf("%04o", f.Squawk),
		strconv.Itoa(f.AltitudeFt),
		TrackID(ctx.Source, f), strconv.Itoa(f.TrackSeq),
	}
	if f.Located {
		p, c := f.Position, f.Position.CovEN
		row = append(row,
			fl(p.Lat, 7), fl(p.Lon, 7), fl(p.Alt, 3),
			fl(p.ENU.E, 3), fl(p.ENU.N, 3), fl(p.ENU.U, 3),
			fl(c[0][0], 1), fl(c[0][1], 1), fl(c[1][1], 1),
		)
	} else {
		row = append(row, make([]string, 9)...)
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
	return append(row, f.Drop, ctx.Run)
}
