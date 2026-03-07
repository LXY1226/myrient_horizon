package worker

import (
	"context"
	"log"
	"sync/atomic"
	"time"

	"myrient-horizon/internal/worker/aria2"
)

var badVerifyTaskCh = make(chan Task, 16384)
var downloadTaskCh = make(chan Task, 16)
var downloadTaskReplaced = make(chan struct{})

type downloader struct {
	taskCh      chan Task
	client      *aria2.Client
	gidMap      map[string]*Task
	downloading int32
}

var Downloader *downloader

func InitDownloader(client *aria2.Client, taskBufSize int) {
	Downloader = &downloader{
		taskCh: make(chan Task, taskBufSize),
		client: client,
		gidMap: make(map[string]*Task),
	}
}

func (d *downloader) Submit(ctx context.Context, task Task) error {
	select {
	case d.taskCh <- task:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (d *downloader) CurrentTask() []*Task {
	return nil
}

func (d *downloader) Downloading() int32 {
	return atomic.LoadInt32(&d.downloading)
}

func (d *downloader) run() {}

func (d *downloader) adoptExisting(ctx context.Context) {
	waiting, _ := d.client.TellWaiting(ctx, 0, 1000)
	active, _ := d.client.TellActive(ctx)
	for _, info := range append(active, waiting...) {
		if len(info.Files) == 0 {
			continue
		}
		_ = info
	}
}

func (d *downloader) drainEvents(events <-chan aria2.Event) {
	for {
		select {
		case ev := <-events:
			if task, ok := d.gidMap[ev.GID]; ok {
				delete(d.gidMap, ev.GID)
				atomic.AddInt32(&d.downloading, -1)
				_ = task
				_ = ev
			}
		default:
			return
		}
	}
}

func (d *downloader) reconnect(ctx context.Context) {
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

func (d *downloader) Run(ctx context.Context) {
	d.run()
}
