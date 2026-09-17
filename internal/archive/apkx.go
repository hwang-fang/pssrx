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

	"pssrx/internal/config"
	"pssrx/internal/record"
)

// ApkxDir は apkx ファイルの配置。1 分 1 ファイルで、分単位に読む。
//
// レイアウトは qpkx と同じで、形式の層だけが apkx/ になる。
//
//	{root}/{YYYYMM}/{station}/{YYYYMMDD}/apkx/{YYYYMMDDHHMM}{station}.apkx
//
// ファイルの分割はファイル上の時刻（F2）で決まる。読み戻した時刻（F1）は
// それより F1–F2 間隔だけ早いので、分の先頭にある応答は F1 では前の分の
// 時刻になる。ReadMinute はファイル区分のまま返し、時刻での切り直しは
// しない。対応づけは PairManager が実際の時刻で行うので、20.3 µs のずれは
// そこで吸収される。
//
// 読んだ結果は常にタイムスタンプ昇順に整列する。対応づけは時刻昇順を前提に
// し、qpkx と同様に逆行がありうるものとして扱う。
type ApkxDir struct {
	Root string
}

// ReadMinute は dt の分のファイルを読み、F1 時刻に直して時刻順に返す。
// ファイルが無ければ空。
func (d *ApkxDir) ReadMinute(stationID string, dt time.Time) ([]record.Reply, error) {
	out, err := readApkx(d.filePath(stationID, dt), dt.UnixNano())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	slices.SortFunc(out, func(a, b record.Reply) int { return cmp.Compare(a.Timestamp, b.Timestamp) })
	return out, nil
}

func (d *ApkxDir) filePath(stationID string, dt time.Time) string {
	return filepath.Join(d.Root,
		dt.Format("200601"), stationID, dt.Format("20060102"), "apkx",
		dt.Format("200601021504")+stationID+".apkx")
}

func readApkx(path string, baseTime int64) ([]record.Reply, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out, err := DecodeApkx(raw, baseTime)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return out, nil
}

// DecodeApkx は apkx のバイト列をレコードへ復号する。
//
// 1 レコード 8 バイト: 分先頭からの経過 [100 ns] (uint32)、応答符号 (uint16)、
// 波高値 (uint16)。すべてリトルエンディアン。ファイル上の時刻は F2 パルスの
// ものなので、F1–F2 間隔を引いて F1 の時刻に直す。
func DecodeApkx(raw []byte, baseTime int64) ([]record.Reply, error) {
	if len(raw)%apkxRecordSize != 0 {
		return nil, fmt.Errorf("apkx ファイルサイズ異常: %d byte は %d byte で割り切れません",
			len(raw), apkxRecordSize)
	}
	out := make([]record.Reply, len(raw)/apkxRecordSize)
	for i := range out {
		b := raw[i*apkxRecordSize:]
		out[i] = record.Reply{
			Timestamp: baseTime + int64(binary.LittleEndian.Uint32(b[0:4]))*tsResolution - config.ReplyFrameLengthNs,
			Code:      binary.LittleEndian.Uint16(b[4:6]),
			WH:        binary.LittleEndian.Uint16(b[6:8]),
		}
	}
	return out, nil
}
