package pssr

import (
	"fmt"
	"math"

	"pssrx/internal/geodesy"
	"pssrx/internal/physics"
)

// Position は推定した機体の位置。
type Position struct {
	Lat float64 // WGS84 [deg]
	Lon float64 // WGS84 [deg]
	Alt float64 // 標高 [m]。気圧高度から換算したもの
	// GroundRangeM は SSR からの地上距離 [m]（SSR の ENU 平面上の距離）。
	GroundRangeM float64
	// RangeSSRM / RangeStationM は SSR・応答局から機体までの斜距離 [m]。
	RangeSSRM     float64
	RangeStationM float64
}

// Fix はプロットとその位置。
type Fix struct {
	Plot
	Position Position
}

// LocateStats は位置推定の件数。
type LocateStats struct {
	In         int
	TooClose   int // 双基地和が基線の内側の最小値より小さく、基線の近傍に解が要る
	OutOfRange int // 覆域の外まで探しても双基地和に届かない
	Out        int
}

// Locator は単局の位置推定。
//
// 機体は「SSR を焦点の一つとする双基地距離の回転楕円体」「SSR からの
// ビーム方位 θ の鉛直面」「気圧高度 h」の交点にある。SSR を原点にした
// ENU で、方位 θ に地上距離 r、高度 h の点 P(r) を置くと、双基地和
// f(r) = |P−SSR| + |P−局| は r ≥ d（d は基線長）で単調増加なので、
// f(r) = (τ − 応答遅延)·c を二分法で解く。地球の曲率は P(r) を ENU から
// 緯度経度に直す際に geodesy が扱う。大気屈折は無視する。
type Locator struct {
	conv    *geodesy.ENUConverter
	station geodesy.ENU
	dM      float64 // 基線長（水平）
	delayNs int64
	maxR    float64
	stats   LocateStats
}

// NewLocator は Locator を作る。
func NewLocator(ssr, station geodesy.OrthometricLLA, geoid geodesy.GeoidHeightProvider, params Params, cfg Config) (*Locator, error) {
	if params.MaxRangeM <= 0 {
		return nil, fmt.Errorf("覆域が不正: %g", params.MaxRangeM)
	}
	conv, err := geodesy.NewENUConverter(ssr, geoid)
	if err != nil {
		return nil, err
	}
	st, err := conv.LLAToENU(station)
	if err != nil {
		return nil, err
	}
	return &Locator{
		conv: conv, station: st,
		dM:      math.Hypot(st.E, st.N),
		delayNs: cfg.TransponderDelayNs,
		maxR:    params.MaxRangeM,
	}, nil
}

// Stats は現時点の集計。
func (l *Locator) Stats() LocateStats { return l.stats }

// Locate はプロットの位置を求める。解けなければ ok が偽。
func (l *Locator) Locate(p Plot) (Fix, bool) {
	l.stats.In++
	sum := float64(p.TauNs-l.delayNs) * physics.SpeedOfLightMPerNs
	h := HeightFromPressureAltitude(p.AltitudeFt)
	sinT, cosT := math.Sincos(p.Azimuth)

	// 方位 θ、地上距離 r、標高 h の点。U は標高が h になるよう決める
	at := func(r float64) (geodesy.ENU, error) {
		return l.pointAt(r*sinT, r*cosT, h)
	}
	f := func(r float64) (float64, geodesy.ENU, error) {
		q, err := at(r)
		if err != nil {
			return 0, q, err
		}
		return l.bistatic(q), q, nil
	}

	// r >= d で f は単調増加。基線の内側は解が 2 つありうるので扱わない
	lo := l.dM
	flo, _, err := f(lo)
	if err != nil {
		return Fix{}, false
	}
	if sum < flo {
		l.stats.TooClose++
		return Fix{}, false
	}
	hi := l.maxR * 1.05
	fhi, _, err := f(hi)
	if err != nil || sum > fhi {
		l.stats.OutOfRange++
		return Fix{}, false
	}
	// 二分法。1 mm まで詰める。400 km / 1 mm で 29 回
	for hi-lo > 1e-3 {
		mid := lo + (hi-lo)/2
		fm, _, err := f(mid)
		if err != nil {
			return Fix{}, false
		}
		if fm < sum {
			lo = mid
		} else {
			hi = mid
		}
	}
	r := lo + (hi-lo)/2
	q, err := at(r)
	if err != nil {
		return Fix{}, false
	}
	lla, err := l.conv.ENUToLLA(q)
	if err != nil {
		return Fix{}, false
	}
	l.stats.Out++
	return Fix{
		Plot: p,
		Position: Position{
			Lat: lla.Lat, Lon: lla.Lon, Alt: lla.Alt,
			GroundRangeM:  r,
			RangeSSRM:     math.Sqrt(q.E*q.E + q.N*q.N + q.U*q.U),
			RangeStationM: l.distToStation(q),
		},
	}, true
}

// pointAt は ENU の水平位置 (e, n) で標高が h になる点を返す。
//
// 標高は U にほぼ比例し、曲率のぶんだけずれる。U の初期値を h として
// 「標高の誤差ぶん U を戻す」反復で 1 mm 未満に収束する。
func (l *Locator) pointAt(e, n, h float64) (geodesy.ENU, error) {
	u := h
	for range 8 {
		lla, err := l.conv.ENUToLLA(geodesy.ENU{E: e, N: n, U: u})
		if err != nil {
			return geodesy.ENU{}, err
		}
		d := lla.Alt - h
		u -= d
		if math.Abs(d) < 1e-4 {
			break
		}
	}
	return geodesy.ENU{E: e, N: n, U: u}, nil
}

func (l *Locator) bistatic(q geodesy.ENU) float64 {
	return math.Sqrt(q.E*q.E+q.N*q.N+q.U*q.U) + l.distToStation(q)
}

func (l *Locator) distToStation(q geodesy.ENU) float64 {
	de, dn, du := q.E-l.station.E, q.N-l.station.N, q.U-l.station.U
	return math.Sqrt(de*de + dn*dn + du*du)
}

// HeightFromPressureAltitude は Mode C の気圧高度 [ft] を標高 [m] に直す。
//
// 気圧高度は標準大気（1013.25 hPa）基準で、実際の気圧配置とは数百 m
// 違いうる。QNH による補正はここに集約して後から足す。いまは単位換算のみ。
func HeightFromPressureAltitude(ft int) float64 {
	return float64(ft) * 0.3048
}
