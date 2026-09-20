// Command fixverify は pssrx の位置を ADS-B から作った真値と突き合わせ、
// 便の純度・完全性、位置誤差、連続性の判定の妥当性、相手のいない便
// （エコー・未装備機の候補）を出す。真値 CSV の形式は VERIFY.md を参照。
//
//	fixverify -config station.yaml -ssr KX90S -fixes fixes.csv -truth truth.csv [-out matched.csv]
//
// 検証用の道具で、パイプラインの一部ではない。
package main

import (
	"encoding/csv"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"pssrx/internal/config"
	"pssrx/internal/geodesy"
	"pssrx/internal/geodesy/geoid"
	"pssrx/internal/record"
)

func main() {
	var o options
	flag.StringVar(&o.cfgPath, "config", "", "SSR・測定局のマスタ YAML (必須)")
	flag.StringVar(&o.ssrID, "ssr", "", "SSR の ID。誤差を距離・方位方向に分けるための原点 (必須)")
	flag.StringVar(&o.fixes, "fixes", "", "pssrx の位置 CSV (必須)")
	flag.StringVar(&o.truth, "truth", "", "真値 CSV (必須)")
	flag.StringVar(&o.out, "out", "", "点ごとの対応を書く CSV。省略時は書かない")
	flag.Float64Var(&o.gateM, "gate-m", 3000, "対応づけの門の固定分 [m]")
	flag.Float64Var(&o.sigmaAzDeg, "sigma-az-deg", 0.35, "門に足す方位誤差の σ [deg]（3σ を足す）")
	flag.Float64Var(&o.maxGapS, "max-gap-s", 10, "真値の内挿を許す隣接行の間隔 [s]")
	flag.IntVar(&o.minNIC, "min-nic", 7, "誤差の集計に使う真値の NIC の下限")
	flag.IntVar(&o.listN, "list", 30, "相手のいない確定便を点数の多い順に何本並べるか")
	flag.Parse()
	if o.cfgPath == "" || o.ssrID == "" || o.fixes == "" || o.truth == "" {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(o); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

type options struct {
	cfgPath, ssrID, fixes, truth, out string
	gateM, sigmaAzDeg, maxGapS        float64
	minNIC, listN                     int
}

const timeLayout = "2006-01-02T15:04:05.999999999"

// --- 真値 ---

type sample struct {
	t      int64 // Unix ns
	e, n   float64
	altFt  int
	hasAlt bool
	squawk string
	nic    int
}

type aircraft struct {
	icao     string
	callsign string
	s        []sample // 時刻順
	first    int64
	last     int64
}

// at は時刻 t の位置を内挿する。隣接行の間隔が maxGap を超えていれば無し。
func (a *aircraft) at(t, maxGap int64) (sample, bool) {
	i := sort.Search(len(a.s), func(i int) bool { return a.s[i].t >= t })
	if i == len(a.s) {
		return sample{}, false
	}
	if i == 0 {
		if a.s[0].t-t > maxGap/10 { // 先頭より前は 1 s 程度まで
			return sample{}, false
		}
		return a.s[0], true
	}
	p, q := a.s[i-1], a.s[i]
	if q.t-p.t > maxGap {
		return sample{}, false
	}
	w := float64(t-p.t) / float64(q.t-p.t)
	near := p
	if w > 0.5 {
		near = q
	}
	out := sample{
		t: t, e: p.e + w*(q.e-p.e), n: p.n + w*(q.n-p.n),
		squawk: near.squawk, nic: min(p.nic, q.nic),
	}
	if p.hasAlt && q.hasAlt {
		out.altFt, out.hasAlt = int(math.Round(float64(p.altFt)+w*float64(q.altFt-p.altFt))), true
	} else if near.hasAlt {
		out.altFt, out.hasAlt = near.altFt, true
	}
	return out, true
}

func readTruth(path string, conv *geodesy.ENUConverter) ([]*aircraft, error) {
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
	col := indexer(header)
	need := []string{"time_jst", "icao", "squawk", "lat", "lon", "pressure_alt_ft"}
	for _, c := range need {
		if col(c) < 0 {
			return nil, fmt.Errorf("真値に列 %q が無い（VERIFY.md 参照）", c)
		}
	}
	cT, cI, cS, cLat, cLon, cAlt := col("time_jst"), col("icao"), col("squawk"), col("lat"), col("lon"), col("pressure_alt_ft")
	cNIC, cCS, cAir := col("nic"), col("callsign"), col("airborne")
	byICAO := map[string]*aircraft{}
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
		s := sample{t: t.UnixNano(), squawk: rec[cS], nic: 99}
		if v := rec[cAlt]; v != "" {
			alt, err := strconv.Atoi(v)
			if err != nil {
				return nil, fmt.Errorf("真値 %d 行目の pressure_alt_ft: %q", line, v)
			}
			s.altFt, s.hasAlt = alt, true
		}
		if cNIC >= 0 {
			if v, err := strconv.Atoi(rec[cNIC]); err == nil {
				s.nic = v
			}
		}
		// 高さは気圧高度を標高とみなす（pssrx と同じ扱い）。水平の ENU にしか使わない
		alt := 0.0
		if s.hasAlt {
			alt = float64(s.altFt) * 0.3048
		}
		enu, err := conv.LLAToENU(geodesy.OrthometricLLA{Lat: lat, Lon: lon, Alt: alt})
		if err != nil {
			return nil, fmt.Errorf("真値 %d 行目の位置: %w", line, err)
		}
		s.e, s.n = enu.E, enu.N
		icao := strings.ToLower(rec[cI])
		a := byICAO[icao]
		if a == nil {
			a = &aircraft{icao: icao}
			byICAO[icao] = a
		}
		if cCS >= 0 && a.callsign == "" {
			a.callsign = rec[cCS]
		}
		a.s = append(a.s, s)
	}
	var out []*aircraft
	for _, a := range byICAO {
		slices.SortStableFunc(a.s, func(x, y sample) int { return cmpInt64(x.t, y.t) })
		a.first, a.last = a.s[0].t, a.s[len(a.s)-1].t
		out = append(out, a)
	}
	slices.SortFunc(out, func(x, y *aircraft) int { return strings.Compare(x.icao, y.icao) })
	return out, nil
}

// --- pssrx の位置 ---

type fix struct {
	rec     []string // 元の行
	t       int64
	squawk  string
	altFt   int
	e, n    float64
	rho     float64 // SSR からの水平距離
	tauNs   int64
	azimuth float64
	replies int
	track   int64
	status  string
	sigmaH  float64 // 出力の σ_E, σ_N を合成した水平の標準偏差 [m]

	// 対応づけ
	cand   *aircraft // 最も近い候補
	truth  sample
	dist   float64
	partOK bool // 便の相手と一致
}

func readFixes(path string, conv *geodesy.ENUConverter) ([]string, []*fix, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	header, err := r.Read()
	if err != nil {
		return nil, nil, fmt.Errorf("位置のヘッダ: %w", err)
	}
	col := indexer(header)
	for _, c := range []string{"time_jst", "squawk", "pressure_alt_ft", "lat", "lon", "azimuth_rad", "tau_ns", "replies", "track", "status"} {
		if col(c) < 0 {
			return nil, nil, fmt.Errorf("位置の CSV に列 %q が無い（pssrx の出力か確認）", c)
		}
	}
	var out []*fix
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		t, err := time.ParseInLocation(timeLayout, rec[col("time_jst")], record.JST)
		if err != nil {
			return nil, nil, err
		}
		lat, _ := strconv.ParseFloat(rec[col("lat")], 64)
		lon, _ := strconv.ParseFloat(rec[col("lon")], 64)
		altM, _ := strconv.ParseFloat(rec[col("alt_m")], 64)
		enu, err := conv.LLAToENU(geodesy.OrthometricLLA{Lat: lat, Lon: lon, Alt: altM})
		if err != nil {
			return nil, nil, err
		}
		fx := &fix{rec: rec, t: t.UnixNano(), squawk: rec[col("squawk")], e: enu.E, n: enu.N, status: rec[col("status")]}
		fx.rho = math.Hypot(enu.E, enu.N)
		fx.altFt, _ = strconv.Atoi(rec[col("pressure_alt_ft")])
		fx.tauNs, _ = strconv.ParseInt(rec[col("tau_ns")], 10, 64)
		fx.azimuth, _ = strconv.ParseFloat(rec[col("azimuth_rad")], 64)
		fx.replies, _ = strconv.Atoi(rec[col("replies")])
		fx.track, _ = strconv.ParseInt(rec[col("track")], 10, 64)
		if ce, cn := col("sigma_e_m"), col("sigma_n_m"); ce >= 0 && cn >= 0 {
			se, _ := strconv.ParseFloat(rec[ce], 64)
			sn, _ := strconv.ParseFloat(rec[cn], 64)
			fx.sigmaH = math.Hypot(se, sn)
		}
		out = append(out, fx)
	}
	return header, out, nil
}

