package verify

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
	"sync"
)

// DownloadingSuffix is appended to filenames while downloading/unverified.
const DownloadingSuffix = ".downloading"

// Result holds the verification outcome for a single file.
type Result struct {
	DirID     int32
	FileIdx   int32
	Name      string // original filename (without .downloading)
	FilePath  string // actual path that was verified (may have .downloading)
	FinalPath string // target path after rename (without .downloading)
	SHA1      []byte
	CRC32     []byte
	OK        bool  // true if file is valid
	Err       error // non-nil if verification failed
}

// Queue processes files for verification with bounded concurrency.
type Queue struct {
	concurrency int
	results     chan Result
	wg          sync.WaitGroup
	sem         chan struct{}
}

// NewQueue creates a verification queue.
func NewQueue(concurrency int) *Queue {
	return &Queue{
		concurrency: concurrency,
		results:     make(chan Result, 256),
		sem:         make(chan struct{}, concurrency),
	}
}

// Results returns the channel of verification results.
func (q *Queue) Results() <-chan Result {
	return q.results
}

// Submit adds a file to the verification queue.
// filePath is the actual file on disk (may end in .downloading).
// finalPath is the desired name after successful verification.
func (q *Queue) Submit(dirID, fileIdx int32, filePath, finalPath string) {
	q.wg.Add(1)
	go func() {
		defer q.wg.Done()
		q.sem <- struct{}{}
		defer func() { <-q.sem }()

		result := verifyFile(dirID, fileIdx, filePath, finalPath)
		q.results <- result
	}()
}

// Wait waits for all pending verifications to complete, then closes the results channel.
func (q *Queue) Wait() {
	q.wg.Wait()
	close(q.results)
}

// SetConcurrency updates the concurrency limit.
func (q *Queue) SetConcurrency(n int) {
	// Replace the semaphore. New goroutines will use the new limit.
	q.sem = make(chan struct{}, n)
}

func verifyFile(dirID, fileIdx int32, filePath, finalPath string) Result {
	r := Result{
		DirID:     dirID,
		FileIdx:   fileIdx,
		Name:      filepath.Base(finalPath),
		FilePath:  filePath,
		FinalPath: finalPath,
	}

	// Determine the real filename for zip detection (strip .downloading suffix).
	realName := strings.ToLower(r.Name)
	if strings.HasSuffix(realName, ".zip") {
		if err := testZip(filePath); err != nil {
			r.Err = fmt.Errorf("zip test failed: %w", err)
			r.OK = false
			return r
		}
	}

	// Compute SHA1 + CRC32 in a single pass.
	sha1Hash, crc32Hash, err := computeHashes(filePath)
	if err != nil {
		r.Err = fmt.Errorf("hash computation failed: %w", err)
		r.OK = false
		return r
	}

	r.SHA1 = sha1Hash
	r.CRC32 = crc32Hash
	r.OK = true
	return r
}

func testZip(filePath string) error {
	r, err := zip.OpenReader(filePath)
	if err != nil {
		return err
	}
	defer r.Close()

	for _, f := range r.File {
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("open %s: %w", f.Name, err)
		}
		// Read all bytes to verify CRC.
		if _, err := io.Copy(io.Discard, rc); err != nil {
			rc.Close()
			return fmt.Errorf("read %s: %w", f.Name, err)
		}
		rc.Close()
	}
	return nil
}

func computeHashes(filePath string) (sha1Bytes, crc32Bytes []byte, err error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()

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

	log.Printf("verified %s: sha1=%x crc32=%x", filepath.Base(filePath), sha1Bytes, crc32Val)
	return sha1Bytes, crc32Bytes, nil
}
