package worker

import (
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
)

var verifyTaskCh = make(chan Task, 16384)

var verifier Verifier

/*
  TODO: verifier流程
  - 接收verifyTaskCh来的任务，执行校验流程，正常成功的话使用reporter.PushVerified()发布文件信息
  - 如果失败，删除文件，重新将任务推送回badVerifyTaskCh
*/

var (
	verifierInstance *Verifier
	verifierOnce     sync.Once
)

// Verifier verifies downloaded files.
type Verifier struct {
	sem      chan struct{}
	reporter *Reporter
	count    int32
}

// InitVerifier initialises the singleton Verifier.
func InitVerifier(concurrency int) *Verifier {
	verifierOnce.Do(func() {
		verifierInstance = &Verifier{
			sem: make(chan struct{}, concurrency),
		}
	})
	return verifierInstance
}

// SetReporter sets the Reporter for chain submission.
func (v *Verifier) SetReporter(r *Reporter) {
	v.reporter = r
}

// SetConcurrency updates the concurrency limit.
func (v *Verifier) SetConcurrency(n int) {
	v.sem = make(chan struct{}, n)
}

// Count returns the current verification count.
func (v *Verifier) Count() int32 {
	return atomic.LoadInt32(&v.count)
}

// Submit submits a task for verification (asynchronous).
func (v *Verifier) Submit(task Task) {
	atomic.AddInt32(&v.count, 1)
	go func(t Task) {
		defer atomic.AddInt32(&v.count, -1)
		v.sem <- struct{}{}
		defer func() { <-v.sem }()
		v.process(t)
	}(task)
}

func (v *Verifier) process(task Task) {
	downloadingPath := task.DownloadingPath()
	verifiedPath := task.VerifiedPath()
	name := task.Name

	sha1Hash, crc32Hash, err := verifyFile(downloadingPath, name)

	if err != nil {
		log.Printf("Verification failed for %s: %v", name, err)
		os.Remove(downloadingPath)
		if v.reporter != nil {
			v.reporter.ReportAndRename(task, nil, nil)
		}
		return
	}

	if err := os.Rename(downloadingPath, verifiedPath); err != nil {
		log.Printf("Rename failed %s -> %s: %v", downloadingPath, verifiedPath, err)
		os.Remove(downloadingPath)
		if v.reporter != nil {
			v.reporter.ReportAndRename(task, nil, nil)
		}
		return
	}

	if v.reporter != nil {
		v.reporter.ReportAndRename(task, sha1Hash, crc32Hash)
	}
}

func verifyFile(filePath, name string) (sha1Bytes, crc32Bytes []byte, err error) {
	realName := strings.ToLower(name)
	if strings.HasSuffix(realName, ".zip") {
		if err := testZip(filePath); err != nil {
			return nil, nil, fmt.Errorf("zip test failed: %w", err)
		}
	}

	sha1Hash, crc32Hash, err := computeHashes(filePath)
	if err != nil {
		return nil, nil, fmt.Errorf("hash computation failed: %w", err)
	}
	return sha1Hash, crc32Hash, nil
}