// --- 対応づけ ---

// trackInfo は pssrx の便 1 本とその相手。
type trackInfo struct {
	id      int64
	points  []*fix
	partner *aircraft
	cover   int     // 相手の真値が内挿できた点数
	purity  float64 // 相手に対応した点 / cover
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
	gm, err := geoid.Load()
	if err != nil {
		return err
	}
	conv, err := geodesy.NewENUConverter(ssr.LLA(), gm)
	if err != nil {
		return err
	}
	aroundNs := int64(ssr.Interrogation.AroundTimeSec * 1e9)

	truth, err := readTruth(o.truth, conv)
	if err != nil {
		return err
	}
	header, fixes, err := readFixes(o.fixes, conv)
	if err != nil {
		return err
	}
	fmt.Printf("真値 %d 機体、位置 %d 点\n", len(truth), len(fixes))

	maxGap := int64(o.maxGapS * 1e9)
	sigmaAz := o.sigmaAzDeg * math.Pi / 180
	// 点ごとの候補: スコーク一致（真値のスコークが空なら位置だけ）で門の内側、最も近い機体
	for _, fx := range fixes {
		gate := o.gateM + 3*fx.rho*sigmaAz
		for _, a := range truth {
			if fx.t < a.first-maxGap || fx.t > a.last+maxGap {
				continue
			}
			s, ok := a.at(fx.t, maxGap)
			if !ok || (s.squawk != "" && s.squawk != fx.squawk) {
				continue
			}
			d := math.Hypot(fx.e-s.e, fx.n-s.n)
			if d > gate {
				continue
			}
			if fx.cand == nil || d < fx.dist {
				fx.cand, fx.truth, fx.dist = a, s, d
			}
		}
	}
	// 便ごとの相手: 点の投票の過半
	tracks := map[int64]*trackInfo{}
	for _, fx := range fixes {
		ti := tracks[fx.track]
		if ti == nil {
			ti = &trackInfo{id: fx.track}
			tracks[fx.track] = ti
		}
		ti.points = append(ti.points, fx)
	}
	// 真値には受信の途切れがあるので、相手の判定は「その機体の真値が
	// 内挿できた点」を分母にする。真値の無い点は不明であって不一致ではない
	for _, ti := range tracks {
		votes := map[*aircraft]int{}
		for _, fx := range ti.points {
			if fx.cand != nil {
				votes[fx.cand]++
			}
		}
		var best *aircraft
		for a, n := range votes {
			if best == nil || n > votes[best] || (n == votes[best] && a.icao < best.icao) {
				best = a
			}
		}
		if best == nil {
			continue
		}
		cover := 0
		for _, fx := range ti.points {
			if _, ok := best.at(fx.t, maxGap); ok {
				cover++
			}
		}
		if votes[best] >= 3 && 2*votes[best] > cover {
			ti.partner = best
			ti.cover = cover
			ti.purity = float64(votes[best]) / float64(cover)
			for _, fx := range ti.points {
				fx.partOK = fx.cand == best
			}
		}
	}

	// --- 指標 ---
	fmt.Println()
	fmt.Println("== status 別の真値あり率（点に門の内側の機体がある割合）")
	byStatus := map[string][2]int{}
	for _, fx := range fixes {
		v := byStatus[fx.status]
		v[0]++
		if fx.cand != nil {
			v[1]++
		}
		byStatus[fx.status] = v
	}
	for _, st := range []string{"ok", "unconfirmed"} {
		v := byStatus[st]
		fmt.Printf("  %-12s %7d 点  真値あり %7d (%5.1f%%)\n", st, v[0], v[1], pct(v[1], v[0]))
	}

	fmt.Println()
	fmt.Println("== 応答数別の真値あり率")
	fmt.Println("  replies      ok: 点数  真値あり率   unconfirmed: 点数  真値あり率")
	type rc struct{ okN, okT, unN, unT int }
	byRep := map[int]*rc{}
	for _, fx := range fixes {
		k := repBucket(fx.replies)
		c := byRep[k]
		if c == nil {
			c = &rc{}
			byRep[k] = c
		}
		if fx.status == "ok" {
			c.okN++
			if fx.cand != nil {
				c.okT++
			}
		} else {
			c.unN++
			if fx.cand != nil {
				c.unT++
			}
		}
	}
	for _, k := range []int{3, 4, 5, 6, 7, 8, 10, 15} {
		c := byRep[k]
		if c == nil {
			continue
		}
		fmt.Printf("  %-8s %10d  %8.1f%%   %14d  %8.1f%%\n", repLabel(k), c.okN, pct(c.okT, c.okN), c.unN, pct(c.unT, c.unN))
	}

	fmt.Println()
	fmt.Println("== 確定した便（ok の点を持つ便）の相手と純度")
	var okTracks, withPartner, pure90 int
	var purities []float64
	for _, ti := range tracks {
		if ti.points[0].status != "ok" {
			continue
		}
		okTracks++
		if ti.partner != nil {
			withPartner++
			purities = append(purities, ti.purity)
			if ti.purity >= 0.9 {
				pure90++
			}
		}
	}
	slices.Sort(purities)
	fmt.Printf("  確定便 %d、相手あり %d (%.1f%%)、うち純度 90%% 以上 %d\n", okTracks, withPartner, pct(withPartner, okTracks), pure90)
	if len(purities) > 0 {
		fmt.Printf("  純度（相手の真値があった点のうち相手に対応した割合）中央値 %.3f p10 %.3f 最小 %.3f\n", purities[len(purities)/2], purities[len(purities)/10], purities[0])
	}

	// 完全性: 相手ありの便の期間で、機体が真値に居た走査数に対する点数
	fmt.Println()
	fmt.Println("== 完全性（相手のいる便の期間に、真値の機体が居た走査のうち点があった割合）")
	var expScans, gotPts float64
	for _, ti := range tracks {
		if ti.partner == nil || ti.points[0].status != "ok" {
			continue
		}
		t0, t1 := ti.points[0].t, ti.points[len(ti.points)-1].t
		for _, fx := range ti.points {
			t0, t1 = min(t0, fx.t), max(t1, fx.t)
		}
		// 期間内で真値が内挿できる時間
		present := 0.0
		for t := t0; t <= t1; t += aroundNs {
			if _, ok := ti.partner.at(t, maxGap); ok {
				present++
			}
		}
		expScans += present
		gotPts += float64(len(ti.points))
	}
	if expScans > 0 {
		fmt.Printf("  点 %.0f / 走査 %.0f = %.1f%%（便の内側のみ。便の切れ目・確定前の点は含まない）\n", gotPts, expScans, 100*gotPts/expScans)
	}

	// 位置誤差
	fmt.Println()
	fmt.Printf("== 位置誤差（ok かつ相手と一致、真値の NIC ≥ %d）。距離方向 / 方位方向 [m]\n", o.minNIC)
	fmt.Println("  距離帯[km]     n    距離: 中央値(符号付)  |p50|   |p90|   |p99|    方位: 中央値(符号付)  |p50|   |p90|   |p99|")
	type band struct{ rad, az, absRad, absAz []float64 }
	bands := map[int]*band{}
	var altDiff []float64
	for _, fx := range fixes {
		if fx.status != "ok" || !fx.partOK || fx.truth.nic < o.minNIC {
			continue
		}
		de, dn := fx.e-fx.truth.e, fx.n-fx.truth.n
		rho := math.Hypot(fx.truth.e, fx.truth.n)
		if rho == 0 {
			continue
		}
		ue, un := fx.truth.e/rho, fx.truth.n/rho
		rad := de*ue + dn*un // 動径方向（外向き正）
		az := -de*un + dn*ue // 方位方向（反時計回り正）
		k := min(int(rho/50000)*50, 300)
		b := bands[k]
		if b == nil {
			b = &band{}
			bands[k] = b
		}
		b.rad = append(b.rad, rad)
		b.az = append(b.az, az)
		b.absRad = append(b.absRad, math.Abs(rad))
		b.absAz = append(b.absAz, math.Abs(az))
		if fx.truth.hasAlt {
			altDiff = append(altDiff, float64(fx.altFt-fx.truth.altFt))
		}
	}
	keys := make([]int, 0, len(bands))
	for k := range bands {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		b := bands[k]
		slices.Sort(b.rad)
		slices.Sort(b.az)
		slices.Sort(b.absRad)
		slices.Sort(b.absAz)
		n := len(b.rad)
		fmt.Printf("  %4d-%-4d %7d    %+9.0f  %7.0f %7.0f %7.0f        %+9.0f  %7.0f %7.0f %7.0f\n",
			k, k+50, n, q(b.rad, 0.5), q(b.absRad, 0.5), q(b.absRad, 0.9), q(b.absRad, 0.99),
			q(b.az, 0.5), q(b.absAz, 0.5), q(b.absAz, 0.9), q(b.absAz, 0.99))
	}
	// 応答数別の方位誤差 [deg] と、σ に対する残差。σ の較正（応答数による
	// σ_θ）が実態に合っていれば、|誤差| / σ は応答数によらず同じ分布になる
	fmt.Println()
	fmt.Println("== 応答数別の方位誤差 [deg]（ok かつ相手と一致、NIC ≥ 下限）と σ に対する残差")
	fmt.Println("  replies     n   |Δaz| p50   p90     |誤差|/σ_h p50   p90   （σ_h = √(σ_E² + σ_N²)）")
	type repErr struct{ az, norm []float64 }
	byRepErr := map[int]*repErr{}
	for _, fx := range fixes {
		if fx.status != "ok" || !fx.partOK || fx.truth.nic < o.minNIC {
			continue
		}
		de, dn := fx.e-fx.truth.e, fx.n-fx.truth.n
		bearing := math.Atan2(fx.truth.e, fx.truth.n)
		daz := math.Abs(math.Mod(fx.azimuth-bearing+3*math.Pi, 2*math.Pi)-math.Pi) * 180 / math.Pi
		k := min(fx.replies, 20)
		r := byRepErr[k]
		if r == nil {
			r = &repErr{}
			byRepErr[k] = r
		}
		r.az = append(r.az, daz)
		if sh := fx.sigmaH; sh > 0 {
			r.norm = append(r.norm, math.Hypot(de, dn)/sh)
		}
	}
	for k := 3; k <= 20; k++ {
		r := byRepErr[k]
		if r == nil || len(r.az) < 20 {
			continue
		}
		slices.Sort(r.az)
		slices.Sort(r.norm)
		label := strconv.Itoa(k)
		if k == 20 {
			label = ">=20"
		}
		fmt.Printf("  %-8s %6d   %6.2f %6.2f        %6.2f %6.2f\n", label, len(r.az), q(r.az, 0.5), q(r.az, 0.9), q(r.norm, 0.5), q(r.norm, 0.9))
	}

	if len(altDiff) > 0 {
		slices.Sort(altDiff)
		abs := make([]float64, len(altDiff))
		for i, v := range altDiff {
			abs[i] = math.Abs(v)
		}
		slices.Sort(abs)
		fmt.Printf("  高度差 [ft] (pssrx − 真値): 中央値 %+.0f, |p90| %.0f, |p99| %.0f, 300 ft 超 %d / %d\n",
			q(altDiff, 0.5), q(abs, 0.9), q(abs, 0.99), countAbove(abs, 300), len(abs))
	}

	// 相手のいない確定便
	fmt.Println()
	fmt.Printf("== 相手のいない確定便（エコー・ADS-B 未装備機の候補）。点数の多い順に %d 本\n", o.listN)
	fmt.Println("  同じスコークで時間の重なる相手ありの便との差: Δτ = 自便 − 相手あり便 [µs]、Δ方位 [deg]")
	var orphans []*trackInfo
	for _, ti := range tracks {
		if ti.points[0].status == "ok" && ti.partner == nil {
			orphans = append(orphans, ti)
		}
	}
	slices.SortFunc(orphans, func(a, b *trackInfo) int { return cmpInt64(int64(len(b.points)), int64(len(a.points))) })
	fmt.Printf("  %-7s %-6s %-6s %-12s %-12s %5s %9s %6s  %s\n", "track", "squawk", "points", "first", "last", "alt", "tau_us", "az", "同スコークの相手あり便 (track: Δτ_us, Δaz_deg, 相手 icao)")
	for i, ti := range orphans {
		if i >= o.listN {
			break
		}
		f0, f1 := ti.points[0], ti.points[len(ti.points)-1]
		mt := meanTau(ti.points)
		var rel []string
		for _, tj := range tracks {
			if tj.partner == nil || tj.points[0].squawk != f0.squawk || tj.points[0].status != "ok" {
				continue
			}
			g0, g1 := tj.points[0], tj.points[len(tj.points)-1]
			if g1.t < f0.t || g0.t > f1.t {
				continue
			}
			daz := (meanAz(ti.points) - meanAz(tj.points)) * 180 / math.Pi
			daz = math.Mod(daz+540, 360) - 180
			rel = append(rel, fmt.Sprintf("%d: %+.0f, %+.0f, %s", tj.id, (mt-meanTau(tj.points))/1000, daz, tj.partner.icao))
		}
		fmt.Printf("  %-7d %-6s %-6d %-12s %-12s %5d %9.0f %6.1f  %s\n", ti.id, f0.squawk, len(ti.points),
			record.ToTime(f0.t).Format("15:04:05"), record.ToTime(f1.t).Format("15:04:05"), f0.altFt, mt/1000, meanAz(ti.points)*180/math.Pi, strings.Join(rel, "; "))
	}
	fmt.Printf("  相手のいない確定便 %d 本（点 %d）\n", len(orphans), sumPoints(orphans))

	if o.out != "" {
		return writeMatched(o.out, header, fixes)
	}
	return nil
}

