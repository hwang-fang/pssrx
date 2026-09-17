package record

// Block は解析へ 1 回に投入するデータ。ブロックの区分はデータの出どころが
// 決める（ファイルなら 1 分 1 ファイル、実時間なら受信の刻み）。解析は
// ブロックの幅に依存しない。
//
// Start / End はブロックが受け持つ区間で、Received は [Start, End) に入る。
// End は interrogator 段がブロック末尾のセグメントを次へ繰り越す判定に使う。
// Replies は F1 時刻で、ファイル区分（F2 時刻）より F1–F2 間隔 20.3 µs だけ
// 早い側にずれる。時刻で切り直さず区分のまま渡し、pssr 段の Synchronizer が
// 実際の時刻で対応づけて吸収する。各列は時刻昇順。Last は最後のブロックで、
// 解析は持ち越しているものをすべて処理してよい。
//
// Interrogations は pssr 段を単独で走らせるときの入力で、interrogator 段と
// 直列に流すときは段の出力が使われる。最初のブロックだけは Start より
// 前のレコードを含みうる（archive.FileSource.IntgLeadNs を参照）。
type Block struct {
	Start, End     int64 // Unix ナノ秒
	Last           bool
	Received       []ReceivedInterrogation // 質問受信（interrogator 段の入力）
	Replies        []Reply                 // 応答（pssr 段の入力）
	Interrogations []Interrogation         // 質問予定（pssr 段を単独で走らせるときの入力）
}
