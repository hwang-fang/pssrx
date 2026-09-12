package geodesy

import (
	"math"
	"testing"
)

type stubGeoid struct {
	height float64
}

func (g stubGeoid) GeoidHeight(
	latDeg,
	lonDeg float64,
) (float64, error) {
	return g.height, nil
}

func TestToEllipsoidal(t *testing.T) {
	const (
		orthometricAlt = 25.72
		geoidHeight    = 40.12
		wantAlt        = 65.84
	)

	p := OrthometricLLA{
		Lat: 36.103774791666666,
		Lon: 140.08785504166664,
		Alt: orthometricAlt,
	}

	got, err := ToEllipsoidal(
		p,
		stubGeoid{height: geoidHeight},
	)
	if err != nil {
		t.Fatalf("ToEllipsoidal() error: %v", err)
	}

	if got.Lat != p.Lat {
		t.Errorf("Lat = %.12f, want %.12f", got.Lat, p.Lat)
	}

	if got.Lon != p.Lon {
		t.Errorf("Lon = %.12f, want %.12f", got.Lon, p.Lon)
	}

	if math.Abs(got.Alt-wantAlt) > 1e-12 {
		t.Errorf(
			"Alt = %.12f, want %.12f",
			got.Alt,
			wantAlt,
		)
	}
}

func TestToOrthometric(t *testing.T) {
	const (
		ellipsoidalAlt = 65.84
		geoidHeight    = 40.12
		wantAlt        = 25.72
	)

	p := EllipsoidalLLA{
		Lat: 36.103774791666666,
		Lon: 140.08785504166664,
		Alt: ellipsoidalAlt,
	}

	got, err := ToOrthometric(
		p,
		stubGeoid{height: geoidHeight},
	)
	if err != nil {
		t.Fatalf("ToOrthometric() error: %v", err)
	}

	if got.Lat != p.Lat {
		t.Errorf("Lat = %.12f, want %.12f", got.Lat, p.Lat)
	}

	if got.Lon != p.Lon {
		t.Errorf("Lon = %.12f, want %.12f", got.Lon, p.Lon)
	}

	if math.Abs(got.Alt-wantAlt) > 1e-12 {
		t.Errorf(
			"Alt = %.12f, want %.12f",
			got.Alt,
			wantAlt,
		)
	}
}

func TestLLAToECEF(t *testing.T) {
	const (
		lat = 36.103774791666666
		lon = 140.08785504166664

		orthometricAlt = 25.72
		geoidHeight    = 40.12
		ellipsoidalAlt = orthometricAlt + geoidHeight
	)

	p := EllipsoidalLLA{
		Lat: lat,
		Lon: lon,
		Alt: ellipsoidalAlt,
	}

	ecef := llaToECEF(p)

	const tolerance = 0.001 // 1 mm

	if math.Abs(ecef.X-(-3957314.622)) > tolerance {
		t.Errorf(
			"X = %.6f, want %.6f",
			ecef.X,
			-3957314.622,
		)
	}

	if math.Abs(ecef.Y-3310254.134) > tolerance {
		t.Errorf(
			"Y = %.6f, want %.6f",
			ecef.Y,
			3310254.134,
		)
	}

	if math.Abs(ecef.Z-3737540.044) > tolerance {
		t.Errorf(
			"Z = %.6f, want %.6f",
			ecef.Z,
			3737540.044,
		)
	}
}

func TestECEFToLLA(t *testing.T) {
	p := ECEF{
		X: -3920022.887,
		Y: 3440202.729,
		Z: 3676451.130,
	}

	lla := ecefToLLA(p)

	const (
		wantLat = 35.36142222
		wantLon = 138.72988888
		wantAlt = 10040.840
	)

	// Approximately millimeter-level angular tolerance
	// at the Earth's surface.
	const angleTolerance = 1e-8 // deg
	const altTolerance = 0.001  // m

	if math.Abs(lla.Lat-wantLat) > angleTolerance {
		t.Errorf(
			"Lat = %.12f, want %.12f",
			lla.Lat,
			wantLat,
		)
	}

	if math.Abs(lla.Lon-wantLon) > angleTolerance {
		t.Errorf(
			"Lon = %.12f, want %.12f",
			lla.Lon,
			wantLon,
		)
	}

	if math.Abs(lla.Alt-wantAlt) > altTolerance {
		t.Errorf(
			"Alt = %.6f, want %.6f",
			lla.Alt,
			wantAlt,
		)
	}
}

func TestENUConverterRoundTrip(t *testing.T) {
	geoid := stubGeoid{
		height: 40.12,
	}

	ref := OrthometricLLA{
		Lat: 36.103774791666666,
		Lon: 140.08785504166664,
		Alt: 25.72,
	}

	converter, err := NewENUConverter(ref, geoid)
	if err != nil {
		t.Fatalf("NewENUConverter() error: %v", err)
	}

	want := OrthometricLLA{
		Lat: 36.250000,
		Lon: 140.300000,
		Alt: 10000.0,
	}

	enu, err := converter.LLAToENU(want)
	if err != nil {
		t.Fatalf("LLAToENU() error: %v", err)
	}

	got, err := converter.ENUToLLA(enu)
	if err != nil {
		t.Fatalf("ENUToLLA() error: %v", err)
	}

	const (
		angleTolerance = 1e-8 // deg
		altTolerance   = 0.001
	)

	if math.Abs(got.Lat-want.Lat) > angleTolerance {
		t.Errorf(
			"Lat = %.12f, want %.12f",
			got.Lat,
			want.Lat,
		)
	}

	if math.Abs(got.Lon-want.Lon) > angleTolerance {
		t.Errorf(
			"Lon = %.12f, want %.12f",
			got.Lon,
			want.Lon,
		)
	}

	if math.Abs(got.Alt-want.Alt) > altTolerance {
		t.Errorf(
			"Alt = %.6f, want %.6f",
			got.Alt,
			want.Alt,
		)
	}
}
