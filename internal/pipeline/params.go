package pipeline

import (
	"math"

	"pssrx/internal/config"
	"pssrx/internal/interrogator"
)

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
