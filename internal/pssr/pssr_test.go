package pssr_test

import (
	"log/slog"
	"math"
	"testing"

	"pssrx/internal/pattern"
	"pssrx/internal/pssr"
	"pssrx/internal/pssr/simtest"
	"pssrx/internal/store"
)

const (
	start    = int64(1_781_000_000_000_000_000)
	tauMin   = int64(7_253)     // 3 µs + 1275 m / c
	tauMax   = int64(2_676_000) // 3 µs + (800 km + 1275 m) / c。PRI 2.9499 ms より短い
	priNs    = int64(2_949_900)
	aroundNs = int64(4_040_000_000)
)

func schedule(t *testing.T, count int) []store.Intg {
	return scheduleFrom(t, count, 1.0)
}

func scheduleFrom(t *testing.T, count int, azimuth0 float64) []store.Intg {
	t.Helper()
	modes, err := pattern.ParseModes("AC")
	if err != nil {
		t.Fatal(err)
	}
	pat, err := pattern.FromStagger([]int64{priNs}, modes)
	if err != nil {
		t.Fatal(err)
	}
	return simtest.Schedule{
		Start: start, Count: count, Pattern: pat,
		AroundTimeNs: aroundNs, Azimuth0: azimuth0, Clockwise: true,
	}.Intg()
}

