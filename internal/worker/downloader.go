package worker

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"myrient-horizon/internal/worker/aria2"
	"myrient-horizon/internal/worker/config"
)

type downloader struct {
	mu sync.Mutex

	taskCh  chan Task
	client  *aria2.Client
	gidMap  map[string]*Task
	pathGID map[string]string
	pending map[string]struct{}

	downloading int32
}

var Downloader *downloader

func InitDownloader(client *aria2.Client, taskBufSize int) {
	Downloader = &downloader{
		taskCh:  make(chan Task, taskBufSize),
		client:  client,
		gidMap:  make(map[string]*Task),
		pathGID: make(map[string]string),
		pending: make(map[string]struct{}),
	}
}

func (d *downloader) Submit(ctx context.Context, task Task) error {
	if allowed, reason := Claims.AllowsTask(&task); !allowed {
		log.Printf("downloader: skipping managed task #%d %s: %s", task.FileID, task.GetPath(), reason)
		setFileQueuedDownload(task.FileID, false)
		return nil
	}

	d.mu.Lock()
	if _, ok := d.pending[task.LocalPath]; ok {
		d.mu.Unlock()
		return nil
	}
	if _, ok := d.pathGID[task.LocalPath]; ok {
		d.mu.Unlock()
		return nil
	}
	d.pending[task.LocalPath] = struct{}{}
	d.mu.Unlock()

	select {
	case d.taskCh <- task:
		return nil
	case <-ctx.Done():
		d.mu.Lock()
		delete(d.pending, task.LocalPath)
		d.mu.Unlock()
		return ctx.Err()
	}
}

func (d *downloader) ResetPendingManaged() int {
	if d == nil {
		return 0
	}
	drained := 0
	for {
		select {
		case task := <-d.taskCh:
			drained++
			d.mu.Lock()
			delete(d.pending, task.LocalPath)
			d.mu.Unlock()
			if task.Managed {
				setFileQueuedDownload(task.FileID, false)
			}
		default:
			return drained
		}
	}
}

func (d *downloader) PendingCount() int {
	if d == nil {
		return 0
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.pending) + len(d.gidMap)
}

func (d *downloader) CurrentTask() []*Task {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	tasks := make([]*Task, 0, len(d.gidMap))
	for _, task := range d.gidMap {
		copyTask := *task
		tasks = append(tasks, &copyTask)
	}
	return tasks
}

func (d *downloader) Downloading() int32 {
	return atomic.LoadInt32(&d.downloading)
}

func (d *downloader) handleTask(ctx context.Context, task Task) {
	d.mu.Lock()
	delete(d.pending, task.LocalPath)
	d.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(task.LocalPath), 0o755); err != nil {
		log.Printf("downloader: mkdir failed for %s: %v", task.LocalPath, err)
		setFileQueuedDownload(task.FileID, false)
		return
	}

	gid, err := d.client.AddURI(ctx, task.GetURI(), filepath.Dir(task.LocalPath), filepath.Base(task.LocalPath))
	if err != nil {
		if d.tryAdoptDuplicate(ctx, task, err) {
			return
		}
		log.Printf("downloader: addUri failed for #%d %s: %v", task.FileID, task.GetPath(), err)
		setFileQueuedDownload(task.FileID, false)
		return
	}
	d.registerActive(gid, task)
}

func (d *downloader) registerActive(gid string, task Task) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if existing, ok := d.pathGID[task.LocalPath]; ok {
		if existing == gid {
			return
		}
		return
	}
	copyTask := task
	d.gidMap[gid] = &copyTask
	d.pathGID[task.LocalPath] = gid
	atomic.AddInt32(&d.downloading, 1)
	setFileQueuedDownload(task.FileID, true)
}

func (d *downloader) unregister(gid string) (*Task, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	task, ok := d.gidMap[gid]
	if !ok {
		return nil, false
	}
	delete(d.gidMap, gid)
	delete(d.pathGID, task.LocalPath)
	atomic.AddInt32(&d.downloading, -1)
	copyTask := *task
	return &copyTask, true
}

