package record

import (
	"testing"
	"time"
)

// TestJSTNanoseconds は JST の日時と Unix ナノ秒の対応を固定する。
// この対応がずれると入出力ファイルの置き場所ごと変わってしまう。
func TestJSTNanoseconds(t *testing.T) {
	cases := []struct {
		t    time.Time
		want int64
	}{
		{time.Date(2026, 7, 13, 7, 0, 0, 0, JST), 1783893600000000000},
		{time.Date(2026, 6, 10, 0, 0, 0, 0, JST), 1781017200000000000},
		{time.Date(2026, 6, 10, 0, 47, 0, 0, JST), 1781020020000000000},
		{time.Date(2026, 1, 1, 0, 0, 0, 0, JST), 1767193200000000000},
	}
	for _, c := range cases {
		if got := c.t.UnixNano(); got != c.want {
			t.Errorf("%s = %d ns, 期待 %d", c.t, got, c.want)
		}
		if back := ToTime(c.want); !back.Equal(c.t) || back.Location() != JST {
			t.Errorf("ToTime(%d) = %s, 期待 %s (JST)", c.want, back, c.t)
		}
	}
	if _, off := time.Date(2026, 6, 10, 0, 0, 0, 0, JST).Zone(); off != 9*3600 {
		t.Errorf("JST のオフセット %d 秒, 期待 %d 秒", off, 9*3600)
	}
}
