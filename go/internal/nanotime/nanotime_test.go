package nanotime

import (
	"testing"
	"time"
)

// TestFromTimeMatchesPython は Python の int(dt.timestamp() * 1e9) と
// 一致することを確認する。1e9 = 2^9 * 5^9 なので、秒値がこの範囲なら
// float 経由でも厳密に一致する。
func TestFromTimeMatchesPython(t *testing.T) {
	cases := []struct {
		t    time.Time
		want int64
	}{
		// 期待値は Python 側 datetime_to_nano() から実測したもの
		{time.Date(2026, 7, 13, 7, 0, 0, 0, JST), 1783893600000000000},
		{time.Date(2026, 6, 10, 0, 0, 0, 0, JST), 1781017200000000000},
		{time.Date(2026, 6, 10, 0, 47, 0, 0, JST), 1781020020000000000},
		{time.Date(2026, 7, 13, 11, 59, 0, 0, JST), 1783911540000000000},
		{time.Date(2026, 1, 1, 0, 0, 0, 0, JST), 1767193200000000000},
	}
	for _, c := range cases {
		if got := FromTime(c.t); got != c.want {
			t.Errorf("FromTime(%s) = %d, Python = %d", c.t, got, c.want)
		}
		if back := ToTime(c.want); !back.Equal(c.t) {
			t.Errorf("ToTime(%d) = %s, 期待 %s", c.want, back, c.t)
		}
	}
}

func TestJSTOffset(t *testing.T) {
	_, off := time.Date(2026, 6, 10, 0, 0, 0, 0, JST).Zone()
	if off != 9*3600 {
		t.Errorf("JST のオフセット %d 秒, 期待 %d 秒", off, 9*3600)
	}
}

func TestTruncateToMinute(t *testing.T) {
	base := time.Date(2026, 6, 10, 0, 47, 0, 0, JST).UnixNano()
	for _, off := range []int64{0, 1, 30_000_000_000, 59_999_999_999} {
		if got := TruncateToMinute(base + off); got != base {
			t.Errorf("TruncateToMinute(base+%d) = %d, 期待 %d", off, got, base)
		}
	}
}
