package archive

import (
	"cmp"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"pssrx/internal/record"
)

// QpkxDir は qpkx ファイルの配置。1 分 1 ファイルで、分単位に読む。
//
// 読んだ結果は常にタイムスタンプ昇順に整列する。解析側はデータが時刻昇順で
// あることを前提にしている。セグメント分割は隣接レコードの時間差で切るし、
// 連鎖検出の探索窓は二分探索で決めるので、逆行があるとどちらも意味を失う。
//
// ところが実データの qpkx は約 3 割のファイルで昇順になっていない。
// ファイル先頭に前の分ぶんが数レコードこぼれている型と、ファイル途中で
// 1〜4 秒巻き戻る型の 2 種類がある。どちらもファイルの中で閉じているので、
// 整列はファイル単位で足りる。整列前後で解析結果がどれだけ変わるかは
// NUMERICS.md を参照。
type QpkxDir struct {
	Root string
}

// ReadMinute は dt の分のファイルを読み、時刻順に返す。ファイルが無ければ空。
func (d *QpkxDir) ReadMinute(stationID string, dt time.Time) ([]record.ReceivedInterrogation, error) {
	out, err := readQpkx(d.filePath(stationID, dt), dt.UnixNano())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	slices.SortFunc(out, func(a, b record.ReceivedInterrogation) int { return cmp.Compare(a.Timestamp, b.Timestamp) })
	return out, nil
}

// filePath は qpkx のパスを組む。兄弟に apkx/ spkx/ があるため
// 日付の下にさらに qpkx/ の層が入る。
//
//	{root}/{YYYYMM}/{station}/{YYYYMMDD}/qpkx/{YYYYMMDDHHMM}{station}.qpkx
func (d *QpkxDir) filePath(stationID string, dt time.Time) string {
	return filepath.Join(d.Root,
		dt.Format("200601"), stationID, dt.Format("20060102"), "qpkx",
		dt.Format("200601021504")+stationID+".qpkx")
}

func readQpkx(path string, baseTime int64) ([]record.ReceivedInterrogation, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out, err := DecodeQpkx(raw, baseTime)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return out, nil
}

// DecodeQpkx は qpkx のバイト列をファイル上の順のままレコードへ復号する。
//
// 1 レコード 7 バイト: 分先頭からの経過 [100 ns] (uint32)、質問種別 (uint8)、
// 波高値 (uint16)。すべてリトルエンディアン。
func DecodeQpkx(raw []byte, baseTime int64) ([]record.ReceivedInterrogation, error) {
	if len(raw)%qpkxRecordSize != 0 {
		return nil, fmt.Errorf("qpkx ファイルサイズ異常: %d byte は %d byte で割り切れません",
			len(raw), qpkxRecordSize)
	}
	out := make([]record.ReceivedInterrogation, len(raw)/qpkxRecordSize)
	for i := range out {
		b := raw[i*qpkxRecordSize:]
		out[i] = record.ReceivedInterrogation{
			Timestamp: baseTime + int64(binary.LittleEndian.Uint32(b[0:4]))*tsResolution,
			Mode:      b[4],
			WH:        binary.LittleEndian.Uint16(b[5:7]),
		}
	}
	return out, nil
}
