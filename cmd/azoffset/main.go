// Command azoffset は pssrx の位置を ADS-B から作った真値と突き合わせ、ビーム
// 方位のずれ（真値の方位 − 点の方位）を確かめる。メイン処理は方位を補正しない
// （補正すると、出力が補正済みかどうかを後から見分けられなくなる）。ずれが
// 見つかったら、質問解析局や SSR の座標の精緻化で対応する。
//
//	azoffset -config station.yaml -ssr RJBB1 -station KX00 -fixes fixes.csv -truth truth.csv
//
// -station は質問予定表（intg）を作った質問解析局。ビーム方位は質問解析局が
// 主ビームの通過を捉えた時刻と、座標から計算した SSR から見た局の方位で決まる
// ので、一定のずれは局の座標が SSR から見て横にずれていれば説明できる。その
// 大きさ（基線長 × ずれの角度）を目安として出す。
//
// 使う点は、真値と照合できた点のうち、完全な列（列の方位の幅が -min-span-deg
// 以上。断片は中点が片側に寄る）で、SSR から -min-range-km 以上（真値の位置の
// 誤差が角度で効かない距離）、真値の NIC が -min-nic 以上のもの。照合は
// スコークの一致と、内挿した真値の位置との距離（-gate-m 以内で最も近い機体）。
// 非個別スコーク（1200 など）は機体を取り違えるので使わない。
//
// 検証用の道具で、パイプラインの一部ではない。真値 CSV の形式は VERIFY.md。
package main

import (
	"cmp"
	"encoding/csv"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"slices"
	"strconv"
	"time"

	"pssrx/internal/config"
	"pssrx/internal/geodesy"
	"pssrx/internal/geodesy/geoid"
	"pssrx/internal/truth"
)

// nonUnique は機体の識別子にならないスコーク。照合に使わない。
var nonUnique = map[string]bool{"1200": true, "2000": true, "7000": true, "1000": true, "7500": true, "7600": true, "7700": true, "0000": true}

type options struct {
	cfgPath, ssrID, stationID, fixes, truth string
	minSpanDeg, minRangeKm, maxGapS, gateM  float64
	minNIC                                  int
}

