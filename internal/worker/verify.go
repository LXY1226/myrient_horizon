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

// verifierRuntime is the runtime owner for file verification.
// It manages a pool of workers that process verification tasks.
// Replaces the old verifyTaskCh/RunVerifier function model.
type verifierRuntime struct {
	mu          sync.Mutex
	concurrency int
	count       int
	taskCh      chan *Task
	wg          sync.WaitGroup
}

// Verifier is the package-global verifier runtime owner.
// Initialized by InitVerifier() and used by commands.
var Verifier *verifierRuntime

// InitVerifier initializes the package-global Verifier runtime owner.
// Pattern: Fail-fast bootstrap (no return value).
// Must be called once before accessing Verifier.
//
// Bootstrap behavior:
//   - Reads config.Global for settings
//   - Ensures Reporter is initialized (panics if not)
//   - Seeds concurrency default of 2 (from old cmd/worker/main.go)
//   - Panics immediately on invalid state
func InitVerifier() {
	if config.Global.Key == "" {
		log.Fatal("InitVerifier: config.Global not loaded (Key is empty)")
	}
	if Reporter == nil {
		log.Fatal("InitVerifier: Reporter not initialized")
	}

	Verifier = &verifierRuntime{
		concurrency: 2, // Default from old cmd/worker/main.go constant
		taskCh:      make(chan *Task, 16384),
	}
}

// Submit queues a task for verification.
// Non-blocking - sends to buffered channel.
// Must only be called after InitVerifier().
func (v *verifierRuntime) Submit(task *Task) {
	v.taskCh <- task
}

// SetConcurrency updates the verification concurrency limit.
// Server alignment: Dynamic configuration updates from server.
func (v *verifierRuntime) SetConcurrency(n int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.concurrency = n
}

// Count returns the current number of pending verification tasks.
// Server alignment: Status reporting for heartbeat.
func (v *verifierRuntime) Count() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.count
}

// Run starts the verifier worker loop with the specified concurrency.
// Pattern: Context-aware long-running loop (server alignment).
// Respects context cancellation for graceful shutdown.
// Spawns 'concurrency' workers that process tasks from the channel.
func (v *verifierRuntime) Run(ctx context.Context) {
	v.mu.Lock()
	concurrency := v.concurrency
	v.mu.Unlock()

	// Start worker pool
	for i := 0; i < concurrency; i++ {
		v.wg.Add(1)
		go v.worker(ctx)
	}

	// Wait for context cancellation
	<-ctx.Done()

	// Close channel to signal workers to stop
	close(v.taskCh)

	// Wait for all workers to finish
	v.wg.Wait()
}

// worker is the per-worker goroutine that processes tasks.
func (v *verifierRuntime) worker(ctx context.Context) {
	defer v.wg.Done()
	for task := range v.taskCh {
		v.mu.Lock()
		v.count++
		v.mu.Unlock()

		task.Verify()

		v.mu.Lock()
		v.count--
		v.mu.Unlock()
	}
}

// Verify performs hash verification on the downloaded file.
// Routes verified results through Reporter.PushVerified.
// This is the core verification primitive - the Verifier owner
// orchestrates calling this method, but the logic stays here.
func (t *Task) Verify() error {
	f, err := os.Open(t.DownloadingPath())
	if err != nil {
		log.Println("Verify:", t.LocalPath, "open:", err)
		return err
	}
	defer f.Close()
	fd := t.GetFile()
	sha, crc, err := computeHashes(f)
	if err != nil {
		log.Println("Should not happen!")
		log.Println("Verify:", t.LocalPath, "computeHashes:", err)
		return err
	}
	if strings.ToLower(filepath.Ext(t.LocalPath)) == ".zip" {
		if t.ShouldSkipZipCheck(sha) {
			log.Printf("Verify: %s zip check skipped; re-downloaded file has unchanged sha1 %s after previous unzip failure", t.LocalPath, hex.EncodeToString(sha))
		} else {
			_, err = f.Seek(0, io.SeekStart)
			if err != nil {
				log.Println("Verify:", t.LocalPath, "seek:", err)
				return err
			}
			err = testZip(f)
			if err != nil {
				t.RememberBadZipSHA1(sha)
				log.Printf("Verify: %s zip: %v (remembering sha1 %s for downloader retry)", t.LocalPath, err, hex.EncodeToString(sha))
				return err
			}
		}
		t.ClearBadZipSHA1()
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
