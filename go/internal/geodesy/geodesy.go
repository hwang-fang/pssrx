package geodesy

import (
	"errors"
	"fmt"
	"math"
)

const (
	// WGS84 ellipsoid.
	wgs84A  = 6378137.0               // semi-major axis [m]
	wgs84F  = 1.0 / 298.257223563     // flattening
	wgs84E2 = wgs84F * (2.0 - wgs84F) // first eccentricity squared

	degToRad = math.Pi / 180.0
	radToDeg = 180.0 / math.Pi
)

var ErrNilGeoid = errors.New("geodesy: geoid height provider is nil")

// OrthometricLLA represents a geodetic position whose height
// is orthometric height (approximately height above mean sea level).
//
// This is the LLA representation used at the application boundary.
type OrthometricLLA struct {
	Lat float64 // WGS84 geodetic latitude [deg]
	Lon float64 // WGS84 longitude [deg]
	Alt float64 // orthometric height H [m]
}

// EllipsoidalLLA represents a geodetic position whose height
// is measured from the WGS84 ellipsoid.
//
// This representation is used internally for ECEF conversion.
type EllipsoidalLLA struct {
	Lat float64 // WGS84 geodetic latitude [deg]
	Lon float64 // WGS84 longitude [deg]
	Alt float64 // ellipsoidal height h [m]
}

type ECEF struct {
	X float64 // [m]
	Y float64 // [m]
	Z float64 // [m]
}

type ENU struct {
	E float64 // East [m]
	N float64 // North [m]
	U float64 // Up [m]
}

// GeoidHeightProvider provides geoid height N at a given
// geodetic latitude and longitude.
//
// internal/geodesy/geoid.Model satisfies this interface.
type GeoidHeightProvider interface {
	GeoidHeight(latDeg, lonDeg float64) (float64, error)
}

type ENUConverter struct {
	geoid GeoidHeightProvider

	// Reference position in ECEF.
	x0 float64
	y0 float64
	z0 float64

	// Precomputed reference latitude/longitude.
	sinLat float64
	cosLat float64
	sinLon float64
	cosLon float64
}

// NewENUConverter creates a local ENU coordinate converter.
//
// ref.Alt is orthometric height H.
// It is converted to WGS84 ellipsoidal height before calculating
// the reference ECEF coordinate.
func NewENUConverter(
	ref OrthometricLLA,
	geoid GeoidHeightProvider,
) (*ENUConverter, error) {
	if geoid == nil {
		return nil, ErrNilGeoid
	}

	ellipsoidalRef, err := toEllipsoidal(ref, geoid)
	if err != nil {
		return nil, fmt.Errorf(
			"geodesy: convert ENU reference height: %w",
			err,
		)
	}

	lat := ref.Lat * degToRad
	lon := ref.Lon * degToRad

	sinLat, cosLat := math.Sincos(lat)
	sinLon, cosLon := math.Sincos(lon)

	ecef := llaToECEF(ellipsoidalRef)

	return &ENUConverter{
		geoid: geoid,

		x0: ecef.X,
		y0: ecef.Y,
		z0: ecef.Z,

		sinLat: sinLat,
		cosLat: cosLat,
		sinLon: sinLon,
		cosLon: cosLon,
	}, nil
}

// ToEllipsoidal converts orthometric height H to
// WGS84 ellipsoidal height h.
//
//	h = H + N
//
// where N is the geoid height.
func ToEllipsoidal(
	p OrthometricLLA,
	geoid GeoidHeightProvider,
) (EllipsoidalLLA, error) {
	if geoid == nil {
		return EllipsoidalLLA{}, ErrNilGeoid
	}

	return toEllipsoidal(p, geoid)
}

func toEllipsoidal(
	p OrthometricLLA,
	geoid GeoidHeightProvider,
) (EllipsoidalLLA, error) {
	n, err := geoid.GeoidHeight(p.Lat, p.Lon)
	if err != nil {
		return EllipsoidalLLA{}, fmt.Errorf(
			"geodesy: get geoid height at lat=%.9f lon=%.9f: %w",
			p.Lat,
			p.Lon,
			err,
		)
	}

	return EllipsoidalLLA{
		Lat: p.Lat,
		Lon: p.Lon,
		Alt: p.Alt + n,
	}, nil
}

// ToOrthometric converts WGS84 ellipsoidal height h
// to orthometric height H.
//
//	H = h - N
//
// where N is the geoid height.
func ToOrthometric(
	p EllipsoidalLLA,
	geoid GeoidHeightProvider,
) (OrthometricLLA, error) {
	if geoid == nil {
		return OrthometricLLA{}, ErrNilGeoid
	}

	return toOrthometric(p, geoid)
}

