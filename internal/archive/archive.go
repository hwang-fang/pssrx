// Package archive は測定局の分ファイル（qpkx / apkx / intg）の配置と形式を
// 持ち、分単位に読み書きし、分ファイルからブロックを作る。
//
// いずれも 1 分 1 ファイルで、レコードはリトルエンディアンの固定長。
// パディングは無い。タイムスタンプはファイルが受け持つ分の先頭からの
// 相対値で、100 ns 単位。
//
//	qpkx: u4 タイムスタンプ(分内 100ns 単位) + u1 質問種別 + u2 波高値  = 7 byte
//	apkx: u4 タイムスタンプ(分内 100ns 単位) + u2 応答符号 + u2 波高値  = 8 byte
//	intg: u4 タイムスタンプ(分内 100ns 単位) + u1 質問種別 + u4 方位角  = 9 byte
//
// ファイル名は JST で組まれている（record.JST）。
package archive

const (
	// OneMinute は 1 分のナノ秒。ファイルの区切り単位。
	OneMinute = int64(60_000_000_000)
	// tsResolution はファイル上のタイムスタンプの分解能 [ns]。
	tsResolution = int64(100)

	qpkxRecordSize = 7
	apkxRecordSize = 8
	intgRecordSize = 9
)
