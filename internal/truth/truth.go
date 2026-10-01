// Package truth は ADS-B から作った真値 CSV（VERIFY.md）を読み、機体ごとの
// 航跡にして任意の時刻に内挿する。検証用の道具（fixverify、azoffset）が共用し、
// パイプラインは使わない。
package truth

import (
	"cmp"
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"pssrx/internal/geodesy"
	"pssrx/internal/record"
)

// timeLayout は真値の時刻（JST、タイムゾーン表記なし）。
const timeLayout = "2006-01-02T15:04:05.999999999"

// Sample は真値の 1 行（ADS-B の 1 メッセージ）、または At が内挿した位置。
type Sample struct {
	T      int64   // Unix ns
	E, N   float64 // SSR を原点にした ENU の水平位置 [m]
	AltFt  int     // 気圧高度 [ft]。HasAlt が偽なら無し
	HasAlt bool
	Squawk string // 8 進 4 桁。空なら不明
	NIC    int    // 位置の完全性カテゴリ。列が無ければ 99
}

// Aircraft は機体（ICAO アドレス）ごとの真値の航跡。
type Aircraft struct {
	ICAO     string
	Callsign string
	Samples  []Sample // 時刻順
	First    int64
	Last     int64
}

// At は時刻 t の位置を内挿する。隣接行の間隔が maxGap を超えていれば無し。
func (a *Aircraft) At(t, maxGap int64) (Sample, bool) {
	i := sort.Search(len(a.Samples), func(i int) bool { return a.Samples[i].T >= t })
	if i == len(a.Samples) {
		return Sample{}, false
	}
	if i == 0 {
		if a.Samples[0].T-t > maxGap/10 { // 先頭より前は 1 s 程度まで
			return Sample{}, false
		}
		return a.Samples[0], true
	}
	p, q := a.Samples[i-1], a.Samples[i]
	if q.T-p.T > maxGap {
		return Sample{}, false
	}
	w := float64(t-p.T) / float64(q.T-p.T)
	near := p
	if w > 0.5 {
		near = q
	}
	out := Sample{
		T: t, E: p.E + w*(q.E-p.E), N: p.N + w*(q.N-p.N),
		Squawk: near.Squawk, NIC: min(p.NIC, q.NIC),
	}
	if p.HasAlt && q.HasAlt {
		out.AltFt, out.HasAlt = int(math.Round(float64(p.AltFt)+w*float64(q.AltFt-p.AltFt))), true
	} else if near.HasAlt {
		out.AltFt, out.HasAlt = near.AltFt, true
	}
	return out, true
}

// Read は真値 CSV（VERIFY.md）を読み、機体ごとの航跡にする。位置は conv の
// 原点（SSR）の ENU に直す。高さは気圧高度を標高とみなす（水平の ENU にしか
// 使わない）。airborne 列が 0 の行（地上）は読まない。
func Read(path string, conv *geodesy.ENUConverter) ([]*Aircraft, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.ReuseRecord = true
	header, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("真値のヘッダ: %w", err)
	}
	col := func(name string) int { return slices.Index(header, name) }
	need := []string{"time_jst", "icao", "squawk", "lat", "lon", "pressure_alt_ft"}
	for _, c := range need {
		if col(c) < 0 {
			return nil, fmt.Errorf("真値に列 %q が無い（VERIFY.md 参照）", c)
		}
	}
	cT, cI, cS, cLat, cLon, cAlt := col("time_jst"), col("icao"), col("squawk"), col("lat"), col("lon"), col("pressure_alt_ft")
	cNIC, cCS, cAir := col("nic"), col("callsign"), col("airborne")
	byICAO := map[string]*Aircraft{}
	line := 1
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		line++
		if cAir >= 0 && rec[cAir] == "0" {
			continue
		}
		t, err := time.ParseInLocation(timeLayout, rec[cT], record.JST)
		if err != nil {
			return nil, fmt.Errorf("真値 %d 行目の time_jst: %w", line, err)
		}
		lat, err1 := strconv.ParseFloat(rec[cLat], 64)
		lon, err2 := strconv.ParseFloat(rec[cLon], 64)
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("真値 %d 行目の lat/lon: %q %q", line, rec[cLat], rec[cLon])
		}
		s := Sample{T: t.UnixNano(), Squawk: rec[cS], NIC: 99}
		if v := rec[cAlt]; v != "" {
			alt, err := strconv.Atoi(v)
			if err != nil {
				return nil, fmt.Errorf("真値 %d 行目の pressure_alt_ft: %q", line, v)
			}
			s.AltFt, s.HasAlt = alt, true
		}
		if cNIC >= 0 {
			if v, err := strconv.Atoi(rec[cNIC]); err == nil {
				s.NIC = v
			}
		}
		// 高さは気圧高度を標高とみなす（pssrx と同じ扱い）。水平の ENU にしか使わない
		alt := 0.0
		if s.HasAlt {
			alt = float64(s.AltFt) * 0.3048
		}
		enu, err := conv.LLAToENU(geodesy.OrthometricLLA{Lat: lat, Lon: lon, Alt: alt})
		if err != nil {
			return nil, fmt.Errorf("真値 %d 行目の位置: %w", line, err)
		}
		s.E, s.N = enu.E, enu.N
		icao := strings.ToLower(rec[cI])
		a := byICAO[icao]
		if a == nil {
			a = &Aircraft{ICAO: icao}
			byICAO[icao] = a
		}
		if cCS >= 0 && a.Callsign == "" {
			a.Callsign = rec[cCS]
		}
		a.Samples = append(a.Samples, s)
	}
	var out []*Aircraft
	for _, a := range byICAO {
		slices.SortStableFunc(a.Samples, func(x, y Sample) int { return cmp.Compare(x.T, y.T) })
		a.First, a.Last = a.Samples[0].T, a.Samples[len(a.Samples)-1].T
		out = append(out, a)
	}
	slices.SortFunc(out, func(x, y *Aircraft) int { return strings.Compare(x.ICAO, y.ICAO) })
	return out, nil
}