func toOrthometric(
	p EllipsoidalLLA,
	geoid GeoidHeightProvider,
) (OrthometricLLA, error) {
	n, err := geoid.GeoidHeight(p.Lat, p.Lon)
	if err != nil {
		return OrthometricLLA{}, fmt.Errorf(
			"geodesy: get geoid height at lat=%.9f lon=%.9f: %w",
			p.Lat,
			p.Lon,
			err,
		)
	}

	return OrthometricLLA{
		Lat: p.Lat,
		Lon: p.Lon,
		Alt: p.Alt - n,
	}, nil
}

// llaToECEF converts WGS84 geodetic coordinates to ECEF.
//
// The input height must be WGS84 ellipsoidal height.
func llaToECEF(p EllipsoidalLLA) ECEF {
	lat := p.Lat * degToRad
	lon := p.Lon * degToRad

	sinLat, cosLat := math.Sincos(lat)
	sinLon, cosLon := math.Sincos(lon)

	n := wgs84A /
		math.Sqrt(1.0-wgs84E2*sinLat*sinLat)

	return ECEF{
		X: (n + p.Alt) * cosLat * cosLon,
		Y: (n + p.Alt) * cosLat * sinLon,
		Z: (n*(1.0-wgs84E2) + p.Alt) * sinLat,
	}
}

// LLAToENU converts an orthometric LLA coordinate into
// the local ENU coordinate.
//
// The geoid correction is performed internally before ECEF conversion.
func (c *ENUConverter) LLAToENU(
	p OrthometricLLA,
) (ENU, error) {
	ellipsoidal, err := toEllipsoidal(p, c.geoid)
	if err != nil {
		return ENU{}, err
	}

	ecef := llaToECEF(ellipsoidal)

	dx := ecef.X - c.x0
	dy := ecef.Y - c.y0
	dz := ecef.Z - c.z0

	return ENU{
		E: -c.sinLon*dx +
			c.cosLon*dy,

		N: -c.sinLat*c.cosLon*dx -
			c.sinLat*c.sinLon*dy +
			c.cosLat*dz,

		U: c.cosLat*c.cosLon*dx +
			c.cosLat*c.sinLon*dy +
			c.sinLat*dz,
	}, nil
}

// ENUToECEF converts local ENU coordinates to ECEF.
//
// This operation does not involve geoid correction.
func (c *ENUConverter) ENUToECEF(p ENU) ECEF {
	// Inverse ENU rotation = transpose of ECEF -> ENU matrix.
	dx :=
		-c.sinLon*p.E -
			c.sinLat*c.cosLon*p.N +
			c.cosLat*c.cosLon*p.U

	dy :=
		c.cosLon*p.E -
			c.sinLat*c.sinLon*p.N +
			c.cosLat*c.sinLon*p.U

	dz :=
		c.cosLat*p.N +
			c.sinLat*p.U

	return ECEF{
		X: c.x0 + dx,
		Y: c.y0 + dy,
		Z: c.z0 + dz,
	}
}

// ENUToLLA converts local ENU coordinates to WGS84 latitude,
// longitude and orthometric height.
//
// Conversion:
//
//	ENU
//	  -> ECEF
//	  -> EllipsoidalLLA
//	  -> geoid correction
//	  -> OrthometricLLA
func (c *ENUConverter) ENUToLLA(
	p ENU,
) (OrthometricLLA, error) {
	ellipsoidal := ecefToLLA(c.ENUToECEF(p))

	orthometric, err := toOrthometric(
		ellipsoidal,
		c.geoid,
	)
	if err != nil {
		return OrthometricLLA{}, err
	}

	return orthometric, nil
}

// ecefToLLA converts ECEF to WGS84 geodetic coordinates.
//
// The returned height is WGS84 ellipsoidal height.
//
// Latitude is solved iteratively. For positions near the Earth
// and ordinary aircraft altitudes, convergence is very fast.
func ecefToLLA(p ECEF) EllipsoidalLLA {
	lon := math.Atan2(p.Y, p.X)
	r := math.Hypot(p.X, p.Y)

	// This is exact when h == 0 and provides a very good initial
	// estimate for positions near the Earth.
	lat := math.Atan2(
		p.Z,
		r*(1.0-wgs84E2),
	)

	for range 5 {
		sinLat := math.Sin(lat)

		n := wgs84A /
			math.Sqrt(1.0-wgs84E2*sinLat*sinLat)

		newLat := math.Atan2(
			p.Z+wgs84E2*n*sinLat,
			r,
		)

		if math.Abs(newLat-lat) < 1e-12 {
			lat = newLat
			break
		}

		lat = newLat
	}

	sinLat, cosLat := math.Sincos(lat)

	n := wgs84A /
		math.Sqrt(1.0-wgs84E2*sinLat*sinLat)

	// Numerically stable ellipsoidal-height formula.
	//
	// This avoids division by cos(latitude), which becomes unstable
	// near the poles.
	alt :=
		r*cosLat +
			p.Z*sinLat -
			wgs84A*wgs84A/n

	return EllipsoidalLLA{
		Lat: lat * radToDeg,
		Lon: lon * radToDeg,
		Alt: alt,
	}
}
