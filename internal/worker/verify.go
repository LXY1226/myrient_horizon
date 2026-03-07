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

var verifyTaskCh = make(chan *Task, 16384)

func RunVerifier() {
	for task := range verifyTaskCh {
		task.Verify()
	}
}

func (t *Task) Verify() error {
	f, err := os.Open(t.DownloadingPath())
	if err != nil {
		log.Println("Verify:", t.LocalPath, "open:", err)
		return err
	}
	defer f.Close()
	fd := t.GetFile()
	if strings.ToLower(filepath.Ext(t.LocalPath)) == ".zip" {
		err = testZip(f)
		if err != nil {
			log.Println("Verify:", t.LocalPath, "zip:", err)
			return err
		}
		_, err = f.Seek(0, io.SeekStart)
		if err != nil {
			log.Println("Verify:", t.LocalPath, "seek:", err)
			return err
		}
	}
	sha, crc, err := computeHashes(f)
	if err != nil {
		log.Println("Should not happen!")
		log.Println("Verify:", t.LocalPath, "computeHashes:", err)
		return err
	}
	log.Println("Verify:", t.FileID, fd.Name, "sha:", sha, "crc:", crc)
	Reporter.PushVerified(t, sha, crc)
	return nil
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
