package tracking_test

import (
	"reflect"
	"testing"

	"pssrx/internal/geodesy"
	"pssrx/internal/pssr/tracking"
)

// fixAt は連続性のテスト用の位置。scan 走査目（0 始まり）、SSR の ENU で
// (e, n) [m]、σ は水平 50 m。
func fixAt(scan float64, squawk uint16, altFt int, e, n float64) tracking.Fix {
	return tracking.Fix{
		Timestamp: start + int64(scan*float64(aroundNs)), Squawk: squawk, AltitudeFt: altFt, Replies: 8,
		Position: tracking.Position{
			ENU: geodesy.ENU{E: e, N: n},
			Cov: [3][3]float64{{50 * 50 / 2, 0, 0}, {0, 50 * 50 / 2, 0}, {0, 0, 8.8 * 8.8}},
		},
	}
}

// tracker は Track の状態と統計をまとめたテスト用の入れ物。
type tracker struct {
	st    tracking.TrackState
	stats tracking.Stats
	cfg   tracking.Config
}

func newTracker(t *testing.T, cfg tracking.Config) *tracker {
	t.Helper()
	if err := tracking.Validate(testParams, cfg); err != nil {
		t.Fatal(err)
	}
	return &tracker{stats: tracking.Stats{}, cfg: cfg}
}

func (tr *tracker) feed(fixes []tracking.Fix, last bool) []tracking.Fix {
	return tracking.Track(&tr.st, &tr.stats, testParams, tr.cfg, fixes, last)
}

// trackAll は全部を 1 回で流して閉じる。
func trackAll(t *testing.T, cfg tracking.Config, fixes []tracking.Fix) ([]tracking.Fix, tracking.Stats) {
	t.Helper()
	tr := newTracker(t, cfg)
	out := tr.feed(fixes, true)
	return out, tr.stats
}

type verdict struct {
	track int64
	seq   int
}

func verdicts(out []tracking.Fix) []verdict {
	v := make([]verdict, len(out))
	for i, f := range out {
		v[i] = verdict{f.Track, f.TrackSeq}
	}
	return v
}

// TestTrackNumbersPoints は航跡片の点に ID と航跡片の中での順番が付き、
// 判定は付かない（2 点で消えた航跡片の点もそのまま出る）ことを確認する。
func TestTrackNumbersPoints(t *testing.T) {
	// 航跡片 A: 3 走査、毎走査 800 m 北へ（200 m/s）。航跡片 B: 2 走査で消える
	fixes := []tracking.Fix{
		fixAt(0, 0o1234, 10000, 0, 0),
		fixAt(0.3, 0o7777, 30000, 50_000, 0),
		fixAt(1, 0o1234, 10000, 0, 800),
		fixAt(1.3, 0o7777, 30000, 50_600, 0),
		fixAt(2, 0o1234, 10100, 0, 1600),
	}
	out, s := trackAll(t, tracking.DefaultConfig(), fixes)
	want := []verdict{{1, 1}, {2, 1}, {1, 2}, {2, 2}, {1, 3}}
	if got := verdicts(out); !reflect.DeepEqual(got, want) {
		t.Errorf("航跡片 %v, 期待 %v", got, want)
	}
	if s.Tracks != 2 || s.Tracks3 != 1 {
		t.Errorf("stats = %+v", s)
	}
}

// TestTrackAllowsMissedScans は欠測 2 走査は繋がり、3 走査空くと別の航跡片に
// なることを確認する。
func TestTrackAllowsMissedScans(t *testing.T) {
	fixes := []tracking.Fix{
		fixAt(0, 0o1234, 10000, 0, 0),
		fixAt(3, 0o1234, 10000, 0, 2400), // 欠測 2（Δt = 3 走査）
		fixAt(4, 0o1234, 10000, 0, 3200),
		fixAt(8, 0o1234, 10000, 0, 6400), // 欠測 3（Δt = 4 走査）→ 別の航跡片
		fixAt(9, 0o1234, 10000, 0, 7200),
		fixAt(10, 0o1234, 10000, 0, 8000),
	}
	out, _ := trackAll(t, tracking.DefaultConfig(), fixes)
	want := []verdict{{1, 1}, {1, 2}, {1, 3}, {2, 1}, {2, 2}, {2, 3}}
	if got := verdicts(out); !reflect.DeepEqual(got, want) {
		t.Errorf("航跡片 %v, 期待 %v", got, want)
	}
}

// TestTrackGates は門の各条件（スコーク・距離・高度）を確認する。
func TestTrackGates(t *testing.T) {
	cfg := tracking.DefaultConfig()
	base := fixAt(0, 0o1234, 10000, 0, 0)
	cases := map[string]tracking.Fix{
		"別のスコーク":   fixAt(1, 0o1235, 10000, 0, 800),
		"速すぎる":     fixAt(1, 0o1234, 10000, 0, 350*4.04+3*100+10),
		"高度が飛ぶ":    fixAt(1, 0o1234, 10000+int(100*4.04)+200, 0, 800),
		"同じ走査の2点目": fixAt(0.3, 0o1234, 10000, 0, 200),
	}
	for name, f := range cases {
		out, _ := trackAll(t, cfg, []tracking.Fix{base, f})
		if out[0].Track == out[1].Track {
			t.Errorf("%s: 同じ航跡片に繋がった", name)
		}
	}
	// 門の内側は繋がる
	ok := fixAt(1, 0o1234, 10000+int(100*4.04), 0, 350*4.04+3*100-10)
	out, _ := trackAll(t, cfg, []tracking.Fix{base, ok})
	if out[0].Track != out[1].Track {
		t.Error("門の内側の点が繋がらない")
	}
}

