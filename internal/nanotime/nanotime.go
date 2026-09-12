// Package nanotime は Unix エポックからのナノ秒と日時の変換を扱う。
package nanotime

import "time"

// Nano は Unix エポックからのナノ秒。データ上の時刻はすべてこの単位で扱う。
type Nano = int64

// JST は qpkx / intg が使う固定オフセットのタイムゾーン。
// ファイル名がこのタイムゾーンで組まれているため、入出力のパス生成と
// 時刻の指定は必ずこれを通す。夏時間が無いので固定オフセットで足りる。
var JST = time.FixedZone("JST", 9*60*60)

// ToTime は ns を JST の時刻に変換する。
func ToTime(ns Nano) time.Time { return time.Unix(0, ns).In(JST) }

// FromTime は時刻を ns に変換する。
func FromTime(t time.Time) Nano { return t.UnixNano() }

// TruncateToMinute は ns をその分の先頭に切り下げる。
func TruncateToMinute(ns Nano) Nano { return ToTime(ns).Truncate(time.Minute).UnixNano() }