func writeMatched(path string, header []string, fixes []*fix) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	w := csv.NewWriter(f)
	if err := w.Write(append(slices.Clone(header), "icao", "truth_e_m", "truth_n_m", "truth_alt_ft", "err_range_m", "err_azimuth_m", "matched")); err != nil {
		return err
	}
	for _, fx := range fixes {
		rec := slices.Clone(fx.rec)
		if fx.cand == nil {
			rec = append(rec, "", "", "", "", "", "", "0")
		} else {
			de, dn := fx.e-fx.truth.e, fx.n-fx.truth.n
			rho := math.Hypot(fx.truth.e, fx.truth.n)
			ue, un := fx.truth.e/rho, fx.truth.n/rho
			alt := ""
			if fx.truth.hasAlt {
				alt = strconv.Itoa(fx.truth.altFt)
			}
			matched := "0"
			if fx.partOK {
				matched = "1"
			}
			rec = append(rec, fx.cand.icao,
				strconv.FormatFloat(fx.truth.e, 'f', 1, 64), strconv.FormatFloat(fx.truth.n, 'f', 1, 64), alt,
				strconv.FormatFloat(de*ue+dn*un, 'f', 1, 64), strconv.FormatFloat(-de*un+dn*ue, 'f', 1, 64), matched)
		}
		if err := w.Write(rec); err != nil {
			return err
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return err
	}
	return f.Close()
}

