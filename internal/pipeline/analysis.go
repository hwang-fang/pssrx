package pipeline

import (
	"fmt"
	"log/slog"
	"reflect"
)

// applyAnalysis は設定の analysis 節（段の Config の鏡像。項目はポインタ）
// のうち nil でない項目を cfg の同名フィールドへ代入する。
//
// 鏡像の項目は Config と同じ名前・同じ型（のポインタ）でなければならない。
// 対応は反射で取り、名前や型の食い違いはテスト（TestAnalysisMirrorsConfig）
// で検出する。
func applyAnalysis(cfg any, analysis any) {
	dst := reflect.ValueOf(cfg).Elem()
	src := reflect.ValueOf(analysis)
	for i := range src.NumField() {
		p := src.Field(i)
		if p.IsNil() {
			continue
		}
		name := src.Type().Field(i).Name
		f := dst.FieldByName(name)
		if !f.IsValid() {
			panic(fmt.Sprintf("analysis の項目 %s が %s に無い", name, dst.Type()))
		}
		f.Set(p.Elem())
	}
}

// nonDefault は cfg のうち既定値と違う項目を、設定ファイルのキー名
// （鏡像の yaml タグ）でログ属性にする。どの定数で流したかを記録に残す。
func nonDefault(defaults, cfg any, mirror any) []slog.Attr {
	d := reflect.ValueOf(defaults)
	c := reflect.ValueOf(cfg)
	m := reflect.TypeOf(mirror)
	var attrs []slog.Attr
	for i := range c.NumField() {
		if reflect.DeepEqual(d.Field(i).Interface(), c.Field(i).Interface()) {
			continue
		}
		name := c.Type().Field(i).Name
		key := name
		if mf, ok := m.FieldByName(name); ok {
			if tag := mf.Tag.Get("yaml"); tag != "" {
				key = tag
			}
		}
		attrs = append(attrs, slog.Any(key, c.Field(i).Interface()))
	}
	return attrs
}
