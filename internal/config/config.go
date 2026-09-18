// Package config は SSR と測定局のマスタを YAML から読み込む。
//
// 設定ファイルは「どの局とどの SSR を組み合わせて処理するか」を持たない。
// ssrs / stations の一覧だけを持ち、組み合わせは各コマンドが実行時に
// 決める。interrogator は局 1 つと SSR 1 つの組で動くが、後続の PSSR
// （応答信号による位置推定）では SSR 1 つに対して受信局が複数になる
// 見込みで、組み合わせを設定側に固定するとコマンドごとに設定を分ける
// ことになる。
//
// 単位はフィールド名に埋めてある（_sec / _100ns / _ns）。PRI をこの系では
// 100 ns 単位で書く一方、内部では ns で扱うため、名前に単位が無いと
// 100 倍の取り違えが起きる。
//
// 位置は WGS84 の緯度経度と標高（lat / lon / alt）で書く。距離と方位は
// SSR を原点にした ENU へ変換して出す（方位は真北基準）。平面直角座標に
// 投影した座標差から出す方法とは、投影の縮尺係数と子午線収差のぶん値が
// 違う。
//
// 書式は CONFIG.md を参照。
package config

import (
	"fmt"
	"maps"
	"math"
	"os"
	"slices"
	"strings"

	"pssrx/internal/geodesy"
	"pssrx/internal/ssr"

	"github.com/goccy/go-yaml"
)

// File は設定ファイル全体。ID をキーにしたマスタで、重複した ID は
// YAML の段階で弾かれる。
type File struct {
	SSRs     map[string]SSR     `yaml:"ssrs"`
	Stations map[string]Station `yaml:"stations"`
	// Analysis は解析の定数の上書き。省略可で、書いた項目だけが効く。
	Analysis Analysis `yaml:"analysis"`
}

// SSR は質問を出す二次監視レーダーの情報。
type SSR struct {
	ID            string `yaml:"-"` // マップのキー。読み込み時に埋める
	Name          string `yaml:"name"`
	Position      `yaml:",inline"`
	MaxRangeM     float64           `yaml:"max_range_m"` // 覆域 [m]。応答の対応づけの遅延上限を決める
	Interrogation InterrogationSpec `yaml:"interrogation"`
}

// Station は質問を受信する測定局の情報。
type Station struct {
	ID       string `yaml:"-"` // マップのキー。読み込み時に埋める
	Name     string `yaml:"name"`
	Position `yaml:",inline"`
}

// Position は局の位置。WGS84 の緯度経度と標高で書く。
//
// 0 も正当な値なので、書かれたかどうかはポインタの nil で見分ける。
// 標高を省略して 0 扱いにはしない。海面高と取り違えたまま気づけない。
type Position struct {
	Lat *float64 `yaml:"lat"` // WGS84 緯度 [deg]
	Lon *float64 `yaml:"lon"` // WGS84 経度 [deg]
	Alt *float64 `yaml:"alt"` // 標高 [m]。楕円体高ではなくジオイド面からの高さ
}

// LLA は位置を geodesy の型で返す。
func (p Position) LLA() geodesy.OrthometricLLA {
	return geodesy.OrthometricLLA{Lat: *p.Lat, Lon: *p.Lon, Alt: *p.Alt}
}

func (p Position) validate() error {
	for _, f := range []struct {
		name string
		v    *float64
	}{{"lat", p.Lat}, {"lon", p.Lon}, {"alt", p.Alt}} {
		if f.v == nil {
			return fmt.Errorf("%s がありません（lat, lon, alt は 3 つとも必要です）", f.name)
		}
	}
	if math.Abs(*p.Lat) > 90 {
		return fmt.Errorf("lat は -90..90 の範囲が必要です: %g", *p.Lat)
	}
	if math.Abs(*p.Lon) > 180 {
		return fmt.Errorf("lon は -180..180 の範囲が必要です: %g", *p.Lon)
	}
	return nil
}

// InterrogationSpec は SSR の質問の仕様（走査周期・質問パターン）。
//
// 質問パターンは「質問種別の繰り返し」と「質問間隔の繰り返し」の対で
// 表す。2 つの長さは同じでなくてよく、最小公倍数の長さに展開してから
// 最小周期へ簡約する（ssr.PatternFromStagger）。
//
//	mode_pattern: AC                  A, C, A, C, ... と交互
//	interval_pattern_ns: [2949900]    間隔は一定 2.9499 ms
//
// スタガ運用（間隔を周期的に変える）なら interval_pattern_ns に 1 周期ぶんの
// 列を書く。例: [2900000, 2906500, 2913000]。
type InterrogationSpec struct {
	AroundTimeSec     float64 `yaml:"around_time_sec"`
	ModePattern       string  `yaml:"mode_pattern"`        // 質問種別の繰り返し。"AC" など
	IntervalPatternNs []int64 `yaml:"interval_pattern_ns"` // 次の質問までの間隔 [ns] の繰り返し。1 要素以上
	Clockwise         *bool   `yaml:"clockwise"`           // 省略時は true
}

