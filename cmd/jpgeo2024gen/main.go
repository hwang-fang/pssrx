package main

import (
	"bufio"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

const (
	modelName = "JPGEO2024"

	numRows = 2101
	numCols = 1601

	latMin  = 15.0
	latMax  = 50.0
	lonMin  = 120.0
	lonMax  = 160.0
	latStep = 1.0 / 60.0
	lonStep = 1.5 / 60.0

	noData = -9999.0

	bytesPerValue   = 4
	expectedValues  = numRows * numCols
	expectedBinSize = expectedValues * bytesPerValue
)

type header map[string]string

func main() {
	var (
		inputPath  string
		outputPath string
	)

	flag.StringVar(
		&inputPath,
		"in",
		"JPGEO2024.isg",
		"input JPGEO2024.isg path",
	)
	flag.StringVar(
		&outputPath,
		"out",
		"internal/geodesy/geoid/data/jpgeo2024.bin",
		"output binary path",
	)
	flag.Parse()

	if err := run(inputPath, outputPath); err != nil {
		fmt.Fprintf(os.Stderr, "jpgeo2024gen: %v\n", err)
		os.Exit(1)
	}
}

func run(inputPath, outputPath string) error {
	in, err := os.Open(inputPath)
	if err != nil {
		return fmt.Errorf("open input %q: %w", inputPath, err)
	}
	defer in.Close()

	outputDir := filepath.Dir(outputPath)

	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}

	// Write to a temporary file first so that a failed conversion
	// never leaves a partially generated jpgeo2024.bin.
	tmp, err := os.CreateTemp(outputDir, ".jpgeo2024-*.bin")
	if err != nil {
		return fmt.Errorf("create temporary output: %w", err)
	}

	tmpPath := tmp.Name()
	committed := false

	defer func() {
		_ = tmp.Close()

		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()

	writer := bufio.NewWriterSize(tmp, 1024*1024)

	stats, err := convert(in, writer)
	if err != nil {
		return err
	}

	if err := writer.Flush(); err != nil {
		return fmt.Errorf("flush output: %w", err)
	}

	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync output: %w", err)
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close output: %w", err)
	}

	info, err := os.Stat(tmpPath)
	if err != nil {
		return fmt.Errorf("stat temporary output: %w", err)
	}

	if info.Size() != int64(expectedBinSize) {
		return fmt.Errorf(
			"unexpected binary size: got %d bytes, want %d",
			info.Size(),
			expectedBinSize,
		)
	}

	if err := replaceFile(tmpPath, outputPath); err != nil {
		return err
	}

	committed = true

	fmt.Printf("converted %s -> %s\n", inputPath, outputPath)
	fmt.Printf(
		"grid: %d x %d (%d values)\n",
		numRows,
		numCols,
		expectedValues,
	)
	fmt.Printf(
		"binary size: %d bytes (%.2f MiB)\n",
		expectedBinSize,
		float64(expectedBinSize)/(1024*1024),
	)
	fmt.Printf(
		"geoid height range: %.4f .. %.4f m\n",
		stats.min,
		stats.max,
	)

	return nil
}

type convertStats struct {
	min float64
	max float64
}

