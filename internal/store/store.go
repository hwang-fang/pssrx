// Package store は qpkx（受信した質問データ）の読み込みと
// intg（質問予定表）の書き出しを扱う。
//
// どちらも 1 分 1 ファイルで、レコードはリトルエンディアンの固定長。
// パディングは無い。タイムスタンプはファイルが受け持つ分の先頭からの
// 相対値で、100 ns 単位。
//
//	qpkx: u4 タイムスタンプ(分内 100ns 単位) + u1 質問種別 + u2 波高値  = 7 byte
//	intg: u4 タイムスタンプ(分内 100ns 単位) + u1 質問種別 + u4 方位角  = 9 byte
package store

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"slices"
	"time"

	"pssrx/internal/nanotime"
)

const (
	// OneMinute は 1 分のナノ秒。ファイルの区切り単位。
	OneMinute = int64(60_000_000_000)
	// tsResolution はファイル上のタイムスタンプの分解能 [ns]。
	tsResolution = int64(100)

	qpkxRecordSize = 7
	intgRecordSize = 9
)

// QData は受信した質問データ 1 件。
type QData struct {
	Timestamp int64 // Unix ナノ秒（受信時刻）
	WH        uint16
	Mode      uint8
}

// Intg は質問予定表のレコード 1 件。
type Intg struct {
	Timestamp int64   // Unix ナノ秒（SSR の送信時刻）
	Azimuth   float64 // [0, 2pi)
	Mode      uint8
}

// maxWaveheightDbm は波高値として表現できる最小値（-255 - 255/256）。
var minWaveheightDbm = -(255.0 + 255.0/256.0)

// EncodeWaveheight は dBm を波高値の生値へ変換する。
// 生値は 1/256 dB 刻みで、0 dBm が 0xFFFF、値が小さいほど弱い。
func EncodeWaveheight(dbm float64) (uint16, error) {
	if !(minWaveheightDbm <= dbm && dbm <= 0.0) {
		return 0, fmt.Errorf("波高値は %g ~ 0 の必要があります: %g", minWaveheightDbm, dbm)
	}
	// 0 方向へ切り捨てる。四捨五入すると 1 刻みずれた生値になる。
	return uint16(0xFFFF + int64(math.Trunc(dbm*256))), nil
}

// DecodeWaveheight は波高値の生値を dBm へ戻す。EncodeWaveheight の逆。
func DecodeWaveheight(v uint16) float64 { return -float64(0xFFFF-v) / 256 }

// QdataRepository は qpkx ファイル群から質問データを読む。
type QdataRepository struct {
	Root string
	// SortInput が true なら取得結果をタイムスタンプで安定ソートする。
	//
	// 解析側はデータが時刻昇順であることを前提にしている。セグメント分割は
	// 隣接レコードの時間差で切るし、連鎖検出の探索窓は二分探索で決めるので、
	// 逆行があるとどちらも意味を失う。
	//
	// ところが実データの qpkx は約 3 割のファイルで昇順になっていない。
	// ファイル先頭に前の分ぶんが数レコードこぼれている型と、ファイル途中で
	// 1〜4 秒巻き戻る型の 2 種類がある。そのため既定で整列する。
	// 整列前後で解析結果がどれだけ変わるかは NUMERICS.md を参照。
	SortInput bool
}

// Fetch は [start, end) の質問データを返す。start/end は Unix ナノ秒。
func (r *QdataRepository) Fetch(stationID string, start, end int64) ([]QData, error) {
	if end <= start {
		return nil, nil
	}
	var out []QData
	first := nanotime.ToTime(start).Truncate(time.Minute)
	last := nanotime.ToTime(end - 1).Truncate(time.Minute)
	for dt := first; !dt.After(last); dt = dt.Add(time.Minute) {
		path := r.filePath(stationID, dt)
		recs, err := readQpkx(path, dt.UnixNano())
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		out = append(out, recs...)
	}

	out = slices.DeleteFunc(out, func(q QData) bool {
		return q.Timestamp < start || q.Timestamp >= end
	})
	if r.SortInput {
		slices.SortStableFunc(out, func(a, b QData) int {
			switch {
			case a.Timestamp < b.Timestamp:
				return -1
			case a.Timestamp > b.Timestamp:
				return 1
			}
			return 0
		})
	}
	return out, nil
}

