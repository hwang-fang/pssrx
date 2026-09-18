package pipeline_test

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"pssrx/internal/config"
	"pssrx/internal/interrogator"
	"pssrx/internal/pipeline"
	"pssrx/internal/pssr"
)

// TestAnalysisMirrorsConfig は設定の鏡像（config.*Analysis）が段の Config の
// 全項目を同じ名前・同じ型のポインタで写していることを確認する。
// 鏡像に項目を足し忘れると「ファイルに書いても効かない」が黙って起きる。
func TestAnalysisMirrorsConfig(t *testing.T) {
	cases := []struct {
		name   string
		cfg    any
		mirror any
	}{
		{"interrogator", interrogator.Config{}, config.InterrogatorAnalysis{}},
		{"pssr", pssr.Config{}, config.PSSRAnalysis{}},
	}
	for _, c := range cases {
		ct, mt := reflect.TypeOf(c.cfg), reflect.TypeOf(c.mirror)
		if ct.NumField() != mt.NumField() {
			t.Errorf("%s: Config %d 項目, 鏡像 %d 項目", c.name, ct.NumField(), mt.NumField())
		}
		for i := range ct.NumField() {
			f := ct.Field(i)
			m, ok := mt.FieldByName(f.Name)
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
	wantP := pssr.DefaultConfig()
	wantP.MaxGap, wantP.MaxAltitudeFt = 0, 70_000 // 0 も正当な指定
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