func main() {
	var o options
	flag.StringVar(&o.cfgPath, "config", "", "SSR・測定局のマスタ YAML (必須)")
	flag.StringVar(&o.ssrID, "ssr", "", "SSR の ID (必須)")
	flag.StringVar(&o.stationID, "station", "", "質問解析局の ID。intg を作った局 (必須)")
	flag.StringVar(&o.fixes, "fixes", "", "pssrx の位置 CSV（-out の 1 ファイル）(必須)")
	flag.StringVar(&o.truth, "truth", "", "真値 CSV (必須)")
	flag.Float64Var(&o.minSpanDeg, "min-span-deg", 4.5, "使う列の方位の幅の下限 [deg]。完全な列だけを使う")
	flag.Float64Var(&o.minRangeKm, "min-range-km", 20, "使う点の SSR からの距離の下限 [km]")
	flag.Float64Var(&o.maxGapS, "max-gap-s", 10, "真値の内挿を許す隣接行の間隔 [s]")
	flag.Float64Var(&o.gateM, "gate-m", 3000, "真値との照合の距離の上限 [m]")
	flag.IntVar(&o.minNIC, "min-nic", 7, "使う真値の NIC の下限")
	flag.Parse()
	if o.cfgPath == "" || o.ssrID == "" || o.stationID == "" || o.fixes == "" || o.truth == "" {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(o); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// residual は 1 点の方位の残差（真値 − 点）。
type residual struct {
	icao    string
	rad     float64 // 真値の方位 − 点の方位 [rad]
	rangeM  float64 // SSR からの水平距離 [m]
	bearing float64 // 真値の方位 [rad]
}

func run(o options) error {
	cfg, err := config.Load(o.cfgPath)
	if err != nil {
		return err
	}
	ssr, err := cfg.SSR(o.ssrID)
	if err != nil {
		return err
	}
	station, err := cfg.Station(o.stationID)
	if err != nil {
		return err
	}
	gm, err := geoid.Load()
	if err != nil {
		return err
	}
	conv, err := geodesy.NewENUConverter(ssr.LLA(), gm)
	if err != nil {
		return err
	}
	ac, err := truth.Read(o.truth, conv)
	if err != nil {
		return err
	}
	bySquawk := map[string][]*truth.Aircraft{}
	for _, a := range ac {
		seen := map[string]bool{}
		for _, s := range a.Samples {
			if s.Squawk != "" && !seen[s.Squawk] {
				seen[s.Squawk] = true
				bySquawk[s.Squawk] = append(bySquawk[s.Squawk], a)
			}
		}
	}
	res, read, err := match(o, bySquawk)
	if err != nil {
		return err
	}
	fmt.Printf("真値 %d 機体、位置 %d 点のうち使った点 %d（%d 機体）\n", len(ac), read, len(res), countAircraft(res))
	if len(res) < 30 {
		return fmt.Errorf("使える点が %d しかない（30 未満）。期間を延ばすか条件を緩める", len(res))
	}

	// 同じ機体の点は独立でない（機体ごとに偏りが違う）ので、不確かさは機体
	// ごとの中央値のばらつきから見る
	all := summarize(res)
	perAC := aircraftMedians(res)
	acMed, acSigma := robust(perAC)
	acSE := func(n int) float64 { return 1.2533 * acSigma / math.Sqrt(float64(n)) }
	fmt.Println()
	fmt.Println("== 方位の残差（真値 − 点）[deg]")
	fmt.Printf("  全体            n %6d  中央値 %+7.4f  頑健σ %6.4f\n", all.n, deg(all.median), deg(all.sigma))
	fmt.Printf("  機体ごとの中央値 %d 機体  中央値 %+7.4f  機体間の頑健σ %6.4f  標準誤差 %6.4f\n",
		len(perAC), deg(acMed), deg(acSigma), deg(acSE(len(perAC))))

	fmt.Println()
	fmt.Println("== 距離帯別 [deg]（距離で変わるなら真値の誤差や距離の偏りを疑う）")
	for _, b := range [][2]float64{{20, 40}, {40, 60}, {60, 100}, {100, 1000}} {
		var sub []residual
		for _, r := range res {
			if r.rangeM >= b[0]*1000 && r.rangeM < b[1]*1000 {
				sub = append(sub, r)
			}
		}
		if len(sub) < 10 {
			continue
		}
		s := summarize(sub)
		fmt.Printf("  %4.0f-%-4.0f km    n %6d  中央値 %+7.4f  頑健σ %6.4f\n", b[0], b[1], s.n, deg(s.median), deg(s.sigma))
	}

	fmt.Println()
	fmt.Println("== 方位（45° ごと）別 [deg]。機体ごとの中央値の中央値と、全体からの外れ（機体単位の標準誤差の何倍か）")
	fmt.Println("   方位で変わるずれ（SSR の座標のずれ、アンテナの回転むらなど）は一定の値では説明できない")
	outliers := 0
	for k := range 8 {
		var sub []residual
		for _, r := range res {
			if int(r.bearing/(math.Pi/4)) == k {
				sub = append(sub, r)
			}
		}
		meds := aircraftMedians(sub)
		if len(meds) < 5 {
			fmt.Printf("  %3d-%-3d°   点 %6d  機体 %3d\n", k*45, k*45+45, len(sub), len(meds))
			continue
		}
		m := median(meds)
		z := (m - acMed) / acSE(len(meds))
		mark := ""
		if math.Abs(z) > 3 {
			mark = "  ←"
			outliers++
		}
		fmt.Printf("  %3d-%-3d°   点 %6d  機体 %3d  中央値 %+7.4f  外れ %+5.1f%s\n", k*45, k*45+45, len(sub), len(meds), deg(m), z, mark)
	}
	if outliers > 0 {
		fmt.Printf("  %d 個の扇形が全体から 3 標準誤差以上外れている。方位によって変わるずれがある\n", outliers)
	}

	// 一定のずれは機体ごとの中央値の中央値（交通の多い機体に引っ張られない）で
	// 見る。質問解析局の座標で説明するなら、局が SSR から見て横にずれている量は
	// 基線長 × ずれの角度。点の方位 = 真の方位 + （座標から計算した局の方位 −
	// 真の局の方位）なので、残差（真値 − 点）が負なら座標の方位が時計回りに
	// 大きすぎ、局は座標より反時計回りの側にある
	baseline, _, err := config.Baseline(ssr, station, gm)
	if err != nil {
		return err
	}
	side := "反時計回り"
	if acMed > 0 {
		side = "時計回り"
	}
	fmt.Println()
	fmt.Println("== 解釈の目安")
	fmt.Printf("  一定のずれ %+.4f° ± %.4f°（機体単位の標準誤差）\n", deg(acMed), deg(acSE(len(perAC))))
	fmt.Printf("  質問解析局 %s の座標で説明するなら、局は座標より SSR から見て%sの側に %.1f m（基線 %.0f m × %.6f rad）\n",
		station.ID, side, baseline*math.Abs(acMed), baseline, math.Abs(acMed))
	fmt.Println("  ほかの要因: SSR の座標、主ビームの通過時刻の捉え方の偏り、真値（ADS-B）の偏り。方位で変わるずれは一定の値では説明できない")
	return nil
}

// match は位置 CSV を読み、条件を満たす点の残差を返す。read は読んだ点の数。
func match(o options, bySquawk map[string][]*truth.Aircraft) ([]residual, int, error) {
	f, err := os.Open(o.fixes)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	header, err := r.Read()
	if err != nil {
		return nil, 0, fmt.Errorf("位置のヘッダ: %w", err)
	}
	cols := map[string]int{}
	for _, c := range []string{"time_utc", "squawk", "e_m", "n_m", "azimuth_rad", "azimuth_first_rad", "azimuth_last_rad", "drop"} {
		i := slices.Index(header, c)
		if i < 0 {
			return nil, 0, fmt.Errorf("位置の CSV に列 %q が無い（pssrx の -out の出力か確認）", c)
		}
		cols[c] = i
	}
	maxGap := int64(o.maxGapS * 1e9)
	minSpan := o.minSpanDeg * math.Pi / 180
	var out []residual
	read := 0
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, 0, err
		}
		if rec[cols["drop"]] != "" {
			continue
		}
		read++
		sq := rec[cols["squawk"]]
		if nonUnique[sq] {
			continue
		}
		first, _ := strconv.ParseFloat(rec[cols["azimuth_first_rad"]], 64)
		last, _ := strconv.ParseFloat(rec[cols["azimuth_last_rad"]], 64)
		if math.Abs(math.Remainder(last-first, 2*math.Pi)) < minSpan {
			continue
		}
		t, err := time.Parse("2006-01-02T15:04:05.999999999Z", rec[cols["time_utc"]])
		if err != nil {
			return nil, 0, err
		}
		e, _ := strconv.ParseFloat(rec[cols["e_m"]], 64)
		n, _ := strconv.ParseFloat(rec[cols["n_m"]], 64)
		az, _ := strconv.ParseFloat(rec[cols["azimuth_rad"]], 64)
		var best *truth.Aircraft
		var bestS truth.Sample
		bestD := o.gateM
		for _, a := range bySquawk[sq] {
			if t.UnixNano() < a.First-maxGap || t.UnixNano() > a.Last+maxGap {
				continue
			}
			s, ok := a.At(t.UnixNano(), maxGap)
			if !ok || s.Squawk != sq {
				continue
			}
			if d := math.Hypot(e-s.E, n-s.N); d <= bestD {
				best, bestS, bestD = a, s, d
			}
		}
		if best == nil || bestS.NIC < o.minNIC {
			continue
		}
		rng := math.Hypot(bestS.E, bestS.N)
		if rng < o.minRangeKm*1000 {
			continue
		}
		bearing := math.Mod(math.Atan2(bestS.E, bestS.N)+2*math.Pi, 2*math.Pi)
		out = append(out, residual{icao: best.ICAO, rad: math.Remainder(bearing-az, 2*math.Pi), rangeM: rng, bearing: bearing})
	}
	return out, read, nil
}