// TestTrackKeepsEchoSeparate は同じスコーク・同じ高度で離れた位置に連続する
// 2 本（エコーの模擬）が別々の航跡片になることを確認する。
func TestTrackKeepsEchoSeparate(t *testing.T) {
	var fixes []tracking.Fix
	for k := range 4 {
		s := float64(k)
		fixes = append(fixes,
			fixAt(s, 0o4321, 20000, 0, 800*s),          // 実機
			fixAt(s+0.1, 0o4321, 20000, 30_000, 800*s), // 30 km 離れた反射像
		)
	}
	out, s := trackAll(t, tracking.DefaultConfig(), fixes)
	if s.Tracks != 2 || s.Tracks3 != 2 {
		t.Fatalf("航跡片 %d（3 点以上 %d）, 期待 2 / 2", s.Tracks, s.Tracks3)
	}
	for i, f := range out {
		if f.Track != int64(1+i%2) || f.TrackSeq != 1+i/2 {
			t.Errorf("[%d] track=%d seq=%d", i, f.Track, f.TrackSeq)
		}
	}
}

// TestTrackNearestWinsSameScan は同じ走査に 2 つの候補があるとき、航跡片が近い方を
// 取り、遠い方は新しい航跡片になることを確認する。到着順に依らない。
func TestTrackNearestWinsSameScan(t *testing.T) {
	cfg := tracking.DefaultConfig()
	a := fixAt(0, 0o1234, 10000, 0, 0)
	far := fixAt(1, 0o1234, 10000, 0, 1200)
	near := fixAt(1.2, 0o1234, 10000, 0, 800) // 遠い方より後に来る
	out, _ := trackAll(t, cfg, []tracking.Fix{a, far, near})
	if out[2].Track != out[0].Track || out[1].Track == out[0].Track {
		t.Errorf("近い方が航跡片を取っていない: %v", verdicts(out))
	}
}

// TestTrackOutputIsTimeOrdered は出力が時刻順で、分割投入でも結果が同じ
// ことを確認する。
func TestTrackOutputIsTimeOrdered(t *testing.T) {
	var fixes []tracking.Fix
	for k := range 12 {
		s := float64(k)
		fixes = append(fixes, fixAt(s, 0o1234, 10000, 0, 800*s))
		if k%4 == 1 {
			fixes = append(fixes, fixAt(s+0.2, 0o5555, 3000, 100_000, 0)) // 孤立点
		}
		if k >= 6 {
			fixes = append(fixes, fixAt(s+0.5, 0o2222, 25000, -20_000, 500*s))
		}
	}
	whole, ws := trackAll(t, tracking.DefaultConfig(), fixes)
	for i := 1; i < len(whole); i++ {
		if whole[i].Timestamp < whole[i-1].Timestamp {
			t.Fatalf("出力が時刻順でない: [%d] %d < [%d] %d", i, whole[i].Timestamp, i-1, whole[i-1].Timestamp)
		}
	}
	for _, chunk := range []int{1, 2, 5} {
		tr := newTracker(t, tracking.DefaultConfig())
		var split []tracking.Fix
		for i := 0; i < len(fixes); i += chunk {
			end := min(i+chunk, len(fixes))
			split = append(split, tr.feed(fixes[i:end], end == len(fixes))...)
		}
		if !reflect.DeepEqual(verdicts(split), verdicts(whole)) {
			t.Errorf("chunk=%d: 航跡片が一括と違う\n  %v\n  %v", chunk, verdicts(split), verdicts(whole))
		}
		// 保留の最大件数は投入の刻みで変わる監視値なので比べない
		a, b := tr.stats, ws
		a.TrackHeldMax, b.TrackHeldMax = 0, 0
		if !reflect.DeepEqual(a, b) {
			t.Errorf("chunk=%d: stats が一括と違う\n  %+v\n  %+v", chunk, a, b)
		}
	}
}

// TestTrackEmitsAfterHalfScan は、点が半走査の後に（確定を待たずに）出て、
// 保留が増え続けないことを確認する。孤立点も同じ遅れで出る。
func TestTrackEmitsAfterHalfScan(t *testing.T) {
	tr := newTracker(t, tracking.DefaultConfig())
	total := 0
	for k := range 60 {
		s := float64(k)
		batch := []tracking.Fix{fixAt(s, 0o1234, 10000, 0, 800*s)}
		if k%3 == 0 {
			batch = append(batch, fixAt(s+0.3, uint16(0o100+k), 5000, 50_000, 0)) // 毎回別スコークの孤立点
		}
		total += len(batch)
		got := tr.feed(batch, false)
		// 走査 k の点を入れた時点で、走査 k−1 以前の点は全部出ている
		for _, f := range got {
			if f.Timestamp > start+int64((s-0.5)*float64(aroundNs)) {
				t.Fatalf("走査 %d で半走査より新しい点が出た: %d", k, f.Timestamp)
			}
		}
		if tr.stats.TrackHeldMax > 3 {
			t.Fatalf("走査 %d で保留が %d 件", k, tr.stats.TrackHeldMax)
		}
		total -= len(got)
	}
	rest := tr.feed(nil, true)
	if total != len(rest) {
		t.Errorf("残り %d 件, 期待 %d", len(rest), total)
	}
}
