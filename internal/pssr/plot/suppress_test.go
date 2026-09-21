package plot_test

import (
	"testing"

	"pssrx/internal/pssr/plot"
)

// suppressor は抑圧の状態と統計をまとめたテスト用の入れ物。
type suppressor struct {
	st    plot.SuppressState
	stats plot.Stats
}

func newSuppressor(t *testing.T) *suppressor {
	t.Helper()
	return &suppressor{}
}

func (s *suppressor) Push(plots []plot.Plot, last bool) []plot.Plot {
	return plot.Suppress(&s.st, &s.stats, testParams, plot.DefaultConfig(), plots, last)
}

func (s *suppressor) Stats() plot.Stats { return s.stats }

// mkPlot は抑圧の判定に要る項目だけを持つプロットを作る。
func mkPlot(t int64, squawk uint16, alt int, tau int64, n int) plot.Plot {
	return plot.Plot{
		Timestamp: start + t, Squawk: squawk, AltitudeFt: alt, TauNs: tau,
		Replies: make([]plot.PairedReply, n),
	}
}

func pushAll(s *suppressor, plots ...plot.Plot) []plot.Plot {
	return s.Push(plots, true)
}

func taus(plots []plot.Plot) []int64 {
	out := make([]int64, len(plots))
	for i, p := range plots {
		out[i] = p.TauNs
	}
	return out
}

// TestSuppressSidelobe は τ が同じで応答数の少ないプロットが落ちることを確認する。
func TestSuppressSidelobe(t *testing.T) {
	s := newSuppressor(t)
	main := mkPlot(0, 0o3534, 5500, 457_000, 20)
	ghost := mkPlot(1_900_000_000, 0o3534, 5500, 457_300, 8) // 1.9 s 後、別方位
	got := pushAll(s, main, ghost)
	if len(got) != 1 || len(got[0].Replies) != 20 {
		t.Errorf("残り = %v, 期待 主ビームのみ", taus(got))
	}
	if st := s.Stats(); st.Sidelobe != 1 || st.Multipath != 0 || st.Kept != 1 {
		t.Errorf("stats = %+v", st)
	}
}

// TestSuppressMultipath は τ が大きいプロットが、応答数が多くても落ちることを確認する。
func TestSuppressMultipath(t *testing.T) {
	s := newSuppressor(t)
	main := mkPlot(0, 0o3534, 5500, 457_000, 12)
	ghost := mkPlot(-1_900_000_000, 0o3534, 5500, 457_000+30_000, 22) // 経路差 9 km、主より長い列
	got := pushAll(s, ghost, main)
	if len(got) != 1 || got[0].TauNs != 457_000 {
		t.Errorf("残り = %v, 期待 τ 最小の主ビーム", taus(got))
	}
	if st := s.Stats(); st.Multipath != 1 || st.Sidelobe != 0 {
		t.Errorf("stats = %+v", st)
	}
}

// TestSuppressMotionTolerance は接近中の機体で後のサイドローブの τ が
// わずかに小さくても、直接照射の群として応答数で判定することを確認する。
func TestSuppressMotionTolerance(t *testing.T) {
	s := newSuppressor(t)
	main := mkPlot(0, 0o3534, 5500, 457_000, 20)
	ghost := mkPlot(2_000_000_000, 0o3534, 5500, 457_000-1_700, 6) // 2 秒で 1.7 µs 減
	got := pushAll(s, main, ghost)
	if len(got) != 1 || len(got[0].Replies) != 20 {
		t.Errorf("残り = %v, 期待 主ビーム", taus(got))
	}
}

// TestSuppressKeepsDifferentAircraft は高度が違う、または走査が違う同じスコークを
// 別機として残すことを確認する。
func TestSuppressKeepsDifferentAircraft(t *testing.T) {
	s := newSuppressor(t)
	a := mkPlot(0, 0o1200, 5500, 457_000, 20)
	b := mkPlot(1_000_000_000, 0o1200, 5800, 490_000, 20)      // 高度差 300 ft
	c := mkPlot(aroundNs+10_000_000, 0o1200, 5500, 500_000, 5) // 次の走査
	got := pushAll(s, a, b, c)
	if len(got) != 3 {
		t.Errorf("残り = %v, 期待 3 件すべて", taus(got))
	}
}

