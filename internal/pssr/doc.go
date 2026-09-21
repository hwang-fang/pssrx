// Package pssr は測定局で受信した Mode A/C 応答から機体の位置を推定する
// 段（PSSR: 受動 SSR）の入口。コードは役割ごとのサブパッケージにあり、
// ここには置かない。
//
//	plot      単一測定点の応答を SSR の質問予定と対応づけ、プロットにする
//	          （Synchronizer, Pair, Suppress）
//	bistatic  単一測定点の双基地幾何で座標を解く（Geometry, Locate）
//	tracking  座標の列を航跡にする（Track, Link, Resolve, Smooth）。
//	          座標がどう解かれたかは知らない
//	sink      航跡の点を書く（CSVSink）
//	simtest   テスト用の合成データ
//
// 依存の向きは plot ← bistatic → tracking ← sink。pipeline がブロックごとに
// plot → bistatic → tracking → sink の順に呼ぶ。多点の座標算出を足すときは
// bistatic の隣に置き、tracking.Fix を作る側が増えるだけにする。
package pssr