func (d *downloader) adoptExisting(ctx context.Context) {
	if d == nil || d.client == nil {
		return
	}
	waiting, _ := d.client.TellWaiting(ctx, 0, 1000)
	active, _ := d.client.TellActive(ctx)
	for _, info := range append(active, waiting...) {
		task, err := matchManagedTaskByLocalPath(downloadInfoPath(info))
		if err != nil || task == nil {
			continue
		}
		d.registerActive(info.GID, *task)
	}
}

func downloadInfoPath(info aria2.DownloadInfo) string {
	if len(info.Files) == 0 {
		return ""
	}
	return info.Files[0].Path
}

func matchManagedTaskByLocalPath(path string) (*Task, error) {
	if path == "" || !strings.HasSuffix(path, DownloadingSuffix) {
		return nil, fmt.Errorf("not a managed downloading path")
	}
	baseDir, err := filepath.Abs(config.Global.DownloadDir)
	if err != nil {
		return nil, err
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(baseDir, absPath)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(rel, "..") {
		return nil, fmt.Errorf("outside download dir")
	}
	rel = strings.TrimSuffix(filepath.ToSlash(rel), DownloadingSuffix)
	dirPart, fileName := splitRelativePath(rel)
	fileID, err := MatchFileByPath(dirPart, fileName)
	if err != nil {
		return nil, err
	}
	return &Task{FileID: fileID, LocalPath: absPath, Managed: true}, nil
}

func (d *downloader) tryAdoptDuplicate(ctx context.Context, task Task, err error) bool {
	if !isDuplicateAddError(err) {
		return false
	}
	waiting, waitErr := d.client.TellWaiting(ctx, 0, 1000)
	active, activeErr := d.client.TellActive(ctx)
	if waitErr != nil && activeErr != nil {
		return false
	}
	for _, info := range append(active, waiting...) {
		if !samePath(downloadInfoPath(info), task.LocalPath) {
			continue
		}
		d.registerActive(info.GID, task)
		log.Printf("downloader: adopted existing aria2 task for #%d %s", task.FileID, task.GetPath())
		return true
	}
	return false
}

func samePath(left, right string) bool {
	leftAbs, leftErr := filepath.Abs(left)
	rightAbs, rightErr := filepath.Abs(right)
	if leftErr != nil || rightErr != nil {
		return left == right
	}
	return strings.EqualFold(leftAbs, rightAbs)
}

func isDuplicateAddError(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "code:11") || strings.Contains(msg, "same file is being downloaded")
}

func (d *downloader) handleEvent(ev aria2.Event) {
	task, ok := d.unregister(ev.GID)
	if !ok {
		return
	}
	setFileQueuedDownload(task.FileID, false)
	if !ev.Success {
		log.Printf("downloader: download failed for #%d %s", task.FileID, task.GetPath())
		return
	}
	setFileQueuedVerify(task.FileID, true)
	Verifier.Submit(task)
}

func (d *downloader) reconnect(ctx context.Context) {
	backoff := time.Second
	const maxBackoff = 30 * time.Second
	consecutiveFailures := 0
	const restartAfterFailures = 3
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if err := d.client.ReconnectOnly(); err == nil {
			log.Println("downloader: reconnected to aria2")
			return
		} else {
			consecutiveFailures++
			log.Printf("downloader: reconnect to aria2 failed (%d/%d): %v", consecutiveFailures, restartAfterFailures, err)
			if d.client.Managed() && consecutiveFailures >= restartAfterFailures {
				log.Printf("downloader: restarting managed aria2 after %d failed reconnect attempt(s)", consecutiveFailures)
				if restartErr := d.client.RestartProcess(); restartErr == nil {
					log.Println("downloader: restarted aria2 and reconnected")
					return
				} else {
					log.Printf("downloader: restart managed aria2 failed: %v", restartErr)
				}
			}
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

func (d *downloader) run(ctx context.Context) {
	if d == nil || d.client == nil {
		return
	}
	d.adoptExisting(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case task := <-d.taskCh:
			d.handleTask(ctx, task)
		case ev := <-d.client.Events:
			d.handleEvent(ev)
		case <-d.client.Done():
			d.reconnect(ctx)
			d.adoptExisting(ctx)
		}
	}
}

func (d *downloader) Run(ctx context.Context) {
	d.run(ctx)
}