// TestSuppressStreaming は分割して渡しても一括と同じ結果になり、判定が
// 同じ走査の相手が出揃うまで保留されることを確認する。
func TestSuppressStreaming(t *testing.T) {
	plots := []plot.Plot{
		mkPlot(0, 0o3534, 5500, 457_000, 20),
		mkPlot(1_900_000_000, 0o3534, 5500, 457_300, 8),                // サイドローブ
		mkPlot(2_100_000_000, 0o3534, 5500, 457_000+40_000, 22),        // 反射
		mkPlot(aroundNs, 0o3534, 5400, 453_000, 19),                    // 次の走査の主
		mkPlot(aroundNs+1_900_000_000, 0o3534, 5400, 453_300, 9),       // 次の走査のサイドローブ
		mkPlot(2*aroundNs+500_000_000, 0o3534, 5400, 449_000, 21),      // その次
		mkPlot(2*aroundNs+3_000_000_000, 0o6250, 12000, 1_200_000, 18), // 別機
	}
	want := pushAll(newSuppressor(t), plots...)
	if len(want) != 4 {
		t.Fatalf("一括の残り %d 件, 期待 4", len(want))
	}

	s := newSuppressor(t)
	var got []plot.Plot
	// 1 件目を渡した直後は判定できない（同じ走査の相手がまだ来うる）
	if r := s.Push(plots[:1], false); len(r) != 0 {
		t.Errorf("相手が出揃う前に判定した: %v", taus(r))
	}
	got = append(got, s.Push(plots[1:3], false)...)
	got = append(got, s.Push(plots[3:5], false)...)
	got = append(got, s.Push(plots[5:], false)...)
	got = append(got, s.Push(nil, true)...)
	if len(got) != len(want) {
		t.Fatalf("分割の残り %v, 一括 %v", taus(got), taus(want))
	}
	for i := range want {
		if got[i].Timestamp != want[i].Timestamp {
			t.Errorf("[%d] %d != %d", i, got[i].Timestamp, want[i].Timestamp)
		}
	}
}

// TestSuppressKeepsFarDuplicateForResolve は τ の一致する候補でも方位が
// ImageAzimuthSeparationRad を超えて離れていれば、応答数によらず両方
// 通すことを確認する（像の判定は便の文脈で行う）。近ければ従来どおり
// 応答数最多だけを残す。
func TestSuppressKeepsFarDuplicateForResolve(t *testing.T) {
	sep := plot.DefaultConfig().ImageAzimuthSeparationRad
	main := mkPlot(0, 0o3534, 5500, 457_000, 20)
	main.Azimuth = 1.0
	far := mkPlot(1_500_000_000, 0o3534, 5500, 457_200, 8)
	far.Azimuth = 1.0 + 2*sep
	got := pushAll(newSuppressor(t), main, far)
	if len(got) != 2 {
		t.Fatalf("残り = %v, 期待 両方（方位差 %.1f° > 上限）", taus(got), 2*sep*180/3.14159)
	}
	near := mkPlot(1_500_000_000, 0o3534, 5500, 457_200, 8)
	near.Azimuth = 1.0 + sep/2
	s := newSuppressor(t)
	got = pushAll(s, main, near)
	if len(got) != 1 || len(got[0].Replies) != 20 || s.Stats().Sidelobe != 1 {
		t.Errorf("近い候補: 残り = %v stats %+v, 期待 主ビームのみ", taus(got), s.Stats())
	}
	// 反射（τ が大きい）は方位が離れていても落ちる
	multi := mkPlot(1_500_000_000, 0o3534, 5500, 457_000+plot.DefaultConfig().DirectTauToleranceNs+1, 25)
	multi.Azimuth = 1.0 + 2*sep
	s = newSuppressor(t)
	got = pushAll(s, main, multi)
	if len(got) != 1 || s.Stats().Multipath != 1 {
		t.Errorf("反射: 残り = %v stats %+v", taus(got), s.Stats())
	}
}
