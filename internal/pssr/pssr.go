// Package pssr は測定局で受信した Mode A/C 応答から機体の位置を推定する。
//
// 処理は決まった手続きの直列で、pipeline が 1 分ブロックごとに順に呼ぶ。
//
//	Pair      応答を質問予定と対応づけ、機体ごと・ドウェルごとのプロットにする
//	Suppress  サイドローブと反射による幽霊プロットを落とす
//	Locate    双基地距離・ビーム方位・気圧高度から位置を解く
//	Sink      位置を書く
//
// 投入の刻みと手続きを切り離すため、質問予定と応答は Synchronizer に投入し、
// 閉じた形で処理できる範囲を切り出してから Pair に渡す。持ち越す記録は
// Synchronizer（時刻同期の待ち行列）、RunState（開いている列）、SuppressState
// （判定待ちのプロット）で、手続きはそれらを引数に取る関数として書いてある。
// 件数の集計は呼び出し側が渡す Stats に足す。
package pssr

import (
	"fmt"
	"math"

	"pssrx/internal/record"
	"pssrx/internal/ssr"
)

// 質問種別のデータ上のコード。応答符号の意味（スコークか高度か）を決める。
var (
	ModeA = ssr.ModeCode['A']
	ModeC = ssr.ModeCode['C']
)

// Params は局と SSR の組に固有の値。設定からの導出は pipeline が担う。
type Params struct {
	SSRID     string
	StationID string // 応答を受信した局
	// TauMinNs は取りうる最小の遅延 [ns]。応答遅延 + 基線長 / c。
	// これより短い経路は無いので、遅延がこれを下回る質問は候補にならない。
	TauMinNs int64
	// TauMaxNs は取りうる最大の遅延 [ns]。応答遅延 + (2·覆域 + 基線長) / c。
	// これを超える応答は捨てる。PRI より小さくなければ前後の質問と取り違える。
	TauMaxNs int64
	// AroundTimeNs は SSR の走査周期 [ns]。同じ走査のプロットの判定に使う。
	AroundTimeNs int64
	// MeanPRINs は質問 1 発あたりの平均間隔 [ns]。走査周期との比が 1 質問
	// あたりのビームの回転角で、応答列の欠けを方位誤差に換算するのに使う。
	MeanPRINs float64
	// MaxRangeM は SSR の覆域 [m]。位置の探索範囲の上限に使う。
	MaxRangeM float64
}

