// Package record は段のあいだを流れるレコードの型と、時刻の規約を持つ。
//
// 時刻は Unix エポックからのナノ秒（int64）で扱い、表示と分の区切りは
// JST 固定。ファイル形式や配置は archive が持ち、段はこのパッケージだけを
// 知る。
package record

// Interrogation は SSR が送った質問 1 発。質問予定表（intg）のレコードで、
// interrogator 段がドウェルの間を内挿して作り、pssr 段が応答と対応づける。
type Interrogation struct {
	Timestamp int64   // Unix ナノ秒（SSR の送信時刻）
	Azimuth   float64 // ビーム方位 [rad], [0, 2pi)
	Mode      uint8   // 質問種別（ssr.ModeCode）
}