type summary struct {
	n             int
	median, sigma float64
}

// summarize は残差の中央値と頑健な標準偏差。
func summarize(res []residual) summary {
	v := make([]float64, len(res))
	for i, r := range res {
		v[i] = r.rad
	}
	m, sigma := robust(v)
	return summary{n: len(v), median: m, sigma: sigma}
}

// robust は中央値と頑健な標準偏差（1.4826 × MAD）。
func robust(v []float64) (m, sigma float64) {
	m = median(v)
	dev := make([]float64, len(v))
	for i, x := range v {
		dev[i] = math.Abs(x - m)
	}
	return m, 1.4826 * median(dev)
}

// aircraftMedians は機体ごとの残差の中央値。
func aircraftMedians(res []residual) []float64 {
	by := map[string][]residual{}
	for _, r := range res {
		by[r.icao] = append(by[r.icao], r)
	}
	var out []float64
	for _, v := range by {
		out = append(out, summarize(v).median)
	}
	return out
}

func countAircraft(res []residual) int {
	seen := map[string]bool{}
	for _, r := range res {
		seen[r.icao] = true
	}
	return len(seen)
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return math.NaN()
	}
	s := slices.Clone(v)
	slices.SortFunc(s, cmp.Compare[float64])
	if len(s)%2 == 1 {
		return s[len(s)/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}

func deg(rad float64) float64 { return rad * 180 / math.Pi }