// InterrogationPattern は質問の仕様から質問パターンを組み立てる。
func InterrogationPattern(i InterrogationSpec) (*ssr.Pattern, error) {
	modes, err := ssr.ParseModes(i.ModePattern)
	if err != nil {
		return nil, err
	}
	return ssr.PatternFromStagger(i.IntervalPatternNs, modes)
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
	for id, s := range f.SSRs {
		s.ID = id
		f.SSRs[id] = s
	}
	for id, s := range f.Stations {
		s.ID = id
		f.Stations[id] = s
	}
	if err := f.validate(); err != nil {
		return nil, fmt.Errorf("設定ファイル %s: %w", path, err)
	}
	return &f, nil
}

func (f *File) validate() error {
	if len(f.SSRs) == 0 {
		return fmt.Errorf("ssrs が空です")
	}
	if len(f.Stations) == 0 {
		return fmt.Errorf("stations が空です")
	}
	for _, id := range f.SSRIDs() {
		if err := f.SSRs[id].validate(); err != nil {
			return fmt.Errorf("ssrs.%s: %w", id, err)
		}
	}
	for _, id := range f.StationIDs() {
		if err := f.Stations[id].Position.validate(); err != nil {
			return fmt.Errorf("stations.%s: %w", id, err)
		}
	}
	return nil
}

func (s SSR) validate() error {
	if err := s.Position.validate(); err != nil {
		return err
	}
	if s.MaxRangeM <= 0 {
		return fmt.Errorf("max_range_m は正の値が必要です: %g", s.MaxRangeM)
	}
	i := s.Interrogation
	if i.AroundTimeSec <= 0 {
		return fmt.Errorf("interrogation.around_time_sec は正の値が必要です: %g", i.AroundTimeSec)
	}
	if _, err := ssr.ParseModes(i.ModePattern); err != nil {
		return fmt.Errorf("interrogation.mode_pattern: %w", err)
	}
	if len(i.IntervalPatternNs) == 0 {
		return fmt.Errorf("interrogation.interval_pattern_ns は 1 要素以上必要です")
	}
	for k, v := range i.IntervalPatternNs {
		if v <= 0 {
			return fmt.Errorf("interrogation.interval_pattern_ns[%d] は正の値が必要です: %d", k, v)
		}
	}
	return nil
}

// SSRIDs は登録されている SSR の ID を昇順で返す。
func (f *File) SSRIDs() []string { return slices.Sorted(maps.Keys(f.SSRs)) }

// StationIDs は登録されている測定局の ID を昇順で返す。
func (f *File) StationIDs() []string { return slices.Sorted(maps.Keys(f.Stations)) }

// SSR は ID で SSR を引く。無ければ登録済みの ID を添えてエラーを返す。
func (f *File) SSR(id string) (SSR, error) {
	s, ok := f.SSRs[id]
	if !ok {
		return SSR{}, fmt.Errorf("SSR %q は設定にありません（登録済み: %s）",
			id, strings.Join(f.SSRIDs(), ", "))
	}
	return s, nil
}

// Station は ID で測定局を引く。無ければ登録済みの ID を添えてエラーを返す。
func (f *File) Station(id string) (Station, error) {
	s, ok := f.Stations[id]
	if !ok {
		return Station{}, fmt.Errorf("測定局 %q は設定にありません（登録済み: %s）",
			id, strings.Join(f.StationIDs(), ", "))
	}
	return s, nil
}

// Baseline は SSR から測定局への基線、すなわち距離 [m] と方位 [rad] を返す。
//
// SSR を原点にした ENU に測定局を置き、距離は斜距離、方位は真北基準で
// 出す。geoid は標高を楕円体高へ直すのに使う。
func Baseline(s SSR, st Station, geoid geodesy.GeoidHeightProvider) (dist, azimuth float64, err error) {
	conv, err := geodesy.NewENUConverter(s.LLA(), geoid)
	if err != nil {
		return 0, 0, fmt.Errorf("SSR %s の位置: %w", s.ID, err)
	}
	enu, err := conv.LLAToENU(st.LLA())
	if err != nil {
		return 0, 0, fmt.Errorf("測定局 %s の位置: %w", st.ID, err)
	}
	dist, azimuth = enu.RangeAzimuth()
	return dist, azimuth, nil
}