func newPairer(t *testing.T, cfg pssr.Config) *pssr.Pairer {
	t.Helper()
	p, err := pssr.New(pssr.Params{SSRID: "S", StationID: "T", TauMinNs: tauMin, TauMaxNs: tauMax}, cfg,
		slog.New(slog.NewTextHandler(discard{}, &slog.HandlerOptions{Level: slog.LevelError})))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

type discard struct{}

func (discard) Write(b []byte) (int, error) { return len(b), nil }

// feedAll は全部を 1 回で流し、last で閉じる。
func feedAll(t *testing.T, p *pssr.Pairer, replies []store.AData, intg []store.Intg) []pssr.Plot {
	t.Helper()
	plots, err := p.Feed(replies, intg, intg[len(intg)-1].Timestamp+1, true)
	if err != nil {
		t.Fatal(err)
	}
	return plots
}

// TestSingleDwell は 1 機体 1 ドウェルの応答列が 1 プロットになり、
// 要約値が定義どおり（最初と最後の中点、τ の平均）であることを確認する。
func TestSingleDwell(t *testing.T) {
	intg := schedule(t, 40)
	ac := simtest.Aircraft{TauNs: 1_500_000, ModeA: 0o5621, ModeC: []uint16{0o1432}, First: 10, Last: 19, WH: 45000}
	replies := simtest.Replies(intg, ac)

	p := newPairer(t, pssr.DefaultConfig())
	plots := feedAll(t, p, replies, intg)
	if len(plots) != 1 {
		t.Fatalf("プロット数 %d, 期待 1 (stats %+v)", len(plots), p.Stats())
	}
	pl := plots[0]
	if pl.SSRID != "S" || pl.StationID != "T" {
		t.Errorf("ID = (%s, %s)", pl.SSRID, pl.StationID)
	}
	if len(pl.Replies) != 10 {
		t.Errorf("応答数 %d, 期待 10", len(pl.Replies))
	}
	if pl.TauNs != 1_500_000 {
		t.Errorf("tau = %d, 期待 1500000", pl.TauNs)
	}
	if !pl.HasModeA || pl.ModeA != 0o5621 {
		t.Errorf("Mode A = %o (has=%v), 期待 5621", pl.ModeA, pl.HasModeA)
	}
	if len(pl.ModeC) != 5 || pl.ModeC[0] != 0o1432 {
		t.Errorf("Mode C = %o, 期待 1432 x5", pl.ModeC)
	}
	wantTs := intg[10].Timestamp + (intg[19].Timestamp-intg[10].Timestamp)/2
	if pl.Timestamp != wantTs {
		t.Errorf("時刻 %d, 期待 %d", pl.Timestamp, wantTs)
	}
	wantAz := (intg[10].Azimuth + intg[19].Azimuth) / 2
	if math.Abs(pl.Azimuth-wantAz) > 1e-12 {
		t.Errorf("方位 %g, 期待 %g", pl.Azimuth, wantAz)
	}
	s := p.Stats()
	if s.Replies != 10 || s.Paired != 10 || s.Plots != 1 || s.Runs != 1 {
		t.Errorf("stats = %+v", s)
	}
}

// TestTwoAircraftSameDwell は τ の違う 2 機が同じ質問に応答しても
// 別の列に分かれることを確認する。
func TestTwoAircraftSameDwell(t *testing.T) {
	intg := schedule(t, 40)
	a := simtest.Aircraft{TauNs: 1_500_000, ModeA: 0o5621, ModeC: []uint16{0o1432}, First: 10, Last: 19}
	b := simtest.Aircraft{TauNs: 1_800_000, ModeA: 0o5621, ModeC: []uint16{0o1432}, First: 12, Last: 22} // 符号は同じ
	replies := simtest.Replies(intg, a, b)

	plots := feedAll(t, newPairer(t, pssr.DefaultConfig()), replies, intg)
	if len(plots) != 2 {
		t.Fatalf("プロット数 %d, 期待 2", len(plots))
	}
	taus := map[int64]int{}
	for _, pl := range plots {
		taus[pl.TauNs] = len(pl.Replies)
	}
	if taus[1_500_000] != 10 || taus[1_800_000] != 11 {
		t.Errorf("τ ごとの応答数 = %v, 期待 {1500000:10, 1800000:11}", taus)
	}
}

// TestGapHandling は MaxGap 以内の途切れは同じ列、超えると別の列になることを固定する。
func TestGapHandling(t *testing.T) {
	intg := schedule(t, 60)
	cfg := pssr.DefaultConfig() // MaxGap = 2
	within := simtest.Aircraft{TauNs: 1_000_000, ModeA: 0o1200, ModeC: []uint16{0o0412}, First: 10, Last: 21, Skip: []int{14, 15}}
	plots := feedAll(t, newPairer(t, cfg), simtest.Replies(intg, within), intg)
	if len(plots) != 1 || len(plots[0].Replies) != 10 {
		t.Errorf("途切れ 2 で列が分かれた: %d プロット", len(plots))
	}
	beyond := simtest.Aircraft{TauNs: 1_000_000, ModeA: 0o1200, ModeC: []uint16{0o0412}, First: 10, Last: 24, Skip: []int{14, 15, 16}}
	p := newPairer(t, cfg)
	plots = feedAll(t, p, simtest.Replies(intg, beyond), intg)
	if len(plots) != 2 {
		t.Errorf("途切れ 3 で列が分かれない: %d プロット (stats %+v)", len(plots), p.Stats())
	}
}

// TestFruitIsDropped は質問予定と無関係な応答が列にならないことを確認する。
func TestFruitIsDropped(t *testing.T) {
	intg := schedule(t, 40)
	// 各質問の直後で TauMin 以上・互いに τ がばらばらの孤立応答
	var ts []int64
	for i := 5; i < 35; i += 3 {
		ts = append(ts, intg[i].Timestamp+100_000+int64(i)*50_000)
	}
	p := newPairer(t, pssr.DefaultConfig())
	plots := feedAll(t, p, simtest.Fruit(ts, 0o7777), intg)
	if len(plots) != 0 {
		t.Errorf("FRUIT がプロットになった: %+v", plots)
	}
	s := p.Stats()
	if s.Paired != len(ts) || s.RunsTooShort == 0 {
		t.Errorf("stats = %+v", s)
	}
}

// TestAboveMaxIsDropped は TauMax を超える遅延の応答が捨てられ、
// 遡れる質問の無い応答が別に数えられることを確認する。
func TestAboveMaxIsDropped(t *testing.T) {
	intg := schedule(t, 40)
	far := simtest.Aircraft{TauNs: tauMax + 1, ModeA: 0o1000, ModeC: []uint16{1}, First: 10, Last: 15}
	early := simtest.Fruit([]int64{intg[0].Timestamp + tauMin - 1}, 1) // 最初の質問より前へ遡る
	p := newPairer(t, pssr.DefaultConfig())
	plots := feedAll(t, p, append(simtest.Replies(intg, far), early...), intg)
	if len(plots) != 0 {
		t.Errorf("窓の外の応答がプロットになった")
	}
	s := p.Stats()
	if s.AboveMax != 6 || s.NoInterrogation != 1 || s.Paired != 0 {
		t.Errorf("stats = %+v", s)
	}
}

// TestModeCChangeWithinRun は列の途中で高度符号が変わっても列が切れず、
// 符号が出現順に残ることを確認する。
func TestModeCChangeWithinRun(t *testing.T) {
	intg := schedule(t, 40)
	climbing := simtest.Aircraft{TauNs: 1_000_000, ModeA: 0o1200, ModeC: []uint16{0o0412, 0o0412, 0o0413}, First: 10, Last: 19}
	plots := feedAll(t, newPairer(t, pssr.DefaultConfig()), simtest.Replies(intg, climbing), intg)
	if len(plots) != 1 {
		t.Fatalf("プロット数 %d, 期待 1", len(plots))
	}
	want := []uint16{0o0412, 0o0412, 0o0413, 0o0413, 0o0413}
	if got := plots[0].ModeC; len(got) != len(want) {
		t.Fatalf("Mode C = %o, 期待 %o", got, want)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("Mode C = %o, 期待 %o", got, want)
				break
			}
		}
	}
}

