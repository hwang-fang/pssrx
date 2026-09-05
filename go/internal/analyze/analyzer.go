package analyze

import (
	"log/slog"
	"math"
	"slices"

	"pssrx/internal/store"
)

// maxBridgeRotations はブラケットが跨いでよい走査回数の上限。
//
// ドウェルを取り逃がした場合、ブラケットは走査 2 回ぶんを跨ぐ。方位を
// 線形に内挿してよいのはこの程度までで、それ以上離れた対はブラケットを
// 作らない（ブロック全体が無音だった後などに、何回転ぶんも内挿して
// しまうのを防ぐ）。
const maxBridgeRotations = 2

// wrapAngle は角度を [0, 2pi) へ畳み込む。
//
// 方位の内挿の重みはドウェル先頭で負になるため、生の値はしばしば負になる。
// math.Mod は被除数の符号を引き継ぐので負のまま返してしまい、その値を
// ファイル書き出しで uint32 へ変換すると Go の仕様上「実装依存」の結果になる
// （amd64 では 2^32 で巻き戻り、arm64 では 0 に飽和する）。ここで必ず
// 非負へ寄せておく。
func wrapAngle(x float64) float64 {
	const twoPi = 2 * math.Pi
	r := math.Mod(x, twoPi)
	if r < 0 {
		r += twoPi
	}
	return r
}

// Stats は処理量と棄却理由ごとの件数。
//
// 棄却はエラーではなく観測イベントなので、ログに流すだけだと件数が
// 埋もれる。データ品質の異変（急に連鎖長不足が増えた、走査周期と
// 整合しないドウェル対ばかりになった）に気づけるよう、解析結果と
// 一緒に取り出せるようにしてある。
type Stats struct {
	Blocks              int // Feed の呼び出し回数
	Segments            int // 時間差で切り出したセグメント数
	SegmentsTooLong     int // SSR 一周より長く、棄却したセグメント
	ChainNotFound       int // DP が連鎖を見つけられなかったセグメント
	ChainTooShort       int // 連鎖が min_chain_length に届かなかったセグメント
	ParabolaFailed      int // 放物線フィットに失敗したセグメント
	DwellsDetected      int // 検出したドウェル
	LastDwellOutOfOrder int // 引き渡されたドウェルが新しいデータより後だった回数
	RotationMismatch    int // 走査周期と整合しないドウェル対
	BracketFailed       int // 連結に失敗したドウェル対
	BracketsEmitted     int // 出力したブラケット
	RecordsEmitted      int // 出力した質問予定レコード
	PutOffRecords       int // 次ブロックへ繰り越した生データ
}

// Analyzer は 1 つの SSR・測定局の組について質問予定表を作る。
//
// 先送り生データと最終ドウェルを内部状態として持つため、時系列に沿って
// Feed を呼び続ける限りブロック境界で重複も欠落も生じない。状態は
// インスタンスに閉じており、パッケージレベルの可変状態は無いので、
// 局ごとに Analyzer を立てれば並列に走らせられる。
type Analyzer struct {
	params    Params
	cfg       Config
	stDist    float64
	stAzimuth float64
	log       *slog.Logger

	gateWH       uint16
	putOffPeriod int64

	putOff    []store.QData
	lastDwell *Dwell
	stats     Stats
}

// New は Analyzer を作る。設定やパターンが不正ならここでエラーになる。
func New(params Params, cfg Config, stDist, stAzimuth float64, log *slog.Logger) (*Analyzer, error) {
	if log == nil {
		log = slog.Default()
	}
	gate, err := store.EncodeWaveheight(cfg.AmplitudeGateDbm)
	if err != nil {
		return nil, err
	}
	return &Analyzer{
		params:       params,
		cfg:          cfg,
		stDist:       stDist,
		stAzimuth:    stAzimuth,
		log:          log,
		gateWH:       gate,
		putOffPeriod: int64(float64(params.Pattern.Period()) * cfg.DwellGapPeriods),
		stats:        Stats{},
	}, nil
}