// Config は手続きの定数。局や SSR によらない。
type Config struct {
	// TauToleranceNs は同じ列とみなす τ の差の上限 [ns]。応答遅延
	// （ssr.TransponderDelayNs）の公差 ±0.5 µs が支配的で、1 ドウェル内の
	// 機体の移動は 100 ns に満たない。
	TauToleranceNs int64
	// MaxGap は列の途中で応答の無い質問を何回まで許すか。これを超えて
	// 途切れたら列を閉じる。
	MaxGap int
	// MinReplies は列として残す最小の応答数。これ未満は FRUIT とみなして捨てる。
	MinReplies int
	// MaxAltitudeFt は列の気圧高度として認める上限 [ft]。Mode C の符号は
	// 126,700 ft まで表せるが、民間機は 51,000 ft までしか上がらない。
	// それより上は FRUIT の偶然の一致で組まれた列の無作為な符号とみなして
	// 捨てる。高高度の軍用機（70,000 ft 級）まで拾うなら上げる。
	MaxAltitudeFt int
	// MaxRetentionNs は Synchronizer が溜めておく時間幅の上限 [ns]。質問予定か
	// 応答の片方が止まったとき、他方が溜まり続けないための安全弁。2 分は
	// 投入の刻みの最大（1 分ファイル）の 2 倍で、投入の遅れや乱れがあっても
	// 対になる前に捨てないことがはっきりする。取り出しの後に適用する。
	MaxRetentionNs int64

	// 以下は幽霊抑圧（Suppress）の閾値。
	//
	// SameScanFraction は同じ走査とみなす時刻差の上限を走査周期に対する
	// 割合で表す。反射体の方向は機体と無関係なので方位では絞らない。
	SameScanFraction float64
	// AltitudeToleranceFt は同じ機体とみなす高度差の上限 [ft]。同じ走査の
	// 中でも最大 2 秒ずれるので、上昇・降下中は 100 ft の境界をまたぐ。
	AltitudeToleranceFt int
	// DirectTauToleranceNs は直接照射（主ビーム・サイドローブ・ガーブルで
	// 割れた列）とみなす τ の幅 [ns]。同じ走査内の機体の移動（2 秒 ×
	// 250 m/s ≈ 1.7 µs）を吸収し、反射（経路差 6〜30 km ≈ 20〜100 µs）
	// とは十分離れる値にする。
	DirectTauToleranceNs int64
	// ResolveAzimuthSeparationRad は、τ の一致する同じ機体のプロットを
	// 「同じドウェルの断片（主ビームとサイドローブ）」と「像（SSR 近傍の
	// 反射体経由）」に分ける方位差 [rad]。以内なら Suppress が応答数最多を
	// 残して併合し、超えれば両方を通して便の文脈（Resolve）で決める。
	// 実データでは断片の方位差が 2〜8° に集中し、像は 10° 以上に一様に
	// 分布する（第 1 サイドローブが 3〜6°、反射体の方向は機体と無関係）。
	ResolveAzimuthSeparationRad float64

	// 以下は位置推定（Locate）の定数。
	//
	// ZMarginM は解の一意性判定 |z| < ℓ に持たせる余裕 [m]。SSR 直上の
	// 除外円錐の広さを決める。精度のためではなく曖昧さを確実に避けるための
	// 余裕で、数百 m あれば足りる。
	ZMarginM float64
	// BaselineMarginM は L − B（双基地距離 − 基線の水平成分）の下限 [m]。
	// 機体が基線上に近いと解が発散する。z > 0 なら一意性判定が弾くが、
	// z ≈ 0 では効かないので、その安全弁
	BaselineMarginM float64
	// CurvatureTolM は曲率の反復の収束判定 [m]。収縮率が 1/500 程度なので、
	// ここで止めても z の誤差はこの 1/500 に収まる。
	CurvatureTolM float64
	// CurvatureMaxIter は曲率の反復の上限回数。通常 2〜3 回で収束する。
	CurvatureMaxIter int
	// SigmaTimingNs は質問時刻と受信時刻のジッタを合成した標準偏差 [ns]。
	// intg の 100 ns 量子化と応答のジッタ（実測 RMS 60 ns）。
	SigmaTimingNs float64
	// SigmaTransponderNs は応答遅延の公差 ±0.5 µs を一様分布とみなした
	// 標準偏差 [ns]（0.5 / √3）。機体ごとの系統誤差として残る。
	SigmaTransponderNs float64
	// SigmaAzimuthRad は応答列が完全なとき（応答数が DwellFullReplies 以上）
	// のビーム中心の方位の標準偏差 [rad]。ADS-B の真値との比較（KX00、
	// 応答 14 件以上）で誤差の中央値 0.11°、p90 0.33°。裾が正規分布より重い
	// ので、p90 が 1.64σ に収まる 0.20° にする（1 質問あたりのビームの回転
	// 約 0.26° と同程度）。
	SigmaAzimuthRad float64
	// DwellFullReplies は応答列を完全とみなす応答数。これに満たない列は
	// ドウェルの断片で、最初と最後の中点がビームの片側に寄る。方位の標準
	// 偏差は欠けた応答 1 件あたり AzimuthFragmentFactor × 1 質問あたりの
	// 回転角だけ増す（azimuthSigma）。真値との比較では欠けに対してほぼ
	// 線形で、14 件を境に 0.13° から 3 件で約 2° まで増えた。
	DwellFullReplies int
	// AzimuthFragmentFactor は欠けた応答 1 件あたりに増す方位の標準偏差を、
	// 1 質問あたりの回転角に対する倍率で表す。
	AzimuthFragmentFactor float64
	// SigmaAltitudeM は高さの標準偏差 [m]。Mode C の 100 ft 量子化（30.48 / √12）。
	SigmaAltitudeM float64

	// 以下は連続性の判定（Track）の定数。
	//
	// TrackMaxSpeedMps は門の速度上限 [m/s]。前の点からの移動がこれ × Δt に
	// 位置の誤差を足した幅を超えたら別の便。
	TrackMaxSpeedMps float64
	// TrackMaxClimbFtps は門の高度変化率の上限 [ft/s]。6,000 ft/min。
	TrackMaxClimbFtps float64
	// TrackGateSigmas は門に足す位置の標準偏差の倍率。
	TrackGateSigmas float64
	// TrackMaxMissedScans は便を打ち切らずに許す欠測の走査数。
	TrackMaxMissedScans int
	// TrackConfirmHits は便を確定するのに要る点数。確定しなかった便の点は
	// 棄却する。
	TrackConfirmHits int

	// 以下は断片の連結（Link）の定数。
	//
	// LinkMaxGapNs はフライトの末尾から次の断片の先頭までに許す切れ目 [ns]。
	// 下限は Track の打ち切り幅（それより短い切れ目は Track が繋がなかった
	// もの）。
	LinkMaxGapNs int64
	// LinkVelocityToleranceMps は末尾の速度で外挿した位置に持たせる幅の
	// 速度換算 [m/s]。切れ目 × これ + 3σ が門になる。
	LinkVelocityToleranceMps float64
	// LinkClimbToleranceFtps は高度の外挿に持たせる幅の変化率換算 [ft/s]。
	LinkClimbToleranceFtps float64

	// 以下は同一機体のフライトの重複解消（Resolve）の定数。
	// 方位差は ResolveAzimuthSeparationRad（抑圧と共用）。
	//
	// ResolveTauToleranceNs は同じ機体とみなす τ の差の上限 [ns]。近傍反射の
	// 経路差（実測 p90 で 5 µs 未満）。
	ResolveTauToleranceNs int64
	// ResolveAltitudeToleranceFt は同じ機体とみなす高度差の上限 [ft]。
	ResolveAltitudeToleranceFt int
	// ResolveConfirmScans は同じ機体と決めるのに要る一致した走査数。
	ResolveConfirmScans int
	// ResolveMaxHoldScans は組の判定を待って点を保留する上限の
	// 走査数。超えた点は ambiguous として先に出す。出力の遅れの上限になる
	// ので、遅れを抑えたいときに小さくする（判定の材料が減り ambiguous が
	// 増える）。
	ResolveMaxHoldScans int
}

