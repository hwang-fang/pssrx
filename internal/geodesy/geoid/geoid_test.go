package geoid

import (
	"errors"
	"math"
	"testing"
)

func TestLoad(t *testing.T) {
	model, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if model == nil {
		t.Fatal("Load() returned nil model")
	}

	if got, want := len(model.data), expectedDataSize; got != want {
		t.Fatalf(
			"embedded data size = %d, want %d",
			got,
			want,
		)
	}
}

func TestHeightAtGridPoint(t *testing.T) {
	model, err := Load()
	if err != nil {
		t.Fatal(err)
	}

	// Select a point well inside the grid.
	const (
		row = 123
		col = 456
	)

	lat := latMax - float64(row)*latStep
	lon := lonMin + float64(col)*lonStep

	want := model.at(row, col)

	got, err := model.GeoidHeight(lat, lon)
	if err != nil {
		t.Fatalf(
			"Height(%v, %v) error: %v",
			lat,
			lon,
			err,
		)
	}

	// At an exact grid point, interpolation should return
	// the grid value itself.
	if math.Abs(got-want) > 1e-6 {
		t.Fatalf(
			"Height(%v, %v) = %.9f, want %.9f",
			lat,
			lon,
			got,
			want,
		)
	}
}

func TestHeightBoundary(t *testing.T) {
	model, err := Load()
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		lat  float64
		lon  float64
	}{
		{
			name: "north west",
			lat:  latMax,
			lon:  lonMin,
		},
		{
			name: "north east",
			lat:  latMax,
			lon:  lonMax,
		},
		{
			name: "south west",
			lat:  latMin,
			lon:  lonMin,
		},
		{
			name: "south east",
			lat:  latMin,
			lon:  lonMax,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := model.GeoidHeight(tt.lat, tt.lon)
			if err != nil {
				t.Fatalf(
					"Height(%v, %v) error: %v",
					tt.lat,
					tt.lon,
					err,
				)
			}

			if math.IsNaN(got) || math.IsInf(got, 0) {
				t.Fatalf(
					"Height(%v, %v) returned non-finite value: %v",
					tt.lat,
					tt.lon,
					got,
				)
			}
		})
	}
}

func TestHeightOutOfRange(t *testing.T) {
	model, err := Load()
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		lat  float64
		lon  float64
	}{
		{
			name: "south of range",
			lat:  latMin - 0.001,
			lon:  135,
		},
		{
			name: "north of range",
			lat:  latMax + 0.001,
			lon:  135,
		},
		{
			name: "west of range",
			lat:  35,
			lon:  lonMin - 0.001,
		},
		{
			name: "east of range",
			lat:  35,
			lon:  lonMax + 0.001,
		},
		{
			name: "NaN latitude",
			lat:  math.NaN(),
			lon:  135,
		},
		{
			name: "NaN longitude",
			lat:  35,
			lon:  math.NaN(),
		},
		{
			name: "positive infinity latitude",
			lat:  math.Inf(1),
			lon:  135,
		},
		{
			name: "negative infinity longitude",
			lat:  35,
			lon:  math.Inf(-1),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := model.GeoidHeight(tt.lat, tt.lon)

			if !errors.Is(err, ErrOutOfRange) {
				t.Fatalf(
					"Height(%v, %v) error = %v, want ErrOutOfRange",
					tt.lat,
					tt.lon,
					err,
				)
			}
		})
	}
}

func TestCellPosition(t *testing.T) {
	const count = 10

	tests := []struct {
		name         string
		pos          float64
		wantIndex    int
		wantFraction float64
	}{
		{
			name:         "first point",
			pos:          0,
			wantIndex:    0,
			wantFraction: 0,
		},
		{
			name:         "inside first cell",
			pos:          0.25,
			wantIndex:    0,
			wantFraction: 0.25,
		},
		{
			name:         "exact interior point",
			pos:          3,
			wantIndex:    3,
			wantFraction: 0,
		},
		{
			name:         "inside cell",
			pos:          3.75,
			wantIndex:    3,
			wantFraction: 0.75,
		},
		{
			name:         "last point",
			pos:          9,
			wantIndex:    8,
			wantFraction: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			index, fraction := cellPosition(
				tt.pos,
				count,
			)

			if index != tt.wantIndex {
				t.Errorf(
					"index = %d, want %d",
					index,
					tt.wantIndex,
				)
			}

			if math.Abs(fraction-tt.wantFraction) > 1e-12 {
				t.Errorf(
					"fraction = %.12f, want %.12f",
					fraction,
					tt.wantFraction,
				)
			}
		})
	}
}

func TestBilinear(t *testing.T) {
	const (
		nw = 10.0
		ne = 20.0
		sw = 30.0
		se = 40.0
	)

	tests := []struct {
		name string
		fx   float64
		fy   float64
		want float64
	}{
		{
			name: "north west",
			fx:   0,
			fy:   0,
			want: nw,
		},
		{
			name: "north east",
			fx:   1,
			fy:   0,
			want: ne,
		},
		{
			name: "south west",
			fx:   0,
			fy:   1,
			want: sw,
		},
		{
			name: "south east",
			fx:   1,
			fy:   1,
			want: se,
		},
		{
			name: "center",
			fx:   0.5,
			fy:   0.5,
			want: 25,
		},
		{
			name: "quarter",
			fx:   0.25,
			fy:   0.25,
			want: 17.5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := bilinear(
				nw,
				ne,
				sw,
				se,
				tt.fx,
				tt.fy,
			)

			if math.Abs(got-tt.want) > 1e-12 {
				t.Fatalf(
					"bilinear(..., %.2f, %.2f) = %.12f, want %.12f",
					tt.fx,
					tt.fy,
					got,
					tt.want,
				)
			}
		})
	}
}
