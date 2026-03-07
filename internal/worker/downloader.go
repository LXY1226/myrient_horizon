package worker

import (
	"context"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"myrient-horizon/internal/worker/aria2"
)

var badVerifyTaskCh = make(chan Task, 16384)
var downloadTaskCh = make(chan Task, 16)
var downloadTaskReplaced = make(chan struct{})

var downloader Downloader

/*
   Worker workflow (TODO):
   - Connect to aria2
   - Attach various handlers (task completion/failure callbacks)
   - Get count of incomplete tasks (queued + active), add tasks to reach queue limit (20),
     repeat this operation every minute, running in parallel with task completion handling
   - Final check before sending task to aria2: if .download file exists, add but don't decrement count
   - After task completion, if GID not in cache (not a task added in this run), call API to get info,
     get task URL and file path to determine which file, then continue execution
   - Distribute to verifier on task completion
   - Prioritize tasks from badVerifyTaskCh
*/

var (
	downloaderInstance *Downloader
	downloaderOnce     sync.Once
)

// Downloader consumes download tasks and submits them to aria2.
// Follows the stateful runtime owner pattern:
// - Singleton initialized via sync.Once in InitDownloader()
// - External access via GetDownloader() accessor
// - Main loop runs in Run() goroutine
type Downloader struct {
	taskCh      chan Task
	client      *aria2.Client
	gidMap      map[string]*Task
	downloading int32
}

// InitDownloader initialises the singleton Downloader. Must be called once.
// Pattern: sync.Once ensures thread-safe single initialization.
// Returns: *Downloader - the singleton instance
func InitDownloader(client *aria2.Client, taskBufSize int) *Downloader {
	downloaderOnce.Do(func() {
		downloaderInstance = &Downloader{
			taskCh: make(chan Task, taskBufSize),
			client: client,
			gidMap: make(map[string]*Task),
		}
	})
	return downloaderInstance
}

// GetDownloader returns the singleton Downloader instance.
// Must be called after InitDownloader(). Returns nil if not initialized.
func GetDownloader() *Downloader {
	return downloaderInstance
}

// Submit sends a task to the download queue.
func (d *Downloader) Submit(ctx context.Context, task Task) error {
	select {
	case d.taskCh <- task:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (d *Downloader) CurrentTask() []*Task {
	return nil
	// TODO get tasks from aria2
}

// Downloading returns the current download count.
func (d *Downloader) Downloading() int32 {
	return atomic.LoadInt32(&d.downloading)
}

// Run is the main loop. Must be called in a goroutine.
func (d *Downloader) Run() {
	//for {
	//	d.adoptExisting()
	//
	//	events := d.client.Events
	//	done := d.client.Done()
	//
	//inner:
	//	for {
	//		select {
	//
	//		case task := <-d.taskCh:
	//			dir, _ := filepath.Split(task.LocalPath)
	//			gid, err := d.client.AddURI(ctx, task.GetURI(), dir, task.GetFile().Name+".downloading")
	//			if err != nil {
	//				log.Printf("downloader: AddURI failed for %s: %v", task.Name, err)
	//				continue
	//			}
	//			d.gidMap[gid] = &task
	//			atomic.AddInt32(&d.downloading, 1)
	//
	//		case ev := <-events:
	//			task, ok := d.gidMap[ev.GID]
	//			if !ok {
	//				continue
	//			}
	//			delete(d.gidMap, ev.GID)
	//			atomic.AddInt32(&d.downloading, -1)
	//
	//			if ev.Success && d.verifier != nil {
	//				d.verifier.Submit(*task)
	//			}
	//
	//		case <-done:
	//			log.Println("downloader: aria2 connection lost")
	//			d.drainEvents(events)
	//			d.failInFlight()
	//			d.reconnect(ctx)
	//			break inner
	//		}
	//	}
	//}
}

func (d *Downloader) adoptExisting(ctx context.Context) {
	waiting, _ := d.client.TellWaiting(ctx, 0, 1000)
	active, _ := d.client.TellActive(ctx)

	for _, info := range append(active, waiting...) {
		if len(info.Files) == 0 {
			continue
		}
		// Cannot adopt without MatchTask - will be redownloaded
		_ = info
	}
}

func (d *Downloader) drainEvents(events <-chan aria2.Event) {
	for {
		select {
		case ev := <-events:
			if task, ok := d.gidMap[ev.GID]; ok {
				delete(d.gidMap, ev.GID)
				atomic.AddInt32(&d.downloading, -1)
				if ev.Success {
					verifyTaskCh <- task
				}
			}
		default:
			return
		}
	}
}

//
//func (d *Downloader) failInFlight() {
//	for gid, task := range d.gidMap {
//		log.Printf("downloader: failing in-flight task %s (GID %s)", task.Name, gid)
//		atomic.AddInt32(&d.downloading, -1)
//		delete(d.gidMap, gid)
//	}
//}

func (d *Downloader) reconnect(ctx context.Context) {
	backoff := time.Second
	const maxBackoff = 30 * time.Second

	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if err := d.client.ReconnectOnly(); err == nil {
			log.Println("downloader: reconnected to aria2")
			return
		}
		backoff = min(backoff*2, maxBackoff)
	}
}