// DefaultConfig は既定の定数。実データの分布を見て調整する。
func DefaultConfig() Config {
	return Config{
		TauToleranceNs: 1000, MaxGap: 2, MinReplies: 3, MaxAltitudeFt: 60_000, MaxRetentionNs: 2 * 60_000_000_000,
		SameScanFraction: 0.75, AltitudeToleranceFt: 200, DirectTauToleranceNs: 5000, ResolveAzimuthSeparationRad: 10 * math.Pi / 180,
		ZMarginM: 200, BaselineMarginM: 500, CurvatureTolM: 0.05, CurvatureMaxIter: 5,
		SigmaTimingNs: 100, SigmaTransponderNs: 500 / math.Sqrt(3),
		SigmaAzimuthRad: 0.20 * math.Pi / 180, DwellFullReplies: 14, AzimuthFragmentFactor: 0.77, SigmaAltitudeM: 30.48 / math.Sqrt(12),
		TrackMaxSpeedMps: 350, TrackMaxClimbFtps: 100, TrackGateSigmas: 3, TrackMaxMissedScans: 2, TrackConfirmHits: 3,
		LinkMaxGapNs: 60_000_000_000, LinkVelocityToleranceMps: 60, LinkClimbToleranceFtps: 50,
		ResolveTauToleranceNs: 5000, ResolveAltitudeToleranceFt: 300, ResolveConfirmScans: 3, ResolveMaxHoldScans: 150,
	}
}

