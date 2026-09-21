package pipeline_test

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"pssrx/internal/config"
	"pssrx/internal/interrogator"
	"pssrx/internal/pipeline"
	"pssrx/internal/pssr/bistatic"
	"pssrx/internal/pssr/plot"
	"pssrx/internal/pssr/tracking"
)

// TestAnalysisMirrorsConfig は設定の鏡像（config.*Analysis）が段の Config の
// 全項目を同じ名前・同じ型のポインタで写していることを確認する。
// 鏡像に項目を足し忘れると「ファイルに書いても効かない」が黙って起きる。
func TestAnalysisMirrorsConfig(t *testing.T) {
	// pssr 段は 3 つの Config を 1 つの鏡像で写す。方位差は抑圧と重複解消が
	// 共用し、鏡像の resolve_azimuth_separation_rad を pipeline が両方に配る
	cases := []struct {
		name   string
		cfgs   []any
		mirror any
		shared map[string]string // Config の項目 → 鏡像の項目（名前が違うもの）
	}{
		{"interrogator", []any{interrogator.Config{}}, config.InterrogatorAnalysis{}, nil},
		{"pssr", []any{plot.Config{}, bistatic.Config{}, tracking.Config{}}, config.PSSRAnalysis{},
			map[string]string{"ImageAzimuthSeparationRad": "ResolveAzimuthSeparationRad"}},
	}
	for _, c := range cases {
		mt := reflect.TypeOf(c.mirror)
		var fields []reflect.StructField
		for _, cfg := range c.cfgs {
			ct := reflect.TypeOf(cfg)
			for i := range ct.NumField() {
				fields = append(fields, ct.Field(i))
			}
		}
		if len(fields)-len(c.shared) != mt.NumField() {
			t.Errorf("%s: Config %d 項目（共用 %d）, 鏡像 %d 項目", c.name, len(fields), len(c.shared), mt.NumField())
		}
		for _, f := range fields {
			name := f.Name
			if alias, ok := c.shared[name]; ok {
				name = alias
			}
			m, ok := mt.FieldByName(name)
			if !ok {
				t.Errorf("%s: 鏡像に %s が無い", c.name, f.Name)
				continue
			}
			if m.Type.Kind() != reflect.Pointer || m.Type.Elem() != f.Type {
				t.Errorf("%s: %s の型は *%s であるべきところ %s", c.name, f.Name, f.Type, m.Type)
			}
			tag := m.Tag.Get("yaml")
			if tag == "" || tag != strings.ToLower(tag) || strings.Contains(tag, "-") {
				t.Errorf("%s: %s の yaml タグが snake_case でない: %q", c.name, f.Name, tag)
			}
		}
	}
}

func ptr[T any](v T) *T { return &v }

// TestNewStageAppliesAnalysis は analysis 節の項目だけが既定値を上書きし、
// 不正な値が節の場所付きでエラーになることを確認する。
func TestNewStageAppliesAnalysis(t *testing.T) {
	cfg, err := config.Load(filepath.Join("..", "..", "testdata", "kx90.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	ssr, _ := cfg.SSR("KX90S")
	st, _ := cfg.Station("KX90")

	is, err := pipeline.NewInterrogatorStage(ssr, st, config.InterrogatorAnalysis{
		AmplitudeGateDbm: ptr(-40.0), MinChainLength: ptr(12),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := interrogator.DefaultConfig()
	want.AmplitudeGateDbm, want.MinChainLength = -40, 12
	if is.Config != want {
		t.Errorf("interrogator: %+v\n期待 %+v", is.Config, want)
	}

	ps, err := pipeline.NewPSSRStage(ssr, st, config.PSSRAnalysis{MaxGap: ptr(0), MaxAltitudeFt: ptr(70_000)})
	if err != nil {
		t.Fatal(err)
	}
	wantP := pipeline.DefaultPSSRConfig()
	wantP.Plot.MaxGap, wantP.Plot.MaxAltitudeFt = 0, 70_000 // 0 も正当な指定
	if ps.Config != wantP {
		t.Errorf("pssr: %+v\n期待 %+v", ps.Config, wantP)
	}

	if _, err := pipeline.NewInterrogatorStage(ssr, st, config.InterrogatorAnalysis{DwellGapPeriods: ptr(1.5)}); err == nil || !strings.Contains(err.Error(), "analysis.interrogator") {
		t.Errorf("interrogator の不正な値がエラーにならない、または場所が無い: %v", err)
	}
	if _, err := pipeline.NewPSSRStage(ssr, st, config.PSSRAnalysis{MinReplies: ptr(0)}); err == nil || !strings.Contains(err.Error(), "analysis.pssr") {
		t.Errorf("pssr の不正な値がエラーにならない、または場所が無い: %v", err)
	}
}
