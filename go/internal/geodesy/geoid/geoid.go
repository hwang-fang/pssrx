package geoid

import (
	_ "embed"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

const (
	// JPGEO2024 coverage.
	latMin = 15.0
	latMax = 50.0
	lonMin = 120.0
	lonMax = 160.0

	// JPGEO2024 grid spacing.
	latStep = 1.0 / 60.0 // 1 minute
	lonStep = 1.5 / 60.0 // 1.5 minutes

	numRows = 2101
	numCols = 1601

	bytesPerValue = 4

	expectedDataSize = numRows * numCols * bytesPerValue
)

var (
	ErrOutOfRange = errors.New("geoid: coordinate out of JPGEO2024 range")
)

//go:embed data/jpgeo2024.bin
var embeddedData []byte

// Model represents the JPGEO2024 geoid model.
//
// The model is immutable after loading and is safe for concurrent use.
type Model struct {
	data []byte
}

// Load loads the embedded JPGEO2024 model.
//
// The embedded binary is expected to contain:
//
//	float32 Little Endian × 2101 × 1601
//
// Grid order:
//
//	north -> south
//	west  -> east
func Load() (*Model, error) {
	if len(embeddedData) != expectedDataSize {
		return nil, fmt.Errorf(
			"geoid: invalid JPGEO2024 data size: got %d bytes, want %d",
			len(embeddedData),
			expectedDataSize,
		)
	}

	return &Model{
		data: embeddedData,
	}, nil
}

// Height returns the JPGEO2024 geoid height at the specified
// geodetic latitude and longitude.
//
// latDeg and lonDeg are specified in degrees.
// The returned geoid height is in meters.
//
// The geoid height N can be used to approximately convert between
// orthometric height H and ellipsoidal height h:
//
//	h = H + N
//	H = h - N
func (m *Model) GeoidHeight(latDeg, lonDeg float64) (float64, error) {
	if !isFinite(latDeg) || !isFinite(lonDeg) {
		return 0, fmt.Errorf(
			"%w: lat=%v lon=%v",
			ErrOutOfRange,
			latDeg,
			lonDeg,
		)
	}

	if latDeg < latMin ||
		latDeg > latMax ||
		lonDeg < lonMin ||
		lonDeg > lonMax {
		return 0, fmt.Errorf(
			"%w: lat=%.9f lon=%.9f",
			ErrOutOfRange,
			latDeg,
			lonDeg,
		)
	}

	// JPGEO2024 rows are stored north -> south.
	rowPos := (latMax - latDeg) / latStep

	// JPGEO2024 columns are stored west -> east.
	colPos := (lonDeg - lonMin) / lonStep

	row, fy := cellPosition(rowPos, numRows)
	col, fx := cellPosition(colPos, numCols)

	nw := m.at(row, col)
	ne := m.at(row, col+1)
	sw := m.at(row+1, col)
	se := m.at(row+1, col+1)

	return bilinear(
		nw, ne,
		sw, se,
		fx, fy,
	), nil
}

// at returns the grid value at row, col.
//
// No bounds check is performed here because Height calculates
// valid indices before calling this method.
func (m *Model) at(row, col int) float64 {
	index := row*numCols + col
	offset := index * bytesPerValue

	bits := binary.LittleEndian.Uint32(
		m.data[offset : offset+bytesPerValue],
	)

	return float64(math.Float32frombits(bits))
}

// cellPosition converts a continuous grid position into
// the index of its containing cell and a normalized fraction [0, 1].
//
// At the outer boundary, the last valid cell is selected and
// fraction=1 is returned so that interpolation reaches the final
// grid point.
func cellPosition(pos float64, count int) (index int, fraction float64) {
	if pos <= 0 {
		return 0, 0
	}

	last := float64(count - 1)

	if pos >= last {
		return count - 2, 1
	}

	index = int(math.Floor(pos))

	return index, pos - float64(index)
}

func bilinear(
	nw, ne,
	sw, se,
	fx, fy float64,
) float64 {
	north := nw + fx*(ne-nw)
	south := sw + fx*(se-sw)

	return north + fy*(south-north)
}

func isFinite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}
