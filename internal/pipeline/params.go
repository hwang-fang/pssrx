package pipeline

import (
	"math"

	"pssrx/internal/config"
	"pssrx/internal/interrogator/analyze"
	"pssrx/internal/pattern"
)

// InterrogatorParams は設定の質問パラメータから解析用パラメータを組み立てる。
//
// 設定の書式（100 ns 単位の PRI、秒単位の走査周期）と解析本体の型
// （ns 単位）の橋渡しはここだけで行う。解析本体は設定の書式を知らない。
func InterrogatorParams(i config.Interrogation) (analyze.Params, error) {
	modes, err := pattern.ParseModes(i.Pattern)
	if err != nil {
		return analyze.Params{}, err
	}
	cycles := i.Stagger100
	if len(cycles) == 0 {
		cycles = []int64{i.QuestCycle100}
	}
	staggerNs := make([]int64, len(cycles))
	for k, v := range cycles {
		staggerNs[k] = v * 100
	}
	pat, err := pattern.FromStagger(staggerNs, modes)
	if err != nil {
		return analyze.Params{}, err
	}
	clockwise := true
	if i.Clockwise != nil {
		clockwise = *i.Clockwise
	}
	return analyze.Params{
		// 秒から ns へは切り捨てで落とす。四捨五入してはならない。
		// 走査周期はドウェル対が何回転ぶん離れているかの判定にしか使わず、
		// 許容は周期の 20% と広い。1 ns の差は効かないが、丸め方を変えると
		// 既存の出力と食い違う。
		AroundTimeNs: int64(math.Trunc(i.AroundTimeSec * 1e9)),
		Pattern:      pat,
		Clockwise:    clockwise,
	}, nil
}
