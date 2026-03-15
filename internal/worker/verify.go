package worker

import (
	"archive/zip"
	"context"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"myrient-horizon/internal/worker/config"
)

type verifierRuntime struct {
	mu          sync.Mutex
	concurrency int
	count       int
	taskCh      chan *Task
	wg          sync.WaitGroup
}

var Verifier *verifierRuntime

func InitVerifier() {
	if config.Global.Key == "" {
		log.Fatal("InitVerifier: config.Global not loaded (Key is empty)")
	}
	if Reporter == nil {
		log.Fatal("InitVerifier: Reporter not initialized")
	}

	Verifier = &verifierRuntime{
		concurrency: 2,
		taskCh:      make(chan *Task, 16384),
	}
}

func (v *verifierRuntime) Submit(task *Task) {
	v.taskCh <- task
}

func (v *verifierRuntime) SetConcurrency(n int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.concurrency = n
}

func (v *verifierRuntime) Count() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.count
}

func (v *verifierRuntime) Run(ctx context.Context) {
	v.mu.Lock()
	concurrency := v.concurrency
	v.mu.Unlock()

	for i := 0; i < concurrency; i++ {
		v.wg.Add(1)
		go v.worker(ctx)
	}

	<-ctx.Done()
	close(v.taskCh)
	v.wg.Wait()
}

func (v *verifierRuntime) worker(ctx context.Context) {
	defer v.wg.Done()
	for task := range v.taskCh {
		v.mu.Lock()
		v.count++
		v.mu.Unlock()

		err := task.Verify()
		if err != nil && task.Managed {
			setFileQueuedVerify(task.FileID, false)
		}

		v.mu.Lock()
		v.count--
		v.mu.Unlock()
	}
}

func (t *Task) Verify() error {
	f, err := os.Open(t.LocalPath)
	if err != nil {
		log.Println("Verify:", t.LocalPath, "open:", err)
		return err
	}
	fd := t.GetFile()
	sha, crc, err := computeHashes(f)
	if err != nil {
		f.Close()
		log.Println("Should not happen!")
		log.Println("Verify:", t.LocalPath, "computeHashes:", err)
		return err
	}
	if strings.ToLower(filepath.Ext(t.FinalPath())) == ".zip" {
		if t.ShouldSkipZipCheck(sha) {
			log.Printf("Verify: %s zip check skipped; re-downloaded file has unchanged sha1 %s after previous unzip failure", t.LocalPath, hex.EncodeToString(sha))
		} else {
			_, err = f.Seek(0, io.SeekStart)
			if err != nil {
				f.Close()
				log.Println("Verify:", t.LocalPath, "seek:", err)
				return err
			}
			err = testZip(f)
			if err != nil {
				f.Close()
				t.RememberBadZipSHA1(sha)
				log.Printf("Verify: %s zip: %v (remembering sha1 %s for downloader retry)", t.LocalPath, err, hex.EncodeToString(sha))
				return err
			}
		}
		t.ClearBadZipSHA1()
	}
	if err := f.Close(); err != nil {
		log.Printf("Verify: %s close: %v", t.LocalPath, err)
		return err
	}
	if err := t.prepareReportedPath(); err != nil {
		log.Printf("Verify: %s prepare reported path: %v", t.LocalPath, err)
		return err
	}
	log.Printf("Verify: #%d %s %s %s", t.FileID, hex.EncodeToString(sha), hex.EncodeToString(crc), fd.Name)
	Reporter.PushVerified(t, sha, crc)
	return nil
}

func (t *Task) prepareReportedPath() error {
	if !t.Managed || !strings.HasSuffix(t.LocalPath, DownloadingSuffix) {
		return nil
	}
	target := t.DownloadedPath()
	if samePath(target, t.LocalPath) {
		return nil
	}
	if err := os.Rename(t.LocalPath, target); err != nil {
		return err
	}
	t.LocalPath = target
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
