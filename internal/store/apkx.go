package store

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"pssrx/internal/config"
)

const apkxRecordSize = 8

// AData は受信した Mode A/C 応答データ 1 件。
//
// Timestamp は F1 パルス（応答の先頭）の受信時刻。apkx ファイルに記録
// されているのは F2 パルス（末尾のフレーミングパルス）の時刻なので、
// 読み込み時に F1–F2 間隔（config.ReplyFrameLengthNs）を引いて直す。
// 応答遅延 3.0 µs は P3 → F1 で定義されるため、対応づけは F1 で行う。
//
// Code は 12 ビットの応答符号で、Mode A 質問への応答ならスコーク、
// Mode C 質問への応答なら高度符号。どちらへの応答かはデータ上には無く、
// 質問予定表との対応づけで決まる。ビット配置の解釈は復号側に任せる。
type AData struct {
	Timestamp int64 // Unix ナノ秒（F1 パルスの受信時刻）
	Code      uint16
	WH        uint16
}

// AdataRepository は apkx ファイル群から応答データを読む。
//
// レイアウトは qpkx と同じで、形式の層だけが apkx/ になる。
//
//	{root}/{YYYYMM}/{station}/{YYYYMMDD}/apkx/{YYYYMMDDHHMM}{station}.apkx
//
// ファイルの分割はファイル上の時刻（F2）で決まる。読み戻した時刻（F1）は
// それより F1–F2 間隔だけ早いので、分の先頭にある応答は F1 では前の分に
// 属する。Fetch は読むファイルの範囲をそのぶんずらして取りこぼさない。
//
// 取得結果は常にタイムスタンプ昇順に整列する。対応づけは時刻昇順を前提に
// し、qpkx と同様に逆行がありうるものとして扱う。
type AdataRepository struct {
	Root string
}

// Fetch は [start, end) の応答データ（F1 時刻）を時刻順に返す。start/end は Unix ナノ秒。
func (r *AdataRepository) Fetch(stationID string, start, end int64) ([]AData, error) {
	if end <= start {
		return nil, nil
	}
	var out []AData
	// ファイル上の時刻は F2 なので、読む範囲は F1–F2 間隔だけ後ろにずれる
	first := ToTime(start + config.ReplyFrameLengthNs).Truncate(time.Minute)
	last := ToTime(end - 1 + config.ReplyFrameLengthNs).Truncate(time.Minute)
	for dt := first; !dt.After(last); dt = dt.Add(time.Minute) {
		recs, err := readApkx(r.filePath(stationID, dt), dt.UnixNano())
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		out = append(out, recs...)
	}
	slices.SortFunc(out, func(a, b AData) int { return compareInt64(a.Timestamp, b.Timestamp) })
	return clip(out, start, end, func(a AData) int64 { return a.Timestamp }), nil
}

func (r *AdataRepository) filePath(stationID string, dt time.Time) string {
	return filepath.Join(r.Root,
		dt.Format("200601"), stationID, dt.Format("20060102"), "apkx",
		dt.Format("200601021504")+stationID+".apkx")
}

func readApkx(path string, baseTime int64) ([]AData, error) {
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
func DecodeApkx(raw []byte, baseTime int64) ([]AData, error) {
	if len(raw)%apkxRecordSize != 0 {
		return nil, fmt.Errorf("apkx ファイルサイズ異常: %d byte は %d byte で割り切れません",
			len(raw), apkxRecordSize)
	}
	out := make([]AData, len(raw)/apkxRecordSize)
	for i := range out {
		b := raw[i*apkxRecordSize:]
		out[i] = AData{
			Timestamp: baseTime + int64(binary.LittleEndian.Uint32(b[0:4]))*tsResolution - config.ReplyFrameLengthNs,
			Code:      binary.LittleEndian.Uint16(b[4:6]),
			WH:        binary.LittleEndian.Uint16(b[6:8]),
		}
	}
	return out, nil
}

// EncodeApkx は DecodeApkx の逆。合成データの書き出しに使う。
// F1 の時刻に F1–F2 間隔を足してファイル上の F2 の時刻にし、baseTime からの
// 経過を 100 ns 単位へ切り捨てる。
func EncodeApkx(data []AData, baseTime int64) []byte {
	buf := make([]byte, len(data)*apkxRecordSize)
	for i, d := range data {
		b := buf[i*apkxRecordSize:]
		binary.LittleEndian.PutUint32(b[0:4], uint32((d.Timestamp+config.ReplyFrameLengthNs-baseTime)/tsResolution))
		binary.LittleEndian.PutUint16(b[4:6], d.Code)
		binary.LittleEndian.PutUint16(b[6:8], d.WH)
	}
	return buf
}

func compareInt64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
