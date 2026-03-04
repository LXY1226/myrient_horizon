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
   TODO: worker流程
   - connect aria2
   - 挂上各种handler(任务完成/失败的call)
   - 获取未完成任务计数（排队+active），少于任务排队数（20）多少就加多少，补足任务列表，每分钟重复执行这个操作，与完成时添加任务并行
   - 任务发送到aria2前做最后检测：如果.download文件存在则添加但不减少计数
   - 任务完成后如果gid不在缓存中（非本次运行添加的任务）调接口获得信息，得到任务URL和文件路径来确定是哪个文件并继续执行
   - 任务完成则分发给verifier
   - 任务优先从badVerifyTaskCh获取
*/

var (
	downloaderInstance *Downloader
	downloaderOnce     sync.Once
)

// Downloader consumes download tasks and submits them to aria2.
type Downloader struct {
	taskCh      chan Task
	client      *aria2.Client
	verifier    *Verifier
	gidMap      map[string]*Task
	downloading int32
}

// InitDownloader initialises the singleton Downloader. Must be called once.
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

// SetVerifier sets the Verifier for chain submission.
func (d *Downloader) SetVerifier(v *Verifier) {
	d.verifier = v
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

// Downloading returns the current download count.
func (d *Downloader) Downloading() int32 {
	return atomic.LoadInt32(&d.downloading)
}

// Run is the main loop. Must be called in a goroutine.
func (d *Downloader) Run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}

		d.adoptExisting(ctx)

		events := d.client.Events
		done := d.client.Done()

	inner:
		for {
			select {
			case <-ctx.Done():
				return

			case task := <-d.taskCh:
				gid, err := d.client.AddURI(ctx, task.URI, task.LocalDir, task.Name+".downloading")
				if err != nil {
					log.Printf("downloader: AddURI failed for %s: %v", task.Name, err)
					continue
				}
				d.gidMap[gid] = &task
				atomic.AddInt32(&d.downloading, 1)

			case ev := <-events:
				task, ok := d.gidMap[ev.GID]
				if !ok {
					continue
				}
				delete(d.gidMap, ev.GID)
				atomic.AddInt32(&d.downloading, -1)

				if ev.Success && d.verifier != nil {
					d.verifier.Submit(*task)
				}

			case <-done:
				log.Println("downloader: aria2 connection lost")
				d.drainEvents(events)
				d.failInFlight()
				d.reconnect(ctx)
				break inner
			}
		}
	}
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
				if ev.Success && d.verifier != nil {
					d.verifier.Submit(*task)
				}
			}
		default:
			return
		}
	}
}

func (d *Downloader) failInFlight() {
	for gid, task := range d.gidMap {
		log.Printf("downloader: failing in-flight task %s (GID %s)", task.Name, gid)
		atomic.AddInt32(&d.downloading, -1)
		delete(d.gidMap, gid)
	}
}

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