// Validate は Params と Config の整合を検査する。手続きに入る前に 1 度呼ぶ。
func Validate(params Params, cfg Config) error {
	if params.TauMinNs < 0 || params.TauMaxNs <= params.TauMinNs {
		return fmt.Errorf("遅延の窓が不正: TauMin=%d TauMax=%d", params.TauMinNs, params.TauMaxNs)
	}
	if params.AroundTimeNs <= 0 {
		return fmt.Errorf("走査周期が不正: %d", params.AroundTimeNs)
	}
	if params.MeanPRINs <= 0 {
		return fmt.Errorf("質問間隔が不正: %g", params.MeanPRINs)
	}
	if params.MaxRangeM <= 0 {
		return fmt.Errorf("覆域が不正: %g", params.MaxRangeM)
	}
	if cfg.TauToleranceNs <= 0 || cfg.MaxGap < 0 || cfg.MinReplies < 1 || cfg.MaxAltitudeFt <= 0 || cfg.MaxRetentionNs <= 0 {
		return fmt.Errorf("対応づけの定数が不正: %+v", cfg)
	}
	if !(cfg.SameScanFraction > 0 && cfg.SameScanFraction < 1) || cfg.AltitudeToleranceFt < 0 || cfg.DirectTauToleranceNs <= 0 ||
		cfg.ResolveAzimuthSeparationRad <= 0 || cfg.ResolveAzimuthSeparationRad > math.Pi {
		return fmt.Errorf("抑圧の定数が不正: %+v", cfg)
	}
	if cfg.ZMarginM < 0 || cfg.BaselineMarginM < 0 || cfg.CurvatureTolM <= 0 || cfg.CurvatureMaxIter < 1 || cfg.SigmaTimingNs < 0 ||
		cfg.SigmaTransponderNs < 0 || cfg.SigmaAzimuthRad < 0 || cfg.SigmaAltitudeM < 0 ||
		cfg.DwellFullReplies < 0 || cfg.AzimuthFragmentFactor < 0 {
		return fmt.Errorf("位置推定の定数が不正: %+v", cfg)
	}
	if cfg.TrackMaxSpeedMps <= 0 || cfg.TrackMaxClimbFtps < 0 || cfg.TrackGateSigmas < 0 ||
		cfg.TrackMaxMissedScans < 0 || cfg.TrackConfirmHits < 1 {
		return fmt.Errorf("連続性の定数が不正: %+v", cfg)
	}
	if cfg.LinkMaxGapNs <= 0 || cfg.LinkVelocityToleranceMps <= 0 || cfg.LinkClimbToleranceFtps < 0 {
		return fmt.Errorf("連結の定数が不正: %+v", cfg)
	}
	if cfg.ResolveTauToleranceNs <= 0 || cfg.ResolveAltitudeToleranceFt < 0 || cfg.ResolveConfirmScans < 1 || cfg.ResolveMaxHoldScans < 1 {
		return fmt.Errorf("重複解消の定数が不正: %+v", cfg)
	}
	return nil
}

// PairedReply は質問と対応づいた応答 1 件。
type PairedReply struct {
	Interrogation record.Interrogation
	Reply         record.Reply
	TauNs         int64 // 受信時刻 − 質問時刻
}

// Plot は 1 機体 × 1 ドウェルの要約。どの SSR・局の処理かはプロットごとの
// 情報ではなく処理の文脈（Params）なので持たない。
type Plot struct {
	Timestamp int64   // 列の最初と最後の質問時刻の中点 [ns]
	Azimuth   float64 // 列の最初と最後の質問方位の中点 [rad], [0, 2pi)
	TauNs     int64   // τ の平均（偶数丸め）
	Squawk    uint16  // 列の Mode A 応答を復号したスコーク（8 進 4 桁）
	// AltitudeFt は列の Mode C 応答から決めた気圧高度 [ft]。
	// 復号できる符号の高度が 100 ft 以内に収まるとき、プロット時刻に
	// 最も近い応答の高度をとる。決まらない列はプロットにしない。
	AltitudeFt int
	// Replies は元の応答列。生の応答符号はここから復号し直せる。
	Replies []PairedReply
}

