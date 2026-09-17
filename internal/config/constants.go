package config

// 段をまたいで使う物理定数。

const (
	// SpeedOfLightMPerNs は真空中の光速 [m/ns]。電波の伝搬遅延の換算に使う。
	// 大気屈折は無視する。
	SpeedOfLightMPerNs = 0.299792458

	// TransponderDelayNs は Mode A/C の応答遅延 [ns]。質問（P3）から応答の
	// 先頭（F1）までの公称値 3.0 µs。公差 ±0.5 µs は対応づけの許容幅で吸収する。
	TransponderDelayNs = 3000

	// ReplyFrameLengthNs は Mode A/C 応答の先頭 F1 パルスから末尾 F2 パルス
	// までの間隔 [ns]。20.3 µs。apkx ファイルの時刻は F2 のものなので、
	// 読み込み時にこれを引いて F1 の時刻に直す（store.DecodeApkx）。
	ReplyFrameLengthNs = 20300
)