func convert(r io.Reader, w io.Writer) (convertStats, error) {
	scanner := bufio.NewScanner(r)

	// A JPGEO2024 row contains 1601 ASCII values.
	// The default Scanner limit is sufficient today, but make the
	// intended upper bound explicit.
	scanner.Buffer(
		make([]byte, 64*1024),
		1024*1024,
	)

	h, err := readHeader(scanner)
	if err != nil {
		return convertStats{}, err
	}

	if err := validateHeader(h); err != nil {
		return convertStats{}, err
	}

	stats := convertStats{
		min: math.Inf(1),
		max: math.Inf(-1),
	}

	row := 0

	var buf [4]byte

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		if row >= numRows {
			return convertStats{}, fmt.Errorf(
				"too many data rows: got more than %d",
				numRows,
			)
		}

		fields := strings.Fields(line)

		if len(fields) != numCols {
			return convertStats{}, fmt.Errorf(
				"invalid column count at data row %d: got %d, want %d",
				row+1,
				len(fields),
				numCols,
			)
		}

		for col, field := range fields {
			v, err := strconv.ParseFloat(field, 64)
			if err != nil {
				return convertStats{}, fmt.Errorf(
					"invalid value at row=%d col=%d: %q: %w",
					row+1,
					col+1,
					field,
					err,
				)
			}

			if math.IsNaN(v) || math.IsInf(v, 0) {
				return convertStats{}, fmt.Errorf(
					"non-finite value at row=%d col=%d: %q",
					row+1,
					col+1,
					field,
				)
			}

			// JPGEO2024 contains geoid heights throughout
			// its published area, so nodata is unexpected here.
			if v == noData {
				return convertStats{}, fmt.Errorf(
					"unexpected nodata at row=%d col=%d",
					row+1,
					col+1,
				)
			}

			if v < stats.min {
				stats.min = v
			}

			if v > stats.max {
				stats.max = v
			}

			f32 := float32(v)

			if math.IsInf(float64(f32), 0) {
				return convertStats{}, fmt.Errorf(
					"value cannot be represented as float32 "+
						"at row=%d col=%d: %q",
					row+1,
					col+1,
					field,
				)
			}

			binary.LittleEndian.PutUint32(
				buf[:],
				math.Float32bits(f32),
			)

			if _, err := w.Write(buf[:]); err != nil {
				return convertStats{}, fmt.Errorf(
					"write row=%d col=%d: %w",
					row+1,
					col+1,
					err,
				)
			}
		}

		row++
	}

	if err := scanner.Err(); err != nil {
		return convertStats{}, fmt.Errorf(
			"read input: %w",
			err,
		)
	}

	if row != numRows {
		return convertStats{}, fmt.Errorf(
			"invalid data row count: got %d, want %d",
			row,
			numRows,
		)
	}

	return stats, nil
}

func readHeader(scanner *bufio.Scanner) (header, error) {
	h := make(header)

	inHeader := false

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		if line == "" {
			continue
		}

		if !inHeader {
			// Lines before begin_of_head are comments according
			// to the JPGEO2024/ISG specification.
			if strings.HasPrefix(line, "begin_of_head") {
				inHeader = true
			}

			continue
		}

		if strings.HasPrefix(line, "end_of_head") {
			return h, nil
		}

		key, value, ok := splitHeaderField(line)
		if !ok {
			return nil, fmt.Errorf(
				"invalid header line: %q",
				line,
			)
		}

		key = normalizeKey(key)

		if _, exists := h[key]; exists {
			return nil, fmt.Errorf(
				"duplicate header field %q",
				key,
			)
		}

		h[key] = strings.TrimSpace(value)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf(
			"read header: %w",
			err,
		)
	}

	if !inHeader {
		return nil, errors.New(
			"begin_of_head not found",
		)
	}

	return nil, errors.New(
		"end_of_head not found",
	)
}

func validateHeader(h header) error {
	checks := []struct {
		key  string
		want string
	}{
		{"model name", modelName},
		{"model year", "2024"},
		{"model type", "gravimetric"},
		{"data type", "geoid"},
		{"data units", "meters"},
		{"data format", "grid"},
		{"data ordering", "N-to-S, W-to-E"},
		{"ref ellipsoid", "GRS80"},
		{"coord type", "geodetic"},
		{"coord units", "dms"},
		{"nrows", strconv.Itoa(numRows)},
		{"ncols", strconv.Itoa(numCols)},
		{"isg format", "2.0"},
	}

	for _, check := range checks {
		if err := requireEqual(
			h,
			check.key,
			check.want,
		); err != nil {
			return err
		}
	}

	if err := requireFloat(
		h,
		"nodata",
		noData,
		0,
	); err != nil {
		return err
	}

	if err := requireDMS(
		h,
		"lat min",
		latMin,
	); err != nil {
		return err
	}

	if err := requireDMS(
		h,
		"lat max",
		latMax,
	); err != nil {
		return err
	}

	if err := requireDMS(
		h,
		"lon min",
		lonMin,
	); err != nil {
		return err
	}

	if err := requireDMS(
		h,
		"lon max",
		lonMax,
	); err != nil {
		return err
	}

	if err := requireDMS(
		h,
		"delta lat",
		latStep,
	); err != nil {
		return err
	}

	if err := requireDMS(
		h,
		"delta lon",
		lonStep,
	); err != nil {
		return err
	}

	return nil
}

func splitHeaderField(
	line string,
) (key, value string, ok bool) {
	colon := strings.IndexByte(line, ':')
	equal := strings.IndexByte(line, '=')

	var sep int

	switch {
	case colon < 0 && equal < 0:
		return "", "", false

	case colon < 0:
		sep = equal

	case equal < 0:
		sep = colon

	case colon < equal:
		sep = colon

	default:
		sep = equal
	}

	key = strings.TrimSpace(line[:sep])
	value = strings.TrimSpace(line[sep+1:])

	return key, value, key != "" && value != ""
}

