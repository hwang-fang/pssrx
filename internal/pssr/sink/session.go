package sink

import (
	"cmp"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"pssrx/internal/pssr/tracking"
)

// SessionGapNs はスコークセッションを分ける切れ目 [ns]（600 s）。同じスコークでも
// 前の点からこれ以上離れた点は別のセッション（別のファイル）にする。
const SessionGapNs = 600_000_000_000

// SessionStore はスコークセッションごとに CSV を書く出力先。ディレクトリは
// 処理をまたいで使い続ける保管庫で、期間 [From, To) の処理は既存のファイルと
// 次の規則で統合する。
//
//	同じスコークで [From − 600 s, To + 600 s) に掛かる既存のファイルを読み、
//	ほかの処理が出した [From, To) の点をこの処理の点で置き換える。残りの点と
//	合わせて 600 s の規則で切り直して書き直し、元のファイルを消す。
//
// 点数が MinPoints に満たないセッションは書かない。下限は処理ごとに当てるので、
// 期間の境目で分かれた小さなセッションは隣の期間とつながる前に落ちる。境目の
// 小さなセッションを残しておき隣の処理で統合する案は、処理の順序で結果が
// 揺らぐので採らない。それが問題になる用途では MinPoints を 1 にする。
//
// この 1 つの規則で、前の期間のファイルへの続き書き、後ろの期間のファイルへの
// 前付け、同じ期間の処理し直し（置き換え）がすべて扱える。点数の少ない
// セッション（FRUIT の偶然の一致によるでたらめなスコークなど）は書かない。
// 点に判定は付けず、ファイルの詰め方だけを決める。
//
// ファイルは {Dir}/{開始時刻}_{終了時刻}_{スコーク}.csv（時刻は UTC の秒、
// 例 20260610T010001Z_20260610T011532Z_2216.csv）。1 セッション分の点を
// メモリに溜め、受け取った点の最新の時刻がそのセッションの最後の点から
// 600 s を過ぎたら統合して書く。Close で残りを書き、処理の記録
// {Dir}/runs/{処理の ID}.json を書く。同じディレクトリへの同時実行はしない前提。
type SessionStore struct {
	dir       string
	ctx       Context
	from, to  int64 // 処理の期間 [from, to) [ns]
	minPoints int
	meta      map[string]any

	pending   map[string][]row // スコーク → 書き出し待ちの行（この処理の点）
	last      map[string]int64 // スコーク → 書き出し待ちの最後の点の時刻
	watermark int64

	files    map[string]fileInfo // ディレクトリにあるセッションのファイル（名前 → 範囲）
	existing map[string]bool     // 処理の開始時にあったファイル
	written  map[string]int      // この処理で書いたファイル → この処理の点の数
}

// row は CSV の 1 行と、統合に要る項目。
type row struct {
	t      int64
	squawk string
	run    string
	fields []string
}

// fileInfo はセッションのファイル名から読める範囲。時刻は秒に切り捨ててある。
type fileInfo struct {
	squawk     string
	start, end int64
}

