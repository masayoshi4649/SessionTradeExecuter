// Package diagnostics は注文APIへ接続せず、保存済みの診断資料を小さいZIPへまとめます。
package diagnostics

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
)

const partLimit = 8 << 20
const fragmentLimit = 512 << 10

// fragment は論理ファイルを元のバイト列へ戻すための順序と指紋です。
type fragment struct {
	Logical string `json:"logical_file"`
	Entry   string `json:"zip_entry"`
	Index   int    `json:"fragment_index"`
	Bytes   int    `json:"uncompressed_bytes"`
	SHA256  string `json:"sha256"`
}

// archives は独立したZIPを作り、単一ファイルの転送容量を制限します。
type archives struct {
	dir, prefix string
	file        *os.File
	zip         *zip.Writer
	compressed  int
	entries     []fragment
	paths       []string
	settings    []byte
	summary     any
	part        int
}

// open は衝突しない一時ZIPを作成します。
//
// 引数: なし。返り値: errorは作成失敗です。
func (a *archives) open() error {
	f, err := os.CreateTemp(a.dir, ".diagnostics-*.zip")
	if err != nil {
		return err
	}
	a.file, a.zip, a.compressed, a.entries = f, zip.NewWriter(f), 0, nil
	a.part++
	return nil
}

// add は最大512KiBの断片を圧縮し、8MiB以内の独立ZIPへ追加します。
//
// 引数: logical(string)は元の名前、index(int)は1起点断片番号、data([]byte)は原文です。
// 返り値: errorは圧縮・出力失敗です。
func (a *archives) add(logical string, index int, data []byte) error {
	if len(data) > fragmentLimit {
		return fmt.Errorf("診断断片の上限を超えました")
	}
	var compressed bytes.Buffer
	w, err := flate.NewWriter(&compressed, flate.DefaultCompression)
	if err != nil {
		return err
	}
	if _, err = w.Write(data); err != nil {
		return err
	}
	if err = w.Close(); err != nil {
		return err
	}
	name := fmt.Sprintf("%s.fragment-%06d", logical, index)
	// ファイル名・中央ディレクトリ・設定・manifestの余地を毎ZIPに確保する。
	if a.zip != nil && (len(a.entries) >= 128 || a.compressed+compressed.Len()+len(name)*2+256 > partLimit-(256<<10)) {
		if err = a.finish(false); err != nil {
			return err
		}
	}
	if a.zip == nil {
		if err = a.open(); err != nil {
			return err
		}
	}
	header := &zip.FileHeader{Name: name, Flags: 0x800, Method: zip.Deflate, CRC32: crc32.ChecksumIEEE(data), CompressedSize64: uint64(compressed.Len()), UncompressedSize64: uint64(len(data))}
	entry, err := a.zip.CreateRaw(header)
	if err != nil {
		return err
	}
	if _, err = entry.Write(compressed.Bytes()); err != nil {
		return err
	}
	digest := sha256.Sum256(data)
	a.entries = append(a.entries, fragment{Logical: logical, Entry: name, Index: index, Bytes: len(data), SHA256: hex.EncodeToString(digest[:])})
	a.compressed += compressed.Len() + len(name)*2 + 256
	return nil
}

// finish はmanifestを付けてZIPを確定します。各ZIPは単独で開けます。
//
// 引数: final(bool)は最後のZIPです。返り値: errorは容量・確定失敗です。
func (a *archives) finish(final bool) error {
	if a.zip == nil {
		if err := a.open(); err != nil {
			return err
		}
	}
	manifest, err := json.MarshalIndent(map[string]any{
		"format_version": 1, "part": a.part, "final_part": final, "max_zip_bytes": partLimit,
		"reconstruction": "logical_fileごとに全ZIPのfragment_index昇順で断片をバイト連結するとCSV/JSONL/JSONの元ファイルになります。行やUTF-8文字が断片境界を跨ぐ場合もあります。",
		"fragments":      a.entries, "scope": a.summary,
	}, "", "  ")
	if err != nil {
		return err
	}
	for _, item := range []struct {
		name string
		data []byte
	}{{"manifest.json", manifest}, {"settings.json", a.settings}} {
		w, err := a.zip.Create(item.name)
		if err != nil {
			return err
		}
		if _, err = w.Write(item.data); err != nil {
			return err
		}
	}
	if err = a.zip.Close(); err != nil {
		return err
	}
	a.zip = nil
	if err = a.file.Sync(); err != nil {
		return err
	}
	stat, err := a.file.Stat()
	if err != nil {
		return err
	}
	if stat.Size() > partLimit {
		return fmt.Errorf("診断ZIPが転送上限を超えました: %d", stat.Size())
	}
	old := a.file.Name()
	if err = a.file.Close(); err != nil {
		return err
	}
	name := filepath.Join(a.dir, fmt.Sprintf("%s_part%03d.zip", a.prefix, a.part))
	if _, err = os.Lstat(name); err == nil {
		return fmt.Errorf("既存診断ZIPを上書きしません: %s", name)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err = os.Rename(old, name); err != nil {
		return err
	}
	a.file = nil
	a.paths = append(a.paths, name)
	return nil
}

// abort はこの呼出しが作成した一時ファイルだけを閉じます。
//
// 引数: なし。返り値: なし。確定済みZIPは不完全な診断として残し、再利用しません。
func (a *archives) abort() {
	if a.file != nil {
		name := a.file.Name()
		_ = a.file.Close()
		_ = os.Remove(name)
	}
}

// chunkWriter は大きなCSV/JSONLを一定メモリで連番断片へ変換します。
type chunkWriter struct {
	archive *archives
	name    string
	buffer  []byte
	index   int
	size    int64
}

// Write は入力バイト列を順序を変えずZIP断片へ蓄積します。
//
// 引数: p([]byte)。返り値: intは受理バイト数、errorは出力失敗です。
func (w *chunkWriter) Write(p []byte) (int, error) {
	total := len(p)
	for len(p) > 0 {
		n := min(len(p), fragmentLimit-len(w.buffer))
		w.buffer = append(w.buffer, p[:n]...)
		p = p[n:]
		w.size += int64(n)
		if len(w.buffer) == fragmentLimit {
			if err := w.flush(); err != nil {
				return total - len(p), err
			}
		}
	}
	return total, nil
}

// flush は未保存断片を確定します。
//
// 引数: なし。返り値: errorは出力失敗です。
func (w *chunkWriter) flush() error {
	if len(w.buffer) == 0 && w.index > 0 {
		return nil
	}
	w.index++
	if err := w.archive.add(w.name, w.index, w.buffer); err != nil {
		return err
	}
	w.buffer = w.buffer[:0]
	return nil
}

// putJSON は小さな診断JSONを同じ断片方式で保存します。
//
// 引数: archive(*archives)、name(string)、value(any)。返り値: errorは変換・保存失敗です。
func putJSON(archive *archives, name string, value any) error {
	w := &chunkWriter{archive: archive, name: name}
	if err := json.NewEncoder(w).Encode(value); err != nil {
		return err
	}
	return w.flush()
}

var _ io.Writer = (*chunkWriter)(nil)