func normalizeKey(s string) string {
	return strings.ToLower(
		strings.Join(strings.Fields(s), " "),
	)
}

func requireEqual(
	h header,
	key,
	want string,
) error {
	got, ok := h[key]
	if !ok {
		return fmt.Errorf(
			"missing header field %q",
			key,
		)
	}

	if got != want {
		return fmt.Errorf(
			"unexpected %s: got %q, want %q",
			key,
			got,
			want,
		)
	}

	return nil
}

func requireFloat(
	h header,
	key string,
	want,
	tolerance float64,
) error {
	s, ok := h[key]
	if !ok {
		return fmt.Errorf(
			"missing header field %q",
			key,
		)
	}

	got, err := strconv.ParseFloat(
		strings.TrimSpace(s),
		64,
	)
	if err != nil {
		return fmt.Errorf(
			"invalid %s %q: %w",
			key,
			s,
			err,
		)
	}

	if math.Abs(got-want) > tolerance {
		return fmt.Errorf(
			"unexpected %s: got %v, want %v",
			key,
			got,
			want,
		)
	}

	return nil
}

func requireDMS(
	h header,
	key string,
	want float64,
) error {
	s, ok := h[key]
	if !ok {
		return fmt.Errorf(
			"missing header field %q",
			key,
		)
	}

	got, err := parseDMS(s)
	if err != nil {
		return fmt.Errorf(
			"invalid %s %q: %w",
			key,
			s,
			err,
		)
	}

	const tolerance = 1e-12

	if math.Abs(got-want) > tolerance {
		return fmt.Errorf(
			"unexpected %s: got %.15g, want %.15g",
			key,
			got,
			want,
		)
	}

	return nil
}

func parseDMS(s string) (float64, error) {
	s = strings.TrimSpace(s)

	if s == "" {
		return 0, errors.New("empty DMS value")
	}

	sign := 1.0

	switch s[0] {
	case '-':
		sign = -1
		s = strings.TrimSpace(s[1:])
	case '+':
		s = strings.TrimSpace(s[1:])
	}

	degEnd := strings.Index(s, "°")
	if degEnd < 0 {
		return 0, errors.New(
			"missing degree symbol",
		)
	}

	minuteEnd := strings.Index(
		s[degEnd+len("°"):],
		"'",
	)
	if minuteEnd < 0 {
		return 0, errors.New(
			"missing minute symbol",
		)
	}
	minuteEnd += degEnd + len("°")

	secondEnd := strings.Index(
		s[minuteEnd+1:],
		`"`,
	)
	if secondEnd < 0 {
		return 0, errors.New(
			"missing second symbol",
		)
	}
	secondEnd += minuteEnd + 1

	deg, err := strconv.ParseFloat(
		strings.TrimSpace(s[:degEnd]),
		64,
	)
	if err != nil {
		return 0, fmt.Errorf(
			"degree: %w",
			err,
		)
	}

	minute, err := strconv.ParseFloat(
		strings.TrimSpace(
			s[degEnd+len("°"):minuteEnd],
		),
		64,
	)
	if err != nil {
		return 0, fmt.Errorf(
			"minute: %w",
			err,
		)
	}

	second, err := strconv.ParseFloat(
		strings.TrimSpace(
			s[minuteEnd+1:secondEnd],
		),
		64,
	)
	if err != nil {
		return 0, fmt.Errorf(
			"second: %w",
			err,
		)
	}

	if minute < 0 || minute >= 60 {
		return 0, fmt.Errorf(
			"minute out of range: %v",
			minute,
		)
	}

	if second < 0 || second >= 60 {
		return 0, fmt.Errorf(
			"second out of range: %v",
			second,
		)
	}

	return sign * (deg +
		minute/60.0 +
		second/3600.0), nil
}

func replaceFile(
	tmpPath,
	outputPath string,
) error {
	// On Unix, rename replaces an existing regular file.
	if runtime.GOOS != "windows" {
		if err := os.Rename(
			tmpPath,
			outputPath,
		); err != nil {
			return fmt.Errorf(
				"replace output %q: %w",
				outputPath,
				err,
			)
		}

		return nil
	}

	// os.Rename does not replace an existing destination
	// reliably on Windows.
	if err := os.Remove(outputPath); err != nil &&
		!errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf(
			"remove existing output %q: %w",
			outputPath,
			err,
		)
	}

	if err := os.Rename(
		tmpPath,
		outputPath,
	); err != nil {
		return fmt.Errorf(
			"replace output %q: %w",
			outputPath,
			err,
		)
	}

	return nil
}
