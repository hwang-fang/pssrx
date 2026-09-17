package pipeline_test

import (
	"path/filepath"
	"testing"

	"pssrx/internal/config"
	"pssrx/internal/geodesy/geoid"
	"pssrx/internal/pipeline"
)

// TestNewInterrogatorStage は設定から段への変換（変わりうる I/F 層）を確認する。
//
// ゴールデンは段から入るので、ここが壊れても気づけない。設定が導いた
// 解析パラメータと幾何が、そのまま段に載ることを確かめる。
func TestNewInterrogatorStage(t *testing.T) {
	cfg, err := config.Load(filepath.Join("..", "..", "testdata", "kx90.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	ssr, err := cfg.SSR("KX90S")
	if err != nil {
		t.Fatal(err)
	}
	station, err := cfg.Station("KX90")
	if err != nil {
		t.Fatal(err)
	}
	stage, err := pipeline.NewInterrogatorStage(ssr, station)
	if err != nil {
		t.Fatal(err)
	}

	if stage.SSRID != "KX90S" || stage.StationID != "KX90" {
		t.Errorf("ID = (%s, %s), 期待 (KX90S, KX90)", stage.SSRID, stage.StationID)
	}
	wantParams, err := pipeline.InterrogatorParams(ssr.Interrogation)
	if err != nil {
		t.Fatal(err)
	}
	if stage.Params.AroundTimeNs != wantParams.AroundTimeNs ||
		stage.Params.Clockwise != wantParams.Clockwise ||
		stage.Params.Pattern.Length() != wantParams.Pattern.Length() ||
		stage.Params.Pattern.Period() != wantParams.Pattern.Period() {
		t.Errorf("Params = %+v, 期待 %+v", stage.Params, wantParams)
	}
	gm, err := geoid.Load()
	if err != nil {
		t.Fatal(err)
	}
	wantDist, wantAz, err := config.Baseline(ssr, station, gm)
	if err != nil {
		t.Fatal(err)
	}
	if stage.Dist != wantDist || stage.Azimuth != wantAz {
		t.Errorf("幾何 = (%x, %x), 期待 (%x, %x)", stage.Dist, stage.Azimuth, wantDist, wantAz)
	}
}
