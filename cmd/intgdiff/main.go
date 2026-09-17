// Command intgdiff は 2 つの intg ディレクトリをレコード単位で突き合わせる。
//
// バイト一致しなかったときに「何が何レコード、どれだけずれたか」を
// 即座に出すためのもの。bytes.Equal の false だけでは先に進めない。
package main

import (
	"flag"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"pssrx/internal/archive"
	"pssrx/internal/record"
)

// azimuthLSB は intg の方位角 1 LSB に相当する角度 [rad]。
const azimuthLSB = 2 * math.Pi / 0xFFFFFFFF

func main() {
	maxShow := flag.Int("show", 5, "食い違ったレコードを表示する最大件数")
	flag.Parse()
	if flag.NArg() != 2 {
		fmt.Fprintln(os.Stderr, "usage: intgdiff [-show N] <dirA> <dirB>")
		os.Exit(2)
	}
	if err := run(flag.Arg(0), flag.Arg(1), *maxShow); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

type diffStats struct {
	filesBoth, filesOnlyA, filesOnlyB int
	filesIdentical, filesDiffer       int
	recordsA, recordsB                int
	onlyA, onlyB                      int // 片方にしか無いタイムスタンプ
	sameTsDiffer                      int // 同じタイムスタンプで内容が違う
	azDiffLSB                         map[int64]int
	modeDiffer                        int
}

func run(dirA, dirB string, maxShow int) error {
	files := map[string]bool{}
	for _, d := range []string{dirA, dirB} {
		err := filepath.WalkDir(d, func(p string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !e.IsDir() && strings.HasSuffix(p, ".intg") {
				rel, _ := filepath.Rel(d, p)
				files[rel] = true
			}
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	rels := make([]string, 0, len(files))
	for r := range files {
		rels = append(rels, r)
	}
	sort.Strings(rels)

	st := diffStats{azDiffLSB: map[int64]int{}}
	shown := 0

	for _, rel := range rels {
		base, err := baseTimeFromName(rel)
		if err != nil {
			return err
		}
		a, errA := archive.ReadIntg(filepath.Join(dirA, rel), base)
		b, errB := archive.ReadIntg(filepath.Join(dirB, rel), base)
		switch {
		case errA != nil && errB == nil:
			st.filesOnlyB++
			st.recordsB += len(b)
			continue
		case errB != nil && errA == nil:
			st.filesOnlyA++
			st.recordsA += len(a)
			continue
		case errA != nil && errB != nil:
			return fmt.Errorf("%s: 双方で読めません: %v / %v", rel, errA, errB)
		}
		st.filesBoth++
		st.recordsA += len(a)
		st.recordsB += len(b)

		if sameRecords(a, b) {
			st.filesIdentical++
			continue
		}
		st.filesDiffer++

		ma := indexByTs(a)
		mb := indexByTs(b)
		for ts, ra := range ma {
			rb, ok := mb[ts]
			if !ok {
				st.onlyA++
				continue
			}
			if ra.Mode != rb.Mode {
				st.modeDiffer++
			}
			// 方位角は u32 に量子化されているので LSB 単位の差で見る
			d := int64(math.Round((ra.Azimuth - rb.Azimuth) / azimuthLSB))
			if d != 0 || ra.Mode != rb.Mode {
				st.sameTsDiffer++
				st.azDiffLSB[d]++
				if shown < maxShow {
					shown++
					fmt.Printf("  %s ts=%d (%s)\n    A: az=%.12f mode=%d\n    B: az=%.12f mode=%d  (差 %d LSB)\n",
						rel, ts, record.ToTime(ts).Format("15:04:05.000000000"),
						ra.Azimuth, ra.Mode, rb.Azimuth, rb.Mode, d)
				}
			}
		}
		for ts := range mb {
			if _, ok := ma[ts]; !ok {
				st.onlyB++
			}
		}
	}

	report(dirA, dirB, st)
	if st.filesDiffer > 0 || st.filesOnlyA > 0 || st.filesOnlyB > 0 {
		os.Exit(1)
	}
	return nil
}

func report(dirA, dirB string, st diffStats) {
	fmt.Printf("\nA = %s\nB = %s\n\n", dirA, dirB)
	fmt.Printf("ファイル  双方 %d (一致 %d / 相違 %d), A のみ %d, B のみ %d\n",
		st.filesBoth, st.filesIdentical, st.filesDiffer, st.filesOnlyA, st.filesOnlyB)
	fmt.Printf("レコード  A %d, B %d\n", st.recordsA, st.recordsB)
	fmt.Printf("          A のみに存在 %d, B のみに存在 %d\n", st.onlyA, st.onlyB)
	fmt.Printf("          同一時刻で内容相違 %d (うち mode 相違 %d)\n", st.sameTsDiffer, st.modeDiffer)

	if len(st.azDiffLSB) > 0 {
		keys := make([]int64, 0, len(st.azDiffLSB))
		for k := range st.azDiffLSB {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return st.azDiffLSB[keys[i]] > st.azDiffLSB[keys[j]] })
		fmt.Printf("\n方位角の差 (LSB = %.3e rad) 上位:\n", azimuthLSB)
		for i, k := range keys {
			if i >= 10 {
				fmt.Printf("  ... 他 %d 種\n", len(keys)-10)
				break
			}
			fmt.Printf("  %+d LSB : %d 件\n", k, st.azDiffLSB[k])
		}
	}
	if st.filesDiffer == 0 && st.filesOnlyA == 0 && st.filesOnlyB == 0 {
		fmt.Printf("\n★ 全ファイルがバイト一致\n")
	}
}

func sameRecords(a, b []record.Interrogation) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func indexByTs(r []record.Interrogation) map[int64]record.Interrogation {
	m := make(map[int64]record.Interrogation, len(r))
	for _, v := range r {
		m[v.Timestamp] = v
	}
	return m
}

// baseTimeFromName はファイル名 YYYYMMDDHHMM<id>.intg から分の先頭時刻を取る。
func baseTimeFromName(rel string) (int64, error) {
	name := filepath.Base(rel)
	if len(name) < 12 {
		return 0, fmt.Errorf("ファイル名から時刻を取れません: %s", rel)
	}
	t, err := time.ParseInLocation("200601021504", name[:12], record.JST)
	if err != nil {
		return 0, fmt.Errorf("ファイル名から時刻を取れません: %s: %w", rel, err)
	}
	return t.UnixNano(), nil
}