// Stats は現在までの集計を返す。
func (a *Analyzer) Stats() Stats { return a.stats }

// PutOffPeriod は先送り判定に使う猶予時間 [ns]。
func (a *Analyzer) PutOffPeriod() int64 { return a.putOffPeriod }

// Feed は 1 ブロック分の質問データを与え、質問予定表を返す。
//
// blockEnd はこのブロックが受け持つ区間の終端 [ns]。isLast が false の間は、
// blockEnd 直前のセグメントを次のブロックへ繰り越して分断を避ける。
//
// 各ブラケットは「ドウェル A の先頭質問から B の先頭質問の直前まで」の
// 半開区間を受け持つ。B は次のブラケットの起点になるので、Feed を
// 呼び続ける限り境界で重複も欠落も生じない。全データを処理し終えた時点で
// 最終ドウェル自身の質問だけは出力されないが、これは後続のドウェルが無く
// 内挿できないためで、外挿すべき区間ではない。
func (a *Analyzer) Feed(qdata []store.QData, blockEnd int64, isLast bool) ([]store.Intg, error) {
	a.stats.Blocks++

	if len(a.putOff) > 0 {
		merged := make([]store.QData, 0, len(a.putOff)+len(qdata))
		merged = append(merged, a.putOff...)
		merged = append(merged, qdata...)
		qdata = merged
	}

	putOffTs, hasPutOff := int64(0), false
	if !isLast {
		putOffTs, hasPutOff = blockEnd-a.putOffPeriod, true
	}

	dwells, putOff, err := a.detectDwells(qdata, putOffTs, hasPutOff)
	if err != nil {
		return nil, err
	}
	a.putOff = putOff
	a.stats.PutOffRecords = len(putOff)

	// 前回の最終ドウェルを先頭に繋ぐ。ブラケットは 2 本のドウェルが揃って
	// 初めて作れるので、最終ドウェルは次の呼び出しへ引き渡す。
	if a.lastDwell != nil {
		if len(dwells) > 0 && a.lastDwell.CenterTs >= dwells[len(dwells)-1].CenterTs {
			a.stats.LastDwellOutOfOrder++
			a.log.Warn("引き渡されたドウェルが新しいデータより後のため、検出結果を破棄する",
				"last_dwell_center", a.lastDwell.CenterTs,
				"newest_center", dwells[len(dwells)-1].CenterTs)
			dwells = nil
		}
		dwells = append([]*Dwell{a.lastDwell}, dwells...)
	}
	if len(dwells) > 0 {
		a.lastDwell = dwells[len(dwells)-1]
	}
	if len(dwells) < 2 {
		return nil, nil
	}

	// 伝搬遅延（受信時刻 -> 送信時刻）
	delayNs := int64(math.RoundToEven(a.stDist / cMPerNs))
	sign := 1.0
	if !a.params.Clockwise {
		sign = -1.0
	}
	const twoPi = 2.0 * math.Pi

	var out []store.Intg
	for i := 0; i+1 < len(dwells); i++ {
		dwellA, dwellB := dwells[i], dwells[i+1]

		// ビーム中心の間隔が走査周期の整数倍かを確かめる。ドウェルを 1 本
		// 取り逃がしていれば 2 になり、方位の内挿はその分回転する。
		span := dwellB.CenterTs - dwellA.CenterTs
		rot := int64(math.RoundToEven(float64(span) / float64(a.params.AroundTimeNs)))
		if rot < 1 || rot > maxBridgeRotations ||
			math.Abs(float64(span-rot*a.params.AroundTimeNs)) > 0.2*float64(a.params.AroundTimeNs) {
			a.stats.RotationMismatch++
			a.log.Debug("走査周期と整合しないドウェル対を棄却",
				"span_ns", span, "rot", rot, "around_time_ns", a.params.AroundTimeNs)
			continue
		}

		br := interpolateBracket(a.params.Pattern, dwellA, dwellB)
		if br == nil {
			a.stats.BracketFailed++
			a.log.Debug("連結に失敗したドウェル対を棄却",
				"a_center", dwellA.CenterTs, "b_center", dwellB.CenterTs)
			continue
		}
		a.stats.BracketsEmitted++

		// 方位はビーム中心通過時刻の間を線形に内挿する。送信時刻・中心時刻の
		// 双方が同じ遅延だけずれるので、遅延補正の有無で方位は変わらない。
		for k, ts := range br.Times {
			w := float64(ts-dwellA.CenterTs) / float64(span)
			out = append(out, store.Intg{
				Timestamp: ts - delayNs,
				Azimuth:   wrapAngle(a.stAzimuth + sign*twoPi*float64(rot)*w),
				Mode:      br.Modes[k],
			})
		}
	}
	a.stats.RecordsEmitted += len(out)
	return out, nil
}

