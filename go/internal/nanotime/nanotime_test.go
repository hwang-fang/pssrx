package nanotime

import (
	"testing"
	"time"
)

// TestFromTime は JST の日時と Unix ナノ秒の対応を固定する。
// この対応がずれると入出力ファイルの置き場所ごと変わってしまう。
func TestFromTime(t *testing.T) {
	cases := []struct {
		t    time.Time
		want int64
	}{
		{time.Date(2026, 7, 13, 7, 0, 0, 0, JST), 1783893600000000000},
		{time.Date(2026, 6, 10, 0, 0, 0, 0, JST), 1781017200000000000},
		{time.Date(2026, 6, 10, 0, 47, 0, 0, JST), 1781020020000000000},
		{time.Date(2026, 7, 13, 11, 59, 0, 0, JST), 1783911540000000000},
		{time.Date(2026, 1, 1, 0, 0, 0, 0, JST), 1767193200000000000},
	}
	for _, c := range cases {
		if got := FromTime(c.t); got != c.want {
			t.Errorf("FromTime(%s) = %d, 期待 %d", c.t, got, c.want)
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