// --- 補助 ---

func indexer(header []string) func(string) int {
	return func(name string) int { return slices.Index(header, name) }
}

func cmpInt64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func pct(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return 100 * float64(a) / float64(b)
}

func q(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return math.NaN()
	}
	return sorted[min(int(float64(len(sorted)-1)*p), len(sorted)-1)]
}

func countAbove(sorted []float64, v float64) int {
	return len(sorted) - sort.SearchFloat64s(sorted, v)
}

func repBucket(n int) int {
	switch {
	case n <= 8:
		return n
	case n <= 12:
		return 10
	}
	return 15
}

func repLabel(k int) string {
	switch k {
	case 10:
		return "9-12"
	case 15:
		return ">=13"
	}
	return strconv.Itoa(k)
}

func meanTau(pts []*fix) float64 {
	var s float64
	for _, p := range pts {
		s += float64(p.tauNs)
	}
	return s / float64(len(pts))
}

// meanAz は方位の平均（単位ベクトルの平均の向き）。
func meanAz(pts []*fix) float64 {
	var x, y float64
	for _, p := range pts {
		x += math.Cos(p.azimuth)
		y += math.Sin(p.azimuth)
	}
	return math.Atan2(y, x)
}

func sumPoints(ts []*trackInfo) int {
	n := 0
	for _, t := range ts {
		n += len(t.points)
	}
	return n
}
