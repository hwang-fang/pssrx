package pipeline

import (
	"log/slog"
	"math"

	"pssrx/internal/config"
	"pssrx/internal/geodesy/geoid"
	"pssrx/internal/interrogator"
)

// InterrogatorStage は interrogator 段を 1 つ組み立てるのに要るもの。
// 設定や幾何の計算はここに含まない。
type InterrogatorStage struct {
	SSRID     string // intg の出力先を決める
	StationID string // qpkx を受信した局
	Params    interrogator.Params
	Dist      float64 // SSR から測定局への距離 [m]
	Azimuth   float64 // SSR から見た測定局の方位 [rad]
	// Intg は質問予定表の出力先。nil なら書かない。
	Intg IntgSink
	Log  *slog.Logger
}

// NewInterrogatorStage は設定から解析パラメータと局の幾何を導く。
func NewInterrogatorStage(ssr config.SSR, station config.Station) (InterrogatorStage, error) {
	params, err := InterrogatorParams(ssr.Interrogation)
	if err != nil {
		return InterrogatorStage{}, err
	}
	gm, err := geoid.Load()
	if err != nil {
		return InterrogatorStage{}, err
	}
	dist, azimuth, err := config.Geometry(ssr, station, gm)
	if err != nil {
		return InterrogatorStage{}, err
	}
	return InterrogatorStage{
		SSRID:     ssr.ID,
		StationID: station.ID,
		Params:    params,
		Dist:      dist,
		Azimuth:   azimuth,
	}, nil
}

// InterrogatorParams は設定の質問パラメータから解析用パラメータを組み立てる。
//
// 質問パターンは config が組み立てる。ここでは走査周期の秒 → ns の
// 切り捨てと clockwise の既定を足して解析本体の型に詰める。解析本体は
// 設定の書式を知らない。
func InterrogatorParams(i config.Interrogation) (interrogator.Params, error) {
	pat, err := config.InterrogationPattern(i)
	if err != nil {
		return interrogator.Params{}, err
	}
	clockwise := true
	if i.Clockwise != nil {
		clockwise = *i.Clockwise
	}
	return interrogator.Params{
		// 秒から ns へは切り捨てで落とす。四捨五入してはならない。
		// 走査周期はドウェル対が何回転ぶん離れているかの判定にしか使わず、
		// 許容は周期の 20% と広い。1 ns の差は効かないが、丸め方を変えると
		// 既存の出力と食い違う。
		AroundTimeNs: int64(math.Trunc(i.AroundTimeSec * 1e9)),
		Pattern:      pat,
		Clockwise:    clockwise,
	}, nil
}

// InterrogatorResult は interrogator 段の実行結果の要約。
type InterrogatorResult struct {
	Stats   interrogator.Stats
	Timing  Timing
	Dist    float64
	Azimuth float64
}

// RunInterrogator は src のブロックの QData を解析し、intg を st.Intg へ書き出す。
func RunInterrogator(src Source, st InterrogatorStage) (*InterrogatorResult, error) {
	res, err := run(src, &st, nil)
	if err != nil {
		return nil, err
	}
	return &res.Interrogator, nil
}
