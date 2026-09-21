package pssr_test

import (
	"math"
	"reflect"
	"testing"

	"pssrx/internal/pssr"
)

// flightFix は Resolve のテスト用の点。フライト ID・便 ID・τ・方位を付ける。
func flightFix(scan float64, flight int64, squawk uint16, altFt int, tauNs int64, azimuth float64) pssr.Fix {
	f := fixAt(scan, squawk, altFt, 0, 0)
	f.Track, f.Flight, f.Status = flight, flight, pssr.FixOK
	f.TauNs, f.Azimuth = tauNs, azimuth
	// 位置は方位と τ から適当に。Resolve は位置を見ない
	rho := float64(tauNs-3000) * 0.15
	f.Position.ENU.E, f.Position.ENU.N = rho*math.Sin(azimuth), rho*math.Cos(azimuth)
	return f
}

func resolveAll(t *testing.T, cfg pssr.Config, fixes []pssr.Fix, chunk int) ([]pssr.Fix, pssr.Stats) {
	t.Helper()
	if err := pssr.Validate(testParams, cfg); err != nil {
		t.Fatal(err)
	}
	var st pssr.ResolveState
	stats := pssr.NewStats(testParams)
	if chunk <= 0 {
		chunk = len(fixes)
	}
	var out []pssr.Fix
	for i := 0; i < len(fixes); i += chunk {
		end := min(i+chunk, len(fixes))
		out = append(out, pssr.Resolve(&st, &stats, testParams, cfg, fixes[i:end], end == len(fixes))...)
	}
	return out, stats
}

func statuses(out []pssr.Fix) map[int64][]pssr.FixStatus {
	m := map[int64][]pssr.FixStatus{}
	for _, f := range out {
		m[f.Flight] = append(m[f.Flight], f.Status)
	}
	return m
}

// directAndEcho は直接波（走査 0〜19、方位が動く）と像（走査 from〜to、
// 同じ τ・高度、方位は反射体の方向 2.0 rad に固定）を時刻順に作る。
func directAndEcho(from, to int) []pssr.Fix {
	var fixes []pssr.Fix
	for k := 0; k < 20; k++ {
		tau := int64(400_000 + 1_000*k)
		fixes = append(fixes, flightFix(float64(k), 1, 0o4321, 20000, tau, 0.5+0.02*float64(k)))
		if k >= from && k <= to {
			fixes = append(fixes, flightFix(float64(k)+0.3, 2, 0o4321, 20000, tau+300, 2.0))
		}
	}
	return fixes
}

// TestResolveMarksContainedFlightAsEcho は直接波の存在区間に含まれる像の
// フライトが echo になり、直接波は ok のままであることを確認する。
func TestResolveMarksContainedFlightAsEcho(t *testing.T) {
	out, s := resolveAll(t, pssr.DefaultConfig(), directAndEcho(5, 12), 0)
	st := statuses(out)
	for _, v := range st[1] {
		if v != pssr.FixOK {
			t.Fatalf("直接波が ok でない: %v", st[1])
		}
	}
	echo := 0
	for _, v := range st[2] {
		if v == pssr.FixEcho {
			echo++
		}
	}
	if echo != len(st[2]) {
		t.Errorf("像の点 %d / %d が echo", echo, len(st[2]))
	}
	if s.ResolveConfirmed != 1 || s.ResolvedByContinuity != 1 || s.ResolveAmbiguous != 0 || s.FixesEcho != echo {
		t.Errorf("stats = %+v", s)
	}
	if len(out) != 28 {
		t.Errorf("出力 %d 点, 期待 28（全点出す）", len(out))
	}
}

// TestResolveCrossingIntervalsAmbiguous は先に始まった方が先に終わる
// （存在区間が食い違う）2 本はどちらも選ばず、一致した走査の区間だけが
// ambiguous になり、その外の点は ok のままであることを確認する。
func TestResolveCrossingIntervalsAmbiguous(t *testing.T) {
	var fixes []pssr.Fix
	for k := 0; k < 20; k++ {
		tau := int64(400_000 + 1_000*k)
		if k <= 8 {
			fixes = append(fixes, flightFix(float64(k), 2, 0o4321, 20000, tau+300, 2.0)) // 先に始まり先に終わる
		}
		if k >= 2 {
			fixes = append(fixes, flightFix(float64(k)+0.3, 1, 0o4321, 20000, tau, 0.5+0.02*float64(k)))
		}
	}
	out, s := resolveAll(t, pssr.DefaultConfig(), fixes, 0)
	if s.ResolveAmbiguous != 1 || s.ResolvedByContinuity != 0 || s.FixesEcho != 0 {
		t.Fatalf("stats = %+v", s)
	}
	base, scan := fixes[0].Timestamp, testParams.AroundTimeNs
	for _, f := range out {
		// 一致した走査は 2〜8（半走査の余裕つき）
		inside := f.Timestamp >= base+2*scan-scan/2 && f.Timestamp <= base+8*scan+scan/2+scan/2
		want := pssr.FixOK
		if inside {
			want = pssr.FixAmbiguous
		}
		if f.Status != want {
			t.Errorf("flight %d t=%d: %v, 期待 %v", f.Flight, f.Timestamp, f.Status, want)
		}
	}
}

