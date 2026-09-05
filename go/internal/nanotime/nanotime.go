// Package nanotime は Unix エポックからのナノ秒と日時の変換を扱う。
// 移植元の interrogator/timestamp.py に対応する。
package nanotime

import "time"

// Nano は Unix エポックからのナノ秒。timestamp.py の NanoUnixTime に対応する。
type Nano = int64

// JST は timestamp.py の JST_TZ と同じ固定オフセット。
// qpkx / intg のファイル名がこのタイムゾーンで組まれているため、
// 入出力のパス生成は必ずこれを通す。
var JST = time.FixedZone("JST", 9*60*60)

// ToTime は ns を JST の時刻に変換する。
func ToTime(ns Nano) time.Time { return time.Unix(0, ns).In(JST) }

// FromTime は時刻を ns に変換する。
//
// Python 側は int(dt.timestamp() * 1e9) と float を経由するが、
// 1e9 = 2^9 * 5^9 なので秒値が 2^53 / 5^9 (= 約 4.6e9、西暦 2115 年頃) 未満なら
// 積は float64 で厳密に表現でき、この整数演算と一致する。
func FromTime(t time.Time) Nano { return t.UnixNano() }

// TruncateToMinute は ns をその分の先頭に切り下げる。
func TruncateToMinute(ns Nano) Nano { return ToTime(ns).Truncate(time.Minute).UnixNano() }
