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
// SSR を原点にした ENU へ変換して出す。移植元は平面直角座標に投影して
// 座標差から出していたが、投影の縮尺係数と子午線収差ぶん値が変わる
// （方位はグリッド北ではなく真北基準になる）。
//
// 従来の設定ファイル（centrair.txt 形式）との対応:
//
//	Lat / Log / Height -> lat / lon / alt  WGS84 で直接受ける（Kei は不要）
//	Quest           -> pattern            "ACAC" のような質問種別文字列
//	QuestCycle      -> quest_cycle_100ns  100 ns 単位の PRI
//	AroundTime      -> around_time_sec    小数を許す
//	Stagger         -> stagger            0 以外は展開規則が不明なので拒否する
package config

import (
	"fmt"
	"maps"
	"math"
	"os"
	"slices"
	"strings"

	"github.com/goccy/go-yaml"
	"pssrx/internal/analyze"
	"pssrx/internal/geodesy"
	"pssrx/internal/pattern"
)

// File は設定ファイル全体。ID をキーにしたマスタで、重複した ID は
// YAML の段階で弾かれる。
type File struct {
	SSRs     map[string]SSR     `yaml:"ssrs"`
	Stations map[string]Station `yaml:"stations"`
}

// SSR は質問を出す二次監視レーダーの情報。
type SSR struct {
	ID            string `yaml:"-"` // マップのキー。読み込み時に埋める
	Name          string `yaml:"name"`
	ICAO          string `yaml:"icao"`
	SerialNo      int    `yaml:"serial_no"`
	Position      `yaml:",inline"`
	Interrogation Interrogation `yaml:"interrogation"`
}

// Station は質問を受信する測定局の情報。
type Station struct {
	ID       string `yaml:"-"` // マップのキー。読み込み時に埋める
	Name     string `yaml:"name"`
	ICAO     string `yaml:"icao"`
	SerialNo int    `yaml:"serial_no"`
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

// Interrogation は質問パラメータ。
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
	i := s.Interrogation
	if i.AroundTimeSec <= 0 {
		return fmt.Errorf("interrogation.around_time_sec は正の値が必要です: %g", i.AroundTimeSec)
	}
	if _, err := pattern.ParseModes(i.Pattern); err != nil {
		return fmt.Errorf("interrogation.pattern: %w", err)
	}
	if len(i.Stagger100) == 0 {
		if i.QuestCycle100 <= 0 {
			return fmt.Errorf("interrogation.quest_cycle_100ns は正の値が必要です: %d", i.QuestCycle100)
		}
		if i.Stagger != 0 {
			return fmt.Errorf("interrogation.stagger=%d の展開規則が不明です。"+
				"stagger_100ns に PRI 列を直接指定してください", i.Stagger)
		}
	} else {
		for k, v := range i.Stagger100 {
			if v <= 0 {
				return fmt.Errorf("interrogation.stagger_100ns[%d] は正の値が必要です: %d", k, v)
			}
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

// Params は SSR の設定から解析用パラメータを組み立てる。
func (s SSR) Params() (analyze.Params, error) {
	i := s.Interrogation
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
		// 秒から ns へは切り捨てで落とす。四捨五入してはならない。
		// 走査周期はドウェル対が何回転ぶん離れているかの判定にしか使わず、
		// 許容は周期の 20% と広い。1 ns の差は効かないが、丸め方を変えると
		// 既存の出力と食い違う。
		AroundTimeNs: int64(math.Trunc(i.AroundTimeSec * 1e9)),
		Pattern:      pat,
		Clockwise:    clockwise,
	}, nil
}

// Geometry は SSR から測定局への距離 [m] と方位 [rad] を返す。
//
// SSR を原点にした ENU に測定局を置き、距離は斜距離、方位は真北基準で
// 出す。geoid は標高を楕円体高へ直すのに使う。
func Geometry(ssr SSR, st Station, geoid geodesy.GeoidHeightProvider) (dist, azimuth float64, err error) {
	conv, err := geodesy.NewENUConverter(ssr.LLA(), geoid)
	if err != nil {
		return 0, 0, fmt.Errorf("SSR %s の位置: %w", ssr.ID, err)
	}
	enu, err := conv.LLAToENU(st.LLA())
	if err != nil {
		return 0, 0, fmt.Errorf("測定局 %s の位置: %w", st.ID, err)
	}
	dist, azimuth = analyze.StationGeometry(enu.E, enu.N, enu.U)
	return dist, azimuth, nil
}
