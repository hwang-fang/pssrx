package pipeline_test

import (
	"path/filepath"
	"testing"
	"time"

	"pssrx/internal/config"
	"pssrx/internal/geodesy/geoid"
	"pssrx/internal/pipeline"
)

// TestOptionsJob は設定から Job への変換（変わりうる I/F 層）を確認する。
//
// ゴールデンは Job から入るので、ここが壊れても気づけない。設定が導いた
// 解析パラメータと幾何が、そのまま Job に載ることを確かめる。
func TestOptionsJob(t *testing.T) {
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
	from := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)
	o := pipeline.Options{
		SSR: ssr, Station: station,
		QpkxRoot: "in", IntgRoot: "out",
		From: from, To: from.Add(time.Hour),
		SortInput: true, Append: true,
	}
	job, err := o.Job()
	if err != nil {
		t.Fatal(err)
	}

	if job.SSRID != "KX90S" || job.StationID != "KX90" {
		t.Errorf("ID = (%s, %s), 期待 (KX90S, KX90)", job.SSRID, job.StationID)
	}
	wantParams, err := pipeline.InterrogatorParams(ssr.Interrogation)
	if err != nil {
		t.Fatal(err)
	}
	if job.Params.AroundTimeNs != wantParams.AroundTimeNs ||
		job.Params.Clockwise != wantParams.Clockwise ||
		job.Params.Pattern.Length() != wantParams.Pattern.Length() ||
		job.Params.Pattern.Period() != wantParams.Pattern.Period() {
		t.Errorf("Params = %+v, 期待 %+v", job.Params, wantParams)
	}
	gm, err := geoid.Load()
	if err != nil {
		t.Fatal(err)
	}
	wantDist, wantAz, err := config.Geometry(ssr, station, gm)
	if err != nil {
		t.Fatal(err)
	}
	if job.Dist != wantDist || job.Azimuth != wantAz {
		t.Errorf("幾何 = (%x, %x), 期待 (%x, %x)", job.Dist, job.Azimuth, wantDist, wantAz)
	}
	if job.QpkxRoot != "in" || job.IntgRoot != "out" || !job.From.Equal(from) ||
		!job.To.Equal(from.Add(time.Hour)) || !job.SortInput || !job.Append {
		t.Errorf("入出力設定が Job に写っていない: %+v", job)
	}
}