// detectDwells は振幅ゲートを通ったデータを時間差で区切り、各区間から
// 連鎖を探し、放物線フィットでビーム中心を求める。
//
// 第 2 戻り値は次ブロックへ繰り越す生データ。
func (a *Analyzer) detectDwells(qdata []store.QData, putOffTs int64, hasPutOff bool) ([]*Dwell, []store.QData, error) {
	// 最低限の波高値フィルタ
	passed := make([]store.QData, 0, len(qdata))
	for _, q := range qdata {
		if q.WH >= a.gateWH {
			passed = append(passed, q)
		}
	}
	if len(passed) == 0 {
		return nil, nil, nil
	}

	n := len(passed)
	ts := make([]int64, n)
	tf := make([]float64, n)
	md := make([]uint8, n)
	pw := make([]float64, n)
	for i, q := range passed {
		ts[i] = q.Timestamp
		tf[i] = float64(q.Timestamp)
		md[i] = q.Mode
		// 放物線フィッティングするために dBm に直しておく
		pw[i] = store.DecodeWaveheight(q.WH)
	}

	// 時間差でグルーピング
	gap := float64(a.params.Pattern.Period()) * a.cfg.DwellGapPeriods
	bounds := []int{0}
	for i := 0; i+1 < n; i++ {
		if float64(ts[i+1]-ts[i]) > gap {
			bounds = append(bounds, i+1)
		}
	}
	bounds = append(bounds, n)

	var dwells []*Dwell
	for b := 0; b+1 < len(bounds); b++ {
		lo, hi := bounds[b], bounds[b+1]
		a.stats.Segments++

		// 末尾データが含まれているので先送り
		if hasPutOff && putOffTs <= ts[hi-1] {
			return dwells, slices.Clone(passed[lo:]), nil
		}

		span := ts[hi-1] - ts[lo]
		if span > a.params.AroundTimeNs {
			// セグメントが SSR 一周より長いのはおかしい
			a.stats.SegmentsTooLong++
			a.log.Debug("SSR 一周より長いセグメントを棄却",
				"span_ns", span, "around_time_ns", a.params.AroundTimeNs, "count", hi-lo)
			continue
		}

		// 動的計画法による対象 SSR データの探査と位相チェック
		ch, err := BestChain(ts[lo:hi], tf[lo:hi], md[lo:hi], pw[lo:hi], a.params.Pattern, &a.cfg)
		if err != nil {
			return nil, nil, err
		}
		if ch == nil {
			a.stats.ChainNotFound++
			continue
		}
		if ch.Length() < a.cfg.MinChainLength {
			a.stats.ChainTooShort++
			continue
		}

		// 放物線フィッティングによるピーク探査
		center, peak, ok := FitParabola(ch.Times, ch.PowersDbm, &a.cfg)
		if !ok {
			a.stats.ParabolaFailed++
			continue
		}
		a.stats.DwellsDetected++
		dwells = append(dwells, &Dwell{CenterTs: center, PeakPowerDbm: peak, Chain: ch})
	}
	return dwells, nil, nil
}
