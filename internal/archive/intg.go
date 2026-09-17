package archive

import (
	"cmp"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"slices"
	"time"

	"pssrx/internal/record"
)

// IntgDir は intg ファイルの配置。質問予定表を 1 分 1 ファイルで書き出し、
// 分単位に読み戻す。
//
// 書き出しは時系列に沿って保存される前提で、1 分ぶんずつ書き足していく。
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
type IntgDir struct {
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
func (d *IntgDir) Save(ssrID string, data []record.Interrogation) error {
	if len(data) == 0 {
		return nil
	}
	// タイムスタンプは Unix エポック以降なので、素の / と % で足りる。
	byMinute := map[int64][]record.Interrogation{}
	for _, d := range data {
		byMinute[d.Timestamp/OneMinute] = append(byMinute[d.Timestamp/OneMinute], d)
	}
	keys := make([]int64, 0, len(byMinute))
	for k := range byMinute {
		keys = append(keys, k)
	}
	slices.Sort(keys)

	last, seen := d.lastMinute[ssrID]
	for _, k := range keys {
		chunk := byMinute[k]
		slices.SortFunc(chunk, func(a, b record.Interrogation) int { return cmp.Compare(a.Timestamp, b.Timestamp) })

		// 切り詰めるのは、まだ到達していない新しい分に初めて書くときだけ。
		truncate := !d.Append && (!seen || k > last)

		// 到達済みより古い分へ戻るのは、時系列に沿って保存するという
		// 前提が崩れている。ここで切り詰めると先に書いた内容を失うので
		// 追記に倒すが、レコードが二重になりうるので記録は残す。
		if seen && k < last {
			d.logger().Warn("到達済みより古い分へ書き戻している。出力が二重になる可能性がある",
				"ssr", ssrID, "minute", record.ToTime(k*OneMinute),
				"last_minute", record.ToTime(last*OneMinute))
		}

		if err := d.writeChunk(d.filePath(ssrID, k*OneMinute), chunk, truncate); err != nil {
			return err
		}
		if !seen || k > last {
			last, seen = k, true
		}
	}
	if d.lastMinute == nil {
		d.lastMinute = make(map[string]int64)
	}
	d.lastMinute[ssrID] = last
	return nil
}

func (d *IntgDir) logger() *slog.Logger {
	if d.Log != nil {
		return d.Log
	}
	return slog.Default()
}

// filePath は intg のパスを組む。qpkx と違い形式ごとの層は無い。
//
//	{root}/{YYYYMM}/{ssrid}/{YYYYMMDD}/{YYYYMMDDHHMM}{ssrid}.intg
func (d *IntgDir) filePath(ssrID string, ts int64) string {
	dt := record.ToTime(ts)
	return filepath.Join(d.Root,
		dt.Format("200601"), ssrID, dt.Format("20060102"),
		dt.Format("200601021504")+ssrID+".intg")
}

func (d *IntgDir) writeChunk(path string, data []record.Interrogation, truncate bool) error {
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

// QuantizeIntg はレコードをファイルに書いて読み戻したのと同じ値にする。
//
// intg をファイルを経由せず次の段へ渡すときに使う。ファイルから再処理
// した結果とメモリ直列の結果が一致するように、ここで同じ丸めを通す。
// 時刻は 100 ns へ切り捨て、方位は 32 ビットへ量子化する。
func QuantizeIntg(data []record.Interrogation) []record.Interrogation {
	out := make([]record.Interrogation, len(data))
	for i, d := range data {
		out[i] = record.Interrogation{
			Timestamp: d.Timestamp - d.Timestamp%OneMinute%tsResolution,
			Mode:      d.Mode,
			Azimuth:   float64(uint32(d.Azimuth/(2*math.Pi)*0xFFFFFFFF)) / 0xFFFFFFFF * 2 * math.Pi,
		}
	}
	return out
}

// ReadIntg は intg ファイルを読み戻す。突き合わせと検証に使う。
func ReadIntg(path string, baseTime int64) ([]record.Interrogation, error) {
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
func DecodeIntg(raw []byte, baseTime int64) ([]record.Interrogation, error) {
	if len(raw)%intgRecordSize != 0 {
		return nil, fmt.Errorf("intg ファイルサイズ異常: %d byte は %d byte で割り切れません",
			len(raw), intgRecordSize)
	}
	out := make([]record.Interrogation, len(raw)/intgRecordSize)
	for i := range out {
		b := raw[i*intgRecordSize:]
		out[i] = record.Interrogation{
			Timestamp: baseTime + int64(binary.LittleEndian.Uint32(b[0:4]))*tsResolution,
			Mode:      b[4],
			Azimuth:   float64(binary.LittleEndian.Uint32(b[5:9])) / 0xFFFFFFFF * 2 * math.Pi,
		}
	}
	return out, nil
}

// ReadMinute は dt の分のファイルを読み、時刻順に返す。ファイルが無ければ空。
//
// 書き出しと同じレイアウトを読む。レコードはタイムスタンプの分のファイルに
// 入っている。
func (d *IntgDir) ReadMinute(ssrID string, dt time.Time) ([]record.Interrogation, error) {
	out, err := ReadIntg(d.filePath(ssrID, dt.UnixNano()), dt.UnixNano())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	slices.SortFunc(out, func(a, b record.Interrogation) int { return cmp.Compare(a.Timestamp, b.Timestamp) })
	return out, nil
}
