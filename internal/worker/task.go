package worker

import (
	"archive/zip"
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// Task是工作单元的描述，由Producer创建并分发给各阶段
type Task struct {
	FileID    int64  // 全局文件索引
	DirID     int32  // 目录ID（冗余，便于查询）
	Name      string // 文件名
	FinalPath string // 本地路径（绝对路径）
	URI       string // 下载URL
}

// 辅助方法：生成各阶段路径
func (t *Task) DownloadingPath() string { return t.FinalPath + ".downloading" }
func (t *Task) VerifiedPath() string    { return t.FinalPath + ".verified" }

func (t *Task) Verify() error {
	f, err := os.Open(t.DownloadingPath())
	if err != nil {
		log.Println("Verify:", t.FinalPath, "open:", err)
		return err
	}
	defer f.Close()
	if strings.ToLower(filepath.Ext(t.URI)) == ".zip" {
		err = testZip(f)
		if err != nil {
			log.Println("Verify:", t.FinalPath, "zip:", err)
			return err
		}
		_, err = f.Seek(0, io.SeekStart)
		if err != nil {
			log.Println("Verify:", t.FinalPath, "seek:", err)
			return err
		}
	}
	sha, crc, err := computeHashes(f)
	if err != nil {
		log.Println("Should not happen!")
		log.Println("Verify:", t.FinalPath, "computeHashes:", err)
		return err
	}
	log.Println("Verify:", t.FileID, t.Name, "sha:", sha, "crc:", crc)
	// TODO -> reporter
}

func testZip(f *os.File) error {
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	r, err := zip.NewReader(f, fi.Size())
	if err != nil {
		return err
	}

	for _, f := range r.File {
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("open %s: %w", f.Name, err)
		}
		if _, err := io.Copy(io.Discard, rc); err != nil {
			rc.Close()
			return fmt.Errorf("read %s: %w", f.Name, err)
		}
		rc.Close()
	}
	return nil
}

func computeHashes(f *os.File) (sha1Bytes, crc32Bytes []byte, err error) {
	sha1w := sha1.New()
	crc32w := crc32.NewIEEE()
	w := io.MultiWriter(sha1w, crc32w)

	if _, err := io.Copy(w, f); err != nil {
		return nil, nil, err
	}

	sha1Bytes = sha1w.Sum(nil)
	crc32Val := crc32w.Sum32()
	crc32Bytes = make([]byte, 4)
	binary.BigEndian.PutUint32(crc32Bytes, crc32Val)
	return sha1Bytes, crc32Bytes, nil
}