// filePath は qpkx のパスを組む。兄弟に apkx/ spkx/ があるため
// 日付の下にさらに qpkx/ の層が入る。
//
//	{root}/{YYYYMM}/{station}/{YYYYMMDD}/qpkx/{YYYYMMDDHHMM}{station}.qpkx
func (r *QdataRepository) filePath(stationID string, dt time.Time) string {
	return filepath.Join(r.Root,
		dt.Format("200601"), stationID, dt.Format("20060102"), "qpkx",
		dt.Format("200601021504")+stationID+".qpkx")
}

func readQpkx(path string, baseTime int64) ([]QData, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(raw)%qpkxRecordSize != 0 {
		return nil, fmt.Errorf("qpkx ファイルサイズ異常: %d byte は %d byte で割り切れません (%s)",
			len(raw), qpkxRecordSize, path)
	}
	out := make([]QData, len(raw)/qpkxRecordSize)
	for i := range out {
		b := raw[i*qpkxRecordSize:]
		out[i] = QData{
			Timestamp: baseTime + int64(binary.LittleEndian.Uint32(b[0:4]))*tsResolution,
			Mode:      b[4],
			WH:        binary.LittleEndian.Uint16(b[5:7]),
		}
	}
	return out, nil
}

// IntgRepository は質問予定表を intg ファイルへ書き出す。
//
// 時系列に沿って保存される前提で、1 分ぶんずつ書き足していく。
//
// 同じファイルに 2 度書くことがある。伝搬遅延の補正でレコードが前の分へ
// またがるためで、この場合は追記でなければ先に書いた内容が消える。
// 一方、同じ期間を流し直したときに追記してしまうとレコードが二重になる。
// 両立させるため、プロセス内で初めて到達した分にだけ切り詰めを行い、
// 同じ分への 2 度目以降は追記する。結果として、何度流し直しても
// 1 回流したときと同じファイルになる。
//
// 「初めて到達したか」は、SSR ごとに到達済みの最新の分だけを覚えて判定する。
// 書き込み先の分は時間とともに進む一方なので、書いたファイル名を全部
// 覚えておく必要は無い。常駐させても保持量は書き込んだ SSR の数で
// 頭打ちになる。
type IntgRepository struct {
	Root string
	// Append が true なら切り詰めを一切せず、常に追記する。
	// 別々に解析した期間を 1 つの出力へ継ぎ足したいときに使う。
	Append bool
	// Log が nil なら slog.Default() を使う。
	Log *slog.Logger

	// lastMinute は SSR ごとの、到達済みの最新の分（OneMinute 単位のキー）。
	lastMinute map[string]int64
}

// Save は intg レコードを 1 分区切りのファイルへ書き出す。
func (r *IntgRepository) Save(ssrID string, data []Intg) error {
	if len(data) == 0 {
		return nil
	}
	// タイムスタンプは Unix エポック以降なので、素の / と % で足りる。
	byMinute := map[int64][]Intg{}
	for _, d := range data {
		byMinute[d.Timestamp/OneMinute] = append(byMinute[d.Timestamp/OneMinute], d)
	}
	keys := make([]int64, 0, len(byMinute))
	for k := range byMinute {
		keys = append(keys, k)
	}
	slices.Sort(keys)

	last, seen := r.lastMinute[ssrID]
	for _, k := range keys {
		chunk := byMinute[k]
		slices.SortStableFunc(chunk, func(a, b Intg) int {
			switch {
			case a.Timestamp < b.Timestamp:
				return -1
			case a.Timestamp > b.Timestamp:
				return 1
			}
			return 0
		})

		// 切り詰めるのは、まだ到達していない新しい分に初めて書くときだけ。
		truncate := !r.Append && (!seen || k > last)

		// 到達済みより古い分へ戻るのは、時系列に沿って保存するという
		// 前提が崩れている。ここで切り詰めると先に書いた内容を失うので
		// 追記に倒すが、レコードが二重になりうるので記録は残す。
		if seen && k < last {
			r.logger().Warn("到達済みより古い分へ書き戻している。出力が二重になる可能性がある",
				"ssr", ssrID, "minute", nanotime.ToTime(k*OneMinute),
				"last_minute", nanotime.ToTime(last*OneMinute))
		}

		if err := r.writeChunk(r.filePath(ssrID, k*OneMinute), chunk, truncate); err != nil {
			return err
		}
		if !seen || k > last {
			last, seen = k, true
		}
	}
	if r.lastMinute == nil {
		r.lastMinute = make(map[string]int64)
	}
	r.lastMinute[ssrID] = last
	return nil
}