// NewSessionStore は dir を保管庫として開く。dir が無ければ作り、既存の
// セッションのファイルを一覧にする。meta は処理の記録に書く項目（版、設定、
// ソース構成など）で、SessionStore が書いたファイルの一覧を足す。
func NewSessionStore(dir string, ctx Context, from, to int64, minPoints int, meta map[string]any) (*SessionStore, error) {
	if minPoints < 1 {
		return nil, fmt.Errorf("セッションの点数の下限が不正: %d", minPoints)
	}
	if err := os.MkdirAll(filepath.Join(dir, "runs"), 0o755); err != nil {
		return nil, err
	}
	s := &SessionStore{
		dir: dir, ctx: ctx, from: from, to: to, minPoints: minPoints, meta: meta,
		pending: map[string][]row{}, last: map[string]int64{},
		files: map[string]fileInfo{}, existing: map[string]bool{}, written: map[string]int{},
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range ents {
		if fi, ok := parseSessionName(e.Name()); ok && !e.IsDir() {
			s.files[e.Name()], s.existing[e.Name()] = fi, true
		}
	}
	return s, nil
}

// Write は点をスコークごとに溜め、切れ目を過ぎたセッションを書く。棄却した点
// （Drop のある点）は書かない。
func (s *SessionStore) Write(fixes []tracking.Fix) error {
	for _, f := range fixes {
		if f.Drop != "" {
			continue
		}
		r := row{t: f.Timestamp, squawk: fmt.Sprintf("%04o", f.Squawk), run: s.ctx.Run, fields: csvRow(s.ctx, f)}
		if last, ok := s.last[r.squawk]; ok && r.t-last >= SessionGapNs {
			if err := s.flush(r.squawk); err != nil {
				return err
			}
		}
		s.pending[r.squawk] = append(s.pending[r.squawk], r)
		s.last[r.squawk] = max(s.last[r.squawk], r.t)
		s.watermark = max(s.watermark, r.t)
	}
	for _, sq := range sortedKeys(s.pending) {
		if s.watermark-s.last[sq] >= SessionGapNs {
			if err := s.flush(sq); err != nil {
				return err
			}
		}
	}
	return nil
}

// Close は残りのセッションを書き、この処理の点が無かったスコークの既存の
// ファイルからも [From, To) のほかの処理の点を除き、処理の記録を書く。
func (s *SessionStore) Close() error {
	for _, sq := range sortedKeys(s.pending) {
		if err := s.flush(sq); err != nil {
			return err
		}
	}
	// この処理が触れなかったファイルのうち、期間に掛かるものを掃除する
	var stale []string
	for _, name := range sortedKeys(s.files) {
		fi := s.files[name]
		if _, mine := s.written[name]; !mine && fi.end+1e9 > s.from && fi.start < s.to {
			stale = append(stale, name)
		}
	}
	for _, name := range stale {
		if _, ok := s.files[name]; !ok {
			continue // 先の掃除で一緒に書き直した
		}
		if err := s.merge(s.files[name].squawk, nil, s.files[name].start, s.files[name].end+1e9); err != nil {
			return err
		}
	}
	return s.writeRecord()
}

// flush はスコーク sq の書き出し待ちの行を既存のファイルと統合して書く。
func (s *SessionStore) flush(sq string) error {
	rows := s.pending[sq]
	delete(s.pending, sq)
	delete(s.last, sq)
	if len(rows) == 0 {
		return nil
	}
	return s.merge(sq, rows, rows[0].t, rows[len(rows)-1].t)
}

// merge は、スコーク sq で [lo − 600 s, hi + 600 s) に掛かるファイルを読み、
// ほかの処理の [from, to) の行を除いて rows と合わせ、600 s の規則で切り直して
// 書き直す。読んだファイルは消す（同じ名前で書き直すものは上書き）。
func (s *SessionStore) merge(sq string, rows []row, lo, hi int64) error {
	var olds []string
	for _, name := range sortedKeys(s.files) {
		fi := s.files[name]
		if fi.squawk == sq && fi.end+1e9 > lo-SessionGapNs && fi.start < hi+SessionGapNs {
			olds = append(olds, name)
		}
	}
	all := slices.Clone(rows)
	for _, name := range olds {
		old, err := s.readRows(name)
		if err != nil {
			return err
		}
		for _, r := range old {
			if r.run != s.ctx.Run && r.t >= s.from && r.t < s.to {
				continue // この処理が置き換える
			}
			all = append(all, r)
		}
	}
	slices.SortStableFunc(all, func(a, b row) int { return cmp.Compare(a.t, b.t) })

	// 600 s の規則で切り直し、点数の足りたセッションだけを書く
	keep := map[string]bool{}
	for i := 0; i < len(all); {
		j := i + 1
		for j < len(all) && all[j].t-all[j-1].t < SessionGapNs {
			j++
		}
		if seg := all[i:j]; len(seg) >= s.minPoints {
			name, err := s.writeSession(sq, seg)
			if err != nil {
				return err
			}
			keep[name] = true
		}
		i = j
	}
	for _, name := range olds {
		if keep[name] {
			continue
		}
		if err := os.Remove(filepath.Join(s.dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		delete(s.files, name)
		delete(s.written, name)
	}
	return nil
}

// writeSession は 1 セッションの行を時刻順に書き、ファイル名を返す。一時
// ファイルに書いてから名前を変えるので、途中で止まっても半端なファイルは残らない。
func (s *SessionStore) writeSession(sq string, seg []row) (string, error) {
	name := sessionName(sq, seg[0].t, seg[len(seg)-1].t)
	tmp := filepath.Join(s.dir, name+".tmp")
	f, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	w := csv.NewWriter(f)
	err = w.Write(csvHeader)
	mine := 0
	for _, r := range seg {
		if err != nil {
			break
		}
		err = w.Write(r.fields)
		if r.run == s.ctx.Run {
			mine++
		}
	}
	w.Flush()
	err = errors.Join(err, w.Error(), f.Close())
	if err != nil {
		os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, filepath.Join(s.dir, name)); err != nil {
		return "", err
	}
	s.files[name] = fileInfo{squawk: sq, start: truncSec(seg[0].t), end: truncSec(seg[len(seg)-1].t)}
	s.written[name] = mine
	return name, nil
}

// readRows はセッションのファイルを読む。列が今の版と違うファイルは統合できない
// のでエラーにする。
func (s *SessionStore) readRows(name string) ([]row, error) {
	f, err := os.Open(filepath.Join(s.dir, name))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	header, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if !slices.Equal(header, csvHeader) {
		return nil, fmt.Errorf("%s: 列が今の版と違うので統合できない（別の出力先を使うか、ファイルを移す）", name)
	}
	cT, cSq, cRun := slices.Index(csvHeader, "time_utc"), slices.Index(csvHeader, "squawk"), slices.Index(csvHeader, "run")
	var out []row
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		t, err := time.Parse(timeLayout, rec[cT])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		out = append(out, row{t: t.UnixNano(), squawk: rec[cSq], run: rec[cRun], fields: rec})
	}
	return out, nil
}

// writeRecord は処理の記録 {Dir}/runs/{Run}.json を書く。
func (s *SessionStore) writeRecord() error {
	type written struct {
		Name      string `json:"name"`
		Squawk    string `json:"squawk"`
		Start     string `json:"start"`
		End       string `json:"end"`
		RunPoints int    `json:"run_points"` // そのファイルのうち、この処理が出した点の数
	}
	var ws []written
	for _, name := range sortedKeys(s.written) {
		fi := s.files[name]
		ws = append(ws, written{Name: name, Squawk: fi.squawk,
			Start: utc(fi.start).Format(time.RFC3339), End: utc(fi.end).Format(time.RFC3339), RunPoints: s.written[name]})
	}
	removed := []string{}
	for _, name := range sortedKeys(s.existing) {
		if _, ok := s.files[name]; !ok {
			removed = append(removed, name)
		}
	}
	rec := map[string]any{}
	for k, v := range s.meta {
		rec[k] = v
	}
	rec["run"] = s.ctx.Run
	rec["source"] = s.ctx.Source
	rec["period"] = map[string]string{"from": utc(s.from).Format(time.RFC3339Nano), "to": utc(s.to).Format(time.RFC3339Nano)}
	rec["min_session_points"] = s.minPoints
	rec["session_gap_s"] = SessionGapNs / 1e9
	rec["files_written"] = ws
	rec["files_removed"] = removed
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.dir, "runs", s.ctx.Run+".json"), append(b, '\n'), 0o644)
}

const sessionTimeLayout = "20060102T150405Z"

func sessionName(sq string, start, end int64) string {
	return utc(start).Format(sessionTimeLayout) + "_" + utc(end).Format(sessionTimeLayout) + "_" + sq + ".csv"
}

// parseSessionName はセッションのファイル名から範囲を読む。
func parseSessionName(name string) (fileInfo, bool) {
	base, ok := strings.CutSuffix(name, ".csv")
	parts := strings.Split(base, "_")
	if !ok || len(parts) != 3 || len(parts[2]) != 4 {
		return fileInfo{}, false
	}
	a, err1 := time.Parse(sessionTimeLayout, parts[0])
	b, err2 := time.Parse(sessionTimeLayout, parts[1])
	if err1 != nil || err2 != nil {
		return fileInfo{}, false
	}
	return fileInfo{squawk: parts[2], start: a.UnixNano(), end: b.UnixNano()}, true
}

func truncSec(ns int64) int64 { return ns - ns%1_000_000_000 }

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// SourceDir はソース構成の ID をディレクトリ名にする（":" を "_" に）。
func SourceDir(source string) string { return strings.ReplaceAll(source, ":", "_") }
