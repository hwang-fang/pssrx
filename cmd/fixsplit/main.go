// Command fixsplit は pssrx が書いた位置の CSV を便（track 列）ごとの
// ファイルに分ける。連続性の判定を便ごとに眺める検証用で、パイプラインの
// 一部ではない。
//
//	fixsplit -in fixes.csv -out dir [-status ok|unconfirmed|all] [-min N]
//
// 出力は {out}/{track}.csv で、ヘッダは入力と同じ。-min は点数がそれ未満の
// 便を書かない（既定 1 = 全部）。-status で判定を絞る（既定 all）。
// 終わりに便ごとの要約（点数、期間、スコーク、判定）を標準出力に出す。
package main

import (
	"encoding/csv"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
)

func main() {
	in := flag.String("in", "", "pssrx が書いた位置の CSV (必須)")
	out := flag.String("out", "", "便ごとの CSV を書くディレクトリ (必須)")
	status := flag.String("status", "all", "書く判定: ok, unconfirmed, all")
	minPoints := flag.Int("min", 1, "この点数未満の便は書かない")
	flag.Parse()
	if *in == "" || *out == "" {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(*in, *out, *status, *minPoints); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

type summary struct {
	track      string
	squawk     string
	status     string
	first, end string
	points     int
}

func run(in, out, status string, minPoints int) error {
	f, err := os.Open(in)
	if err != nil {
		return err
	}
	defer f.Close()
	r := csv.NewReader(f)
	header, err := r.Read()
	if err != nil {
		return fmt.Errorf("ヘッダ: %w", err)
	}
	col := func(name string) (int, error) {
		if i := slices.Index(header, name); i >= 0 {
			return i, nil
		}
		return 0, fmt.Errorf("列 %q が無い（pssrx の CSV か確認）", name)
	}
	cTrack, err := col("track")
	if err != nil {
		return err
	}
	cStatus, err := col("status")
	if err != nil {
		return err
	}
	cSquawk, err := col("squawk")
	if err != nil {
		return err
	}
	cTime, err := col("time_jst")
	if err != nil {
		return err
	}

	rows := map[string][][]string{}
	var order []string
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if status != "all" && rec[cStatus] != status {
			continue
		}
		k := rec[cTrack]
		if _, seen := rows[k]; !seen {
			order = append(order, k)
		}
		rows[k] = append(rows[k], rec)
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	var sums []summary
	for _, k := range order {
		recs := rows[k]
		if len(recs) < minPoints {
			continue
		}
		if err := writeTrack(filepath.Join(out, k+".csv"), header, recs); err != nil {
			return err
		}
		sums = append(sums, summary{
			track: k, squawk: recs[0][cSquawk], status: recs[0][cStatus],
			first: recs[0][cTime], end: recs[len(recs)-1][cTime], points: len(recs),
		})
	}
	slices.SortFunc(sums, func(a, b summary) int {
		x, _ := strconv.ParseInt(a.track, 10, 64)
		y, _ := strconv.ParseInt(b.track, 10, 64)
		return int(x - y)
	})
	fmt.Printf("%-8s %-6s %-12s %-6s %s .. %s\n", "track", "squawk", "status", "points", "first", "last")
	for _, s := range sums {
		fmt.Printf("%-8s %-6s %-12s %-6d %s .. %s\n", s.track, s.squawk, s.status, s.points, s.first[11:23], s.end[11:23])
	}
	fmt.Printf("%d 便を %s に書いた\n", len(sums), out)
	return nil
}

func writeTrack(path string, header []string, recs [][]string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	w := csv.NewWriter(f)
	if err := w.Write(header); err != nil {
		f.Close()
		return err
	}
	if err := w.WriteAll(recs); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
