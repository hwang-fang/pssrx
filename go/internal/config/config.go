// Package config は SSR と測定局の設定を YAML から読み込む。
//
// 移植元には未使用の pydantic モデル（config.py）と、実在する独自形式の
// 設定ファイル（centrair.txt）の 2 系統があり、両者は噛み合っていなかった。
// ここでは実データと整合の取れている centrair.txt 側の意味論を採り、
// 表現だけを YAML に移している。対応は次のとおり。
//
//	Lat / Log / Kei -> x / y      投影変換は本実装の範囲外なので直交座標で受ける
//	Quest           -> pattern    "ACAC" のような質問種別文字列
//	QuestCycle      -> quest_cycle_100ns  100 ns 単位の PRI
//	AroundTime      -> around_time_sec    小数（centrair.txt の実値は 4.05）
//	Stagger         -> stagger            0 以外は展開規則が不明なので拒否する
//
// 参照箇所が無かったフィールド（interval_tolerance_ns / count_lag /
// altitude / epsg）は移植していない。
package config

import (
	"fmt"
	"math"
	"os"

	"github.com/goccy/go-yaml"
	"pssrx/internal/analyze"
	"pssrx/internal/pattern"
)

// File は設定ファイル全体。
type File struct {
	SSR     SSR     `yaml:"ssr"`
	Station Station `yaml:"station"`
}

// SSR は質問を出す二次監視レーダーの情報。
type SSR struct {
	ID            string        `yaml:"id"`
	Name          string        `yaml:"name"`
	ICAO          string        `yaml:"icao"`
	SerialNo      int           `yaml:"serial_no"`
	X             float64       `yaml:"x"` // 直交座標 [m]
	Y             float64       `yaml:"y"`
	Interrogation Interrogation `yaml:"interrogation"`
}

// Station は質問を受信する測定局の情報。
type Station struct {
	ID       string  `yaml:"id"`
	Name     string  `yaml:"name"`
	ICAO     string  `yaml:"icao"`
	SerialNo int     `yaml:"serial_no"`
	X        float64 `yaml:"x"` // 直交座標 [m]
	Y        float64 `yaml:"y"`
}

// Interrogation は質問パラメータ。単位はフィールド名に埋めてある。
// 移植元で最も取り違えやすかったのが PRI の 100 ns 単位と ns 単位の差なので、
// 設定の段階で単位を明示する。
type Interrogation struct {
	AroundTimeSec float64 `yaml:"around_time_sec"`
	Pattern       string  `yaml:"pattern"`
	QuestCycle100 int64   `yaml:"quest_cycle_100ns"`
	Stagger100    []int64 `yaml:"stagger_100ns"` // 指定時は quest_cycle_100ns より優先
	Stagger       int     `yaml:"stagger"`
	Clockwise     *bool   `yaml:"clockwise"` // 省略時は true
}

// Load は YAML を読み込んで検証する。
func Load(path string) (*File, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f File
	if err := yaml.UnmarshalWithOptions(raw, &f, yaml.Strict()); err != nil {
		return nil, fmt.Errorf("設定ファイル %s の解析に失敗: %w", path, err)
	}
	if err := f.validate(); err != nil {
		return nil, fmt.Errorf("設定ファイル %s: %w", path, err)
	}
	return &f, nil
}

func (f *File) validate() error {
	if f.SSR.ID == "" {
		return fmt.Errorf("ssr.id が空です")
	}
	if f.Station.ID == "" {
		return fmt.Errorf("station.id が空です")
	}
	i := f.SSR.Interrogation
	if i.AroundTimeSec <= 0 {
		return fmt.Errorf("ssr.interrogation.around_time_sec は正の値が必要です: %g", i.AroundTimeSec)
	}
	if _, err := pattern.ParseModes(i.Pattern); err != nil {
		return fmt.Errorf("ssr.interrogation.pattern: %w", err)
	}
	if len(i.Stagger100) == 0 {
		if i.QuestCycle100 <= 0 {
			return fmt.Errorf("ssr.interrogation.quest_cycle_100ns は正の値が必要です: %d", i.QuestCycle100)
		}
		if i.Stagger != 0 {
			return fmt.Errorf("ssr.interrogation.stagger=%d の展開規則が不明です。"+
				"stagger_100ns に PRI 列を直接指定してください", i.Stagger)
		}
	} else {
		for k, v := range i.Stagger100 {
			if v <= 0 {
				return fmt.Errorf("ssr.interrogation.stagger_100ns[%d] は正の値が必要です: %d", k, v)
			}
		}
	}
	return nil
}

// Params は設定から解析用パラメータを組み立てる。
func (f *File) Params() (analyze.Params, error) {
	i := f.SSR.Interrogation
	modes, err := pattern.ParseModes(i.Pattern)
	if err != nil {
		return analyze.Params{}, err
	}
	cycles := i.Stagger100
	if len(cycles) == 0 {
		cycles = []int64{i.QuestCycle100}
	}
	staggerNs := make([]int64, len(cycles))
	for k, v := range cycles {
		staggerNs[k] = v * 100
	}
	pat, err := pattern.FromStagger(staggerNs, modes)
	if err != nil {
		return analyze.Params{}, err
	}
	clockwise := true
	if i.Clockwise != nil {
		clockwise = *i.Clockwise
	}
	return analyze.Params{
		// Python 側は int(around_time_sec * 1e9) と切り捨てる。
		// たとえば 4.1 秒は 4099999999 ns になるため、四捨五入してはならない。
		AroundTimeNs: int64(math.Trunc(i.AroundTimeSec * 1e9)),
		Pattern:      pat,
		Clockwise:    clockwise,
	}, nil
}

// Geometry は SSR から測定局への距離 [m] と方位 [rad] を返す。
func (f *File) Geometry() (dist, azimuth float64) {
	return analyze.StationGeometry(f.SSR.X, f.SSR.Y, f.Station.X, f.Station.Y)
}
