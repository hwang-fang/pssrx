package pssr_test

import (
	"reflect"
	"testing"

	"pssrx/internal/pssr"
)

// linkAll は Track の出力に相当する点列（便 ID と判定つき）を Link に流す。
func linkAll(t *testing.T, cfg pssr.Config, fixes []pssr.Fix, chunk int) ([]pssr.Fix, pssr.Stats) {
	t.Helper()
	if err := pssr.Validate(testParams, cfg); err != nil {
		t.Fatal(err)
	}
	var st pssr.LinkState
	stats := pssr.NewStats(testParams)
	if chunk <= 0 {
		chunk = len(fixes)
	}
	var out []pssr.Fix
	for i := 0; i < len(fixes); i += chunk {
		end := min(i+chunk, len(fixes))
		out = append(out, pssr.Link(&st, &stats, testParams, cfg, fixes[i:end], end == len(fixes))...)
	}
	return out, stats
}

// tracked は fixAt に便 ID と判定を付ける。
func tracked(f pssr.Fix, track int64, status pssr.FixStatus) pssr.Fix {
	f.Track, f.Status = track, status
	return f
}

// flights は出力のフライト ID の列。
func flights(out []pssr.Fix) []int64 {
	v := make([]int64, len(out))
	for i, f := range out {
		v[i] = f.Flight
	}
	return v
}

// TestLinkJoinsFragmentsAcrossGap は Track の打ち切り幅を超えて切れた断片が、
// 末尾からの外挿に乗れば同じフライトになることを確認する。
func TestLinkJoinsFragmentsAcrossGap(t *testing.T) {
	// 便 1: 走査 0〜4、北へ 800 m/走査（198 m/s）。便 2: 走査 9〜11、同じ速度で続く
	var fixes []pssr.Fix
	for k := 0; k <= 4; k++ {
		fixes = append(fixes, tracked(fixAt(float64(k), 0o1234, 10000, 0, 800*float64(k)), 1, pssr.FixOK))
	}
	for k := 9; k <= 11; k++ {
		fixes = append(fixes, tracked(fixAt(float64(k), 0o1234, 10000, 0, 800*float64(k)), 2, pssr.FixOK))
	}
	out, s := linkAll(t, pssr.DefaultConfig(), fixes, 0)
	want := []int64{1, 1, 1, 1, 1, 1, 1, 1}
	if got := flights(out); !reflect.DeepEqual(got, want) {
		t.Errorf("flight = %v, 期待 %v", got, want)
	}
	if s.Flights != 1 || s.Links != 1 {
		t.Errorf("stats = %+v", s)
	}
}

// TestLinkRescuesShortFragment は 3 点未満で unconfirmed になった断片が、
// 既存のフライトに連結できれば ok になることを確認する。
func TestLinkRescuesShortFragment(t *testing.T) {
	var fixes []pssr.Fix
	for k := 0; k <= 4; k++ {
		fixes = append(fixes, tracked(fixAt(float64(k), 0o1234, 10000, 0, 800*float64(k)), 1, pssr.FixOK))
	}
	fixes = append(fixes,
		tracked(fixAt(9, 0o1234, 10000, 0, 7200), 2, pssr.FixUnconfirmed),
		tracked(fixAt(10, 0o1234, 10000, 0, 8000), 2, pssr.FixUnconfirmed),
	)
	out, s := linkAll(t, pssr.DefaultConfig(), fixes, 0)
	if out[5].Status != pssr.FixOK || out[6].Status != pssr.FixOK || out[5].Flight != 1 {
		t.Errorf("救済されていない: %+v %+v", out[5].Status, out[6].Status)
	}
	if s.LinkedRescued != 2 {
		t.Errorf("LinkedRescued = %d, 期待 2", s.LinkedRescued)
	}
}

// TestLinkRefuses は繋がない条件（Track の打ち切り幅より短い切れ目、外挿から
// 外れる、高度が合わない、別のスコーク、点が 1 つの末尾、切れ目が長すぎる）を
// 確認する。
func TestLinkRefuses(t *testing.T) {
	cfg := pssr.DefaultConfig()
	head := func() []pssr.Fix {
		var v []pssr.Fix
		for k := 0; k <= 4; k++ {
			v = append(v, tracked(fixAt(float64(k), 0o1234, 10000, 0, 800*float64(k)), 1, pssr.FixOK))
		}
		return v
	}
	cases := map[string]pssr.Fix{
		"切れ目が打ち切り幅より短い（Track が繋がなかったもの）": fixAt(6, 0o1234, 10000, 0, 4800),
		"外挿から外れる":  fixAt(9, 0o1234, 10000, 6000, 7200),
		"高度が合わない":  fixAt(9, 0o1234, 13000, 0, 7200),
		"別のスコーク":   fixAt(9, 0o1235, 10000, 0, 7200),
		"切れ目が長すぎる": fixAt(20, 0o1234, 10000, 0, 16000),
	}
	for name, f := range cases {
		out, _ := linkAll(t, cfg, append(head(), tracked(f, 2, pssr.FixOK)), 0)
		if out[5].Flight == out[0].Flight {
			t.Errorf("%s: 繋がった", name)
		}
	}
	// 末尾が 1 点では外挿できない
	one := []pssr.Fix{tracked(fixAt(0, 0o1234, 10000, 0, 0), 1, pssr.FixOK), tracked(fixAt(9, 0o1234, 10000, 0, 7200), 2, pssr.FixOK)}
	if out, _ := linkAll(t, cfg, one, 0); out[1].Flight == out[0].Flight {
		t.Error("1 点の末尾から繋がった")
	}
}

// TestLinkIsChunkInvariant は投入の刻みによらず同じ結果になり、フライトを
// 忘れた後の便が新しいフライトになることを確認する。
func TestLinkIsChunkInvariant(t *testing.T) {
	var fixes []pssr.Fix
	for k := 0; k <= 4; k++ {
		fixes = append(fixes, tracked(fixAt(float64(k), 0o1234, 10000, 0, 800*float64(k)), 1, pssr.FixOK))
	}
	for k := 9; k <= 11; k++ {
		fixes = append(fixes, tracked(fixAt(float64(k), 0o1234, 10000, 0, 800*float64(k)), 2, pssr.FixOK))
	}
	for k := 40; k <= 43; k++ { // 60 s 以上空く → 新しいフライト
		fixes = append(fixes, tracked(fixAt(float64(k), 0o1234, 10000, 0, 800*float64(k)), 3, pssr.FixOK))
	}
	whole, ws := linkAll(t, pssr.DefaultConfig(), fixes, 0)
	if whole[8].Flight == whole[0].Flight {
		t.Error("忘れたフライトに繋がった")
	}
	for _, chunk := range []int{1, 2, 5} {
		got, gs := linkAll(t, pssr.DefaultConfig(), fixes, chunk)
		if !reflect.DeepEqual(flights(got), flights(whole)) {
			t.Errorf("chunk=%d: %v / %v", chunk, flights(got), flights(whole))
		}
		gs.FlightsOpenMax, ws.FlightsOpenMax = 0, 0
		if !reflect.DeepEqual(gs, ws) {
			t.Errorf("chunk=%d: stats %+v / %+v", chunk, gs, ws)
		}
	}
}