// Stats は全段の件数。呼び出し側が持ち、各手続きに渡して足し込む。
type Stats struct {
	// Synchronizer
	Replies        int // 受け取った応答
	DroppedIntg    int // 取り出し済みより古い、または保持幅を超えて捨てた質問予定
	DroppedReplies int // 同じく応答

	// Pair
	Unpaired        int // どの質問とも対にならない（TauMax 超、または遡る質問が無い）
	Paired          int // 質問と対応づいた
	Runs            int // 閉じた列
	RunsSingle      int // 閉じた列のうち応答が 1 件だけのもの（FRUIT の孤立応答）
	RunsTooShort    int // 閉じたが MinReplies 未満で捨てた
	NoModeA         int // Mode A 応答が無い（スコークが決まらない）
	NoAltitude      int // Mode C 応答が無い、または全部復号できない
	AltitudeSpread  int // 復号した高度が 100 ft を超えて散っている（ガーブル）
	AltitudeTooHigh int // 高度が MaxAltitudeFt を超える（FRUIT の偶然の一致）
	Plots           int
	Tau             Histogram // τ の分布。窓（TauMin, TauMax）の妥当性を見る
	// Assignments と OpenRunsTotal は列の割り当ての負荷を見る。割り当て 1 回に
	// つき、そのとき開いていた列の数を OpenRunsTotal に足す。平均の開いている
	// 列数 OpenRunsTotal / Assignments と、孤立応答の割合 RunsSingle / Runs が
	// 対応づけの性能の前提（開いている列は数十本、閉じた列の大半は孤立）を
	// 満たしているかの目安になる。
	Assignments   int
	OpenRunsTotal int64

	// Suppress
	Sidelobe  int // 直接照射の候補のうち応答数で負けた
	Multipath int // τ が直接照射より大きい
	Kept      int

	// Locate
	Inconsistent  int // 双基地距離が 0 以下、または座標変換の失敗
	Baseline      int // 双基地距離が基線の水平成分 + 余裕 以下（基線特異点）
	Ambiguous     int // 正根が 2 つ（機体が基線の近傍）
	NoSolution    int // 与えた高さに解が無い
	NonConvergent int // 曲率の反復が収束しない
	Fixes         int

	// Track
	Tracks           int // 開いた便
	TracksConfirmed  int // 確定した便
	FixesOK          int // 確定した便の点
	FixesUnconfirmed int // 確定しなかった便の点（棄却）
	TrackHeldMax     int // 保留した点の最大件数。常駐運転で増え続けないことの確認用

	// Link
	Flights        int // 開いたフライト
	Links          int // 既存のフライトに連結した便
	LinkedRescued  int // 連結で unconfirmed から ok になった点
	FlightsOpenMax int // 開いているフライトの最大数

	// Resolve
	ResolvePairs         int // 同じ機体と疑われた組
	ResolveConfirmed     int // 同じ機体と決めた組
	ResolvedByContinuity int // 存在区間の包含で実位置を決めた組
	ResolveAmbiguous     int // 決められなかった組
	FixesEcho            int // 像の点
	FixesAmbiguous       int // 決められなかった重なりの点
	ResolveHeldMax       int // 保留した点の最大件数
}

// NewStats は τ の分布のビンを窓に合わせて用意した Stats を返す。
func NewStats(params Params) Stats {
	const bins = 64
	return Stats{Tau: Histogram{
		// 上限の 1.25 倍までを見る。窓のすぐ外に何があるかを確かめるため
		BinNs:  (params.TauMaxNs*5/4 + bins - 1) / bins,
		Counts: make([]int, bins),
	}}
}

// Histogram は固定幅のビンの度数分布。
type Histogram struct {
	BinNs  int64
	Counts []int // [k*BinNs, (k+1)*BinNs)
	Over   int
}

func histogramAdd(h *Histogram, v int64) {
	if h.BinNs <= 0 {
		return
	}
	k := v / h.BinNs
	if k < 0 || k >= int64(len(h.Counts)) {
		h.Over++
		return
	}
	h.Counts[k]++
}

func absInt64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func compareInt64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
