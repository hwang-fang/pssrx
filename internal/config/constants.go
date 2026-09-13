package config

// 段をまたいで使う物理定数。

const (
	// SpeedOfLightMPerNs は真空中の光速 [m/ns]。電波の伝搬遅延の換算に使う。
	// 大気屈折は無視する。
	SpeedOfLightMPerNs = 0.299792458

	// TransponderDelayNs は Mode A/C の応答遅延 [ns]。質問（P3）から応答
	// （F1）までの公称値 3.0 µs。公差 ±0.5 µs は対応づけの許容幅で吸収する。
	TransponderDelayNs = 3000
)