// TestModeAMismatchSplits は τ が同じでもスコークが違えば同じ列に入らないことを確認する。
func TestModeAMismatchSplits(t *testing.T) {
	intg := schedule(t, 40)
	a := simtest.Aircraft{TauNs: 1_000_000, ModeA: 0o1200, ModeC: []uint16{0o0412}, First: 10, Last: 15}
	b := simtest.Aircraft{TauNs: 1_000_000, ModeA: 0o1201, ModeC: []uint16{0o0412}, First: 16, Last: 21}
	plots := feedAll(t, newPairer(t, pssr.DefaultConfig()), simtest.Replies(intg, a, b), intg)
	if len(plots) != 2 {
		t.Errorf("プロット数 %d, 期待 2", len(plots))
	}
}

// TestAzimuthWrap はドウェルが方位 0 をまたぐときの中点を確認する。
func TestAzimuthWrap(t *testing.T) {
	// 40 質問で約 0.18 rad 回る。2pi の 0.05 rad 手前から始めて 0 をまたがせる
	intg := scheduleFrom(t, 40, 2*math.Pi-0.05)
	k := -1
	for i := 1; i < len(intg); i++ {
		if intg[i].Azimuth < intg[i-1].Azimuth {
			k = i
			break
		}
	}
	if k < 5 || k+5 >= len(intg) {
		t.Fatalf("0 をまたぐ質問が範囲内に無い: k=%d", k)
	}
	ac := simtest.Aircraft{TauNs: 1_000_000, ModeA: 0o1200, ModeC: []uint16{1}, First: k - 4, Last: k + 3}
	plots := feedAll(t, newPairer(t, pssr.DefaultConfig()), simtest.Replies(intg, ac), intg)
	if len(plots) != 1 {
		t.Fatalf("プロット数 %d", len(plots))
	}
	az := plots[0].Azimuth
	if !(az < 0.01 || az > 2*math.Pi-0.01) {
		t.Errorf("0 をまたぐ中点が %g rad になった", az)
	}
}

// TestBlockwiseFeedMatchesSingleFeed はブロックに分けて流しても、
// 質問予定が遅れて確定しても、1 回で流したのと同じプロットになることを
// 確認する。ドウェルがブロック境界にかかるケースを含む。
func TestBlockwiseFeedMatchesSingleFeed(t *testing.T) {
	intg := schedule(t, 200)
	acs := []simtest.Aircraft{
		{TauNs: 1_500_000, ModeA: 0o5621, ModeC: []uint16{0o1432}, First: 10, Last: 19},
		{TauNs: 1_800_000, ModeA: 0o2345, ModeC: []uint16{0o0412}, First: 95, Last: 105}, // 境界にかかる
		{TauNs: 900_000, ModeA: 0o4341, ModeC: []uint16{0o1656}, First: 150, Last: 160},
	}
	replies := simtest.Replies(intg, acs...)
	want := feedAll(t, newPairer(t, pssr.DefaultConfig()), replies, intg)
	if len(want) != 3 {
		t.Fatalf("基準のプロット数 %d", len(want))
	}

	// 応答は 100 質問ぶんずつ、質問予定は 1 ブロック遅れて渡す
	cut := intg[100].Timestamp
	p := newPairer(t, pssr.DefaultConfig())
	var got []pssr.Plot
	split := func(rs []store.AData, from, to int64) []store.AData {
		var out []store.AData
		for _, r := range rs {
			if r.Timestamp >= from && r.Timestamp < to {
				out = append(out, r)
			}
		}
		return out
	}
	end := intg[len(intg)-1].Timestamp + 1
	feeds := []struct {
		replies []store.AData
		intg    []store.Intg
		upTo    int64
		last    bool
	}{
		{split(replies, 0, cut), nil, 0, false},           // 質問予定はまだ無い
		{split(replies, cut, end), intg[:100], cut, false}, // 前のブロックの分が確定
		{nil, intg[100:], end, true},
	}
	for i, f := range feeds {
		pl, err := p.Feed(f.replies, f.intg, f.upTo, f.last)
		if err != nil {
			t.Fatalf("feed %d: %v", i, err)
		}
		got = append(got, pl...)
	}
	if len(got) != len(want) {
		t.Fatalf("プロット数 %d, 期待 %d (stats %+v)", len(got), len(want), p.Stats())
	}
	for i := range want {
		w, g := want[i], got[i]
		if w.Timestamp != g.Timestamp || w.Azimuth != g.Azimuth || w.TauNs != g.TauNs ||
			w.ModeA != g.ModeA || len(w.Replies) != len(g.Replies) {
			t.Errorf("[%d] 一致しない:\n  1 回: %+v\n  分割: %+v", i, summary(w), summary(g))
		}
	}
}

func summary(p pssr.Plot) pssr.Plot {
	p.Replies = nil
	return p
}

// TestIntgRegressionIsError は質問予定の逆行をエラーにすることを確認する。
func TestIntgRegressionIsError(t *testing.T) {
	intg := schedule(t, 10)
	p := newPairer(t, pssr.DefaultConfig())
	if _, err := p.Feed(nil, intg[5:], 0, false); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Feed(nil, intg[:5], 0, false); err == nil {
		t.Error("逆行した質問予定がエラーにならない")
	}
}
