package record

import "time"

// データ上の時刻は Unix エポックからのナノ秒（int64）で扱う。

// JST は qpkx / apkx / intg が使う固定オフセットのタイムゾーン。
// ファイル名がこのタイムゾーンで組まれているため、入出力のパス生成と
// 時刻の指定は必ずこれを通す。夏時間が無いので固定オフセットで足りる。
var JST = time.FixedZone("JST", 9*60*60)

// ToTime は Unix ナノ秒を JST の時刻に変換する。
func ToTime(ns int64) time.Time { return time.Unix(0, ns).In(JST) }