func (r *IntgRepository) logger() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.Default()
}

// filePath は intg のパスを組む。qpkx と違い形式ごとの層は無い。
//
//	{root}/{YYYYMM}/{ssrid}/{YYYYMMDD}/{YYYYMMDDHHMM}{ssrid}.intg
func (r *IntgRepository) filePath(ssrID string, ts int64) string {
	dt := nanotime.ToTime(ts)
	return filepath.Join(r.Root,
		dt.Format("200601"), ssrID, dt.Format("20060102"),
		dt.Format("200601021504")+ssrID+".intg")
}

func (r *IntgRepository) writeChunk(path string, data []Intg, truncate bool) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_APPEND
	if truncate {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	buf := make([]byte, len(data)*intgRecordSize)
	for i, d := range data {
		b := buf[i*intgRecordSize:]
		binary.LittleEndian.PutUint32(b[0:4],
			uint32(d.Timestamp%OneMinute/tsResolution))
		b[4] = d.Mode
		// [0, 2pi) を [0, 2^32) へ写す。float から整数への変換は 0 方向へ
		// 切り捨てられる。負の方位角を渡すと変換結果が実装依存になるので、
		// 呼び出し側が [0, 2pi) を保証していること。
		binary.LittleEndian.PutUint32(b[5:9],
			uint32(d.Azimuth/(2*math.Pi)*0xFFFFFFFF))
	}
	_, err = f.Write(buf)
	return err
}

// ReadIntg は intg ファイルを読み戻す。突き合わせと検証に使う。
func ReadIntg(path string, baseTime int64) ([]Intg, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out, err := DecodeIntg(raw, baseTime)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return out, nil
}

// DecodeIntg は intg のバイト列をレコードへ復号する。
func DecodeIntg(raw []byte, baseTime int64) ([]Intg, error) {
	if len(raw)%intgRecordSize != 0 {
		return nil, fmt.Errorf("intg ファイルサイズ異常: %d byte は %d byte で割り切れません",
			len(raw), intgRecordSize)
	}
	out := make([]Intg, len(raw)/intgRecordSize)
	for i := range out {
		b := raw[i*intgRecordSize:]
		out[i] = Intg{
			Timestamp: baseTime + int64(binary.LittleEndian.Uint32(b[0:4]))*tsResolution,
			Mode:      b[4],
			Azimuth:   float64(binary.LittleEndian.Uint32(b[5:9])) / 0xFFFFFFFF * 2 * math.Pi,
		}
	}
	return out, nil
}

// Fetch は [start, end) の質問予定表を読み戻す。start/end は Unix ナノ秒。
//
// 書き出しと同じレイアウトを読む。レコードはタイムスタンプの分の
// ファイルに入っているので、期間に重なる分のファイルだけを読めばよい。
// 書き出しは分ごとに整列済みで、分をまたぐ順序もファイル順で保たれる。
func (r *IntgRepository) Fetch(ssrID string, start, end int64) ([]Intg, error) {
	if end <= start {
		return nil, nil
	}
	var out []Intg
	first := nanotime.ToTime(start).Truncate(time.Minute)
	last := nanotime.ToTime(end - 1).Truncate(time.Minute)
	for dt := first; !dt.After(last); dt = dt.Add(time.Minute) {
		recs, err := ReadIntg(r.filePath(ssrID, dt.UnixNano()), dt.UnixNano())
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		out = append(out, recs...)
	}
	return slices.DeleteFunc(out, func(d Intg) bool {
		return d.Timestamp < start || d.Timestamp >= end
	}), nil
}
