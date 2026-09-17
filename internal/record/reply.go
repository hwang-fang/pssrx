package record

// Reply は測定局で受信した Mode A/C 応答 1 件（apkx のレコード）。
//
// Timestamp は F1 パルス（応答の先頭）の受信時刻。apkx ファイルに記録
// されているのは F2 パルス（末尾のフレーミングパルス）の時刻なので、
// 読み込み時に F1–F2 間隔（ssr.ReplyFrameLengthNs）を引いて直す。
// 応答遅延 3.0 µs は P3 → F1 で定義されるため、対応づけは F1 で行う。
//
// Code は 12 ビットの応答符号で、Mode A 質問への応答ならスコーク、
// Mode C 質問への応答なら高度符号。どちらへの応答かはデータ上には無く、
// 質問予定表との対応づけで決まる。ビット配置の解釈は復号側に任せる。
type Reply struct {
	Timestamp int64 // Unix ナノ秒（F1 パルスの受信時刻）
	Code      uint16
	WH        uint16
}