// TestResolveAmbiguousWhenIndistinguishable は同時に始まり同時に終わる 2 本は
// どちらも選ばず、重なりの点が ambiguous になることを確認する。
func TestResolveAmbiguousWhenIndistinguishable(t *testing.T) {
	var fixes []pssr.Fix
	for k := 0; k < 8; k++ {
		tau := int64(400_000 + 1_000*k)
		fixes = append(fixes,
			flightFix(float64(k), 1, 0o4321, 20000, tau, 0.5),
			flightFix(float64(k)+0.3, 2, 0o4321, 20000, tau+300, 2.0),
		)
	}
	out, s := resolveAll(t, pssr.DefaultConfig(), fixes, 0)
	if s.ResolveAmbiguous != 1 || s.ResolvedByContinuity != 0 {
		t.Fatalf("stats = %+v", s)
	}
	amb := 0
	for _, f := range out {
		if f.Status == pssr.FixAmbiguous {
			amb++
		}
		if f.Status == pssr.FixEcho {
			t.Error("選んでいる")
		}
	}
	if amb == 0 || s.FixesAmbiguous != amb {
		t.Errorf("ambiguous %d, stats %+v", amb, s)
	}
}

// TestResolveLeavesUnrelatedFlightsAlone は反射の無い状況（別の機体、同じ
// 機体の断片、τ の違う同スコーク）で何も変わらないことを確認する。
func TestResolveLeavesUnrelatedFlightsAlone(t *testing.T) {
	var fixes []pssr.Fix
	for k := 0; k < 12; k++ {
		fixes = append(fixes,
			flightFix(float64(k), 1, 0o4321, 20000, int64(400_000+1_000*k), 0.5),
			flightFix(float64(k)+0.2, 2, 0o4321, 20000, int64(400_000+1_000*k)+300, 0.55), // 方位差 3°: 断片
			flightFix(float64(k)+0.4, 3, 0o4321, 20000, int64(700_000+1_000*k), 2.0),      // τ が違う: 別の機体
			flightFix(float64(k)+0.6, 4, 0o1200, 8000, int64(400_000+1_000*k), 2.0),       // 別のスコーク
		)
	}
	out, s := resolveAll(t, pssr.DefaultConfig(), fixes, 0)
	if s.ResolvePairs != 0 || s.FixesEcho != 0 || s.FixesAmbiguous != 0 {
		t.Errorf("何かした: %+v", s)
	}
	for _, f := range out {
		if f.Status != pssr.FixOK {
			t.Errorf("status が変わった: %+v", f.Status)
		}
	}
	if len(out) != 48 {
		t.Errorf("出力 %d, 期待 48", len(out))
	}
}

// TestResolveIsChunkInvariantAndOrdered は投入の刻みによらず同じ結果で、
// 出力が時刻順であることを確認する。
func TestResolveIsChunkInvariantAndOrdered(t *testing.T) {
	fixes := directAndEcho(5, 12)
	whole, ws := resolveAll(t, pssr.DefaultConfig(), fixes, 0)
	for i := 1; i < len(whole); i++ {
		if whole[i].Timestamp < whole[i-1].Timestamp {
			t.Fatal("出力が時刻順でない")
		}
	}
	key := func(out []pssr.Fix) []verdict {
		v := make([]verdict, len(out))
		for i, f := range out {
			v[i] = verdict{f.Flight, f.Status}
		}
		return v
	}
	for _, chunk := range []int{1, 3, 7} {
		got, gs := resolveAll(t, pssr.DefaultConfig(), fixes, chunk)
		if !reflect.DeepEqual(key(got), key(whole)) {
			t.Errorf("chunk=%d: %v\n%v", chunk, key(got), key(whole))
		}
		gs.ResolveHeldMax, ws.ResolveHeldMax = 0, 0
		if !reflect.DeepEqual(gs, ws) {
			t.Errorf("chunk=%d: stats %+v / %+v", chunk, gs, ws)
		}
	}
}
