package worker

import (
	"context"
	"fmt"
	"log"
	"myrient-horizon/internal/worker/config"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"myrient-horizon/pkg/protocol"

	"github.com/gorilla/websocket"
)

// reporter maintains WebSocket connection to server and reports task status.
// Follows the long-running loop pattern:
// - Run() never returns unless context cancelled, uses exponential backoff for reconnections
// - Close() returns *sync.WaitGroup for graceful shutdown coordination
// - All log messages prefixed with "reporter: " for identification
type reporter struct {
	vMu           sync.Mutex
	verified      []protocol.VerifyReport
	verifiedTasks []*Task
	lastSentVer   int32
	lastSent      []protocol.VerifyReport
	lastSentTasks []*Task

	reportTick *time.Ticker
	closing    *sync.WaitGroup
}

var Reporter *reporter

// Run connects to the server WebSocket and starts the report loop.
// Pattern: Exponential backoff reconnections with max cap (30s).
// Never returns - runs until process termination.
// Run starts the reporter loop with context support for cancellation.
// Called by cmd/worker/main.go with context.
func (r *reporter) RunContext(ctx context.Context) {
	// TODO: Use ctx for cancellation
	wsURL := config.Global.ServerURL + "/ws"
	r.Run(wsURL, config.Global.Key)
}

func (r *reporter) Run(wsURL, workerKey string) {
	if len(wsURL) > 4 && wsURL[:4] == "http" {
		wsURL = "ws" + wsURL[4:]
	}
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+workerKey)
	headers.Set(protocol.HeaderWorkerVersion, protocol.Version)
	headers.Set(protocol.HeaderTreeSHA1, TreeSHA1)
	backoff := time.Second
	const maxBackoff = 10 * time.Second
	for {
		conn, resp, err := websocket.DefaultDialer.Dial(wsURL, headers)
		if err != nil {
			if resp != nil {
				switch resp.StatusCode {
				case http.StatusUpgradeRequired:
					upgradeErr := doUpdate(resp)
					_ = resp.Body.Close()
					log.Fatal(upgradeErr)
				case http.StatusServiceUnavailable:
					retryDelay, retryReason := serviceUnavailableRetry(resp.Header, backoff)
					_ = resp.Body.Close()
					log.Printf("reporter: server unavailable: %s", retryReason)
					log.Printf("reporter: retrying in %v", retryDelay)
					time.Sleep(retryDelay)
					backoff = time.Second
					continue
				}
			}
			log.Printf("reporter: reconnect failed: %v (retry in %v)", err, backoff)
			backoff = min(backoff*2, maxBackoff)
			time.Sleep(backoff)
			continue
		}
		closed := false
		conn.SetReadLimit(1 << 20)
		go r.report(conn, &closed)
		r.readLoop(conn)
		closed = true
	}
}

func serviceUnavailableRetry(headers http.Header, fallback time.Duration) (time.Duration, string) {
	reason := strings.TrimSpace(headers.Get(protocol.HeaderReason))
	if reason == "" {
		reason = "server unavailable"
	}
	retryDelay, err := parseRetryDelay(headers.Get(protocol.HeaderRetryAfter))
	if err != nil {
		log.Printf("reporter: invalid %s header %q: %v", protocol.HeaderRetryAfter, headers.Get(protocol.HeaderRetryAfter), err)
		return fallback, reason
	}
	if retryDelay <= 0 {
		return fallback, reason
	}
	return retryDelay, reason
}

func parseRetryDelay(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	seconds, err := strconv.Atoi(raw)
	if err == nil {
		return time.Duration(seconds) * time.Second, nil
	}
	d, durationErr := time.ParseDuration(raw)
	if durationErr == nil {
		return d, nil
	}
	return 0, fmt.Errorf("parse seconds: %v; parse duration: %v", err, durationErr)
}

func (r *reporter) report(conn *websocket.Conn, closed *bool) {
	r.reportTick = time.NewTicker(time.Duration(config.Global.HeartBeatIntv) * time.Second)
	defer conn.Close()
	for range r.reportTick.C {
		if *closed {
			return
		}
		r.vMu.Lock()
		r.lastSentVer++
		if r.lastSent == nil {
			r.lastSent = r.verified
			r.lastSentTasks = r.verifiedTasks
			r.verified = nil
			r.verifiedTasks = nil
		} else {
			log.Println("reporter: no response received since last sent")
			r.lastSent = append(r.lastSent, r.verified...)
			r.lastSentTasks = append(r.lastSentTasks, r.verifiedTasks...)
			r.verified = nil
			r.verifiedTasks = nil
		}
		r.vMu.Unlock()
		downloading := Downloader.CurrentTask()
		status := protocol.WorkerStatus{}
		if Downloader != nil {
			status.RemainDownload = Downloader.PendingCount()
		}
		if Verifier != nil {
			status.QueueVerify = Verifier.Count()
		}

		report := protocol.PingMsg{
			Version:     r.lastSentVer,
			Verified:    r.lastSent,
			Downloading: make([]protocol.DownloadReport, 0, len(downloading)),
			Status:      status,
		}
		for _, tsk := range downloading {
			report.Downloading = append(report.Downloading, protocol.DownloadReport{
				FileID: tsk.FileID,
			})
		}
		// TODO downloading... downloaded... verified...
		// TODO currentDownloadBytes...
		err := conn.WriteMessage(websocket.BinaryMessage,
			protocol.MarshalConnMessage(protocol.MessagePing, report))
		if err != nil {
			log.Printf("reporter: report failed: %v", err)
			return
		}
		if r.closing != nil {
			r.reportTick.Stop()
		}
	}
}

// closeImpl is the internal implementation of graceful shutdown.
func (r *reporter) closeImpl() *sync.WaitGroup {
	r.closing = &sync.WaitGroup{}
	if r.reportTick == nil {
		return r.closing
	}
	r.closing.Add(1)
	r.reportTick.Reset(10 * time.Millisecond)
	return r.closing
}

// Close initiates graceful shutdown without context (for backward compatibility).
// Called by cmd/verifier/main.go.
func (r *reporter) Close() *sync.WaitGroup {
	return r.closeImpl()
}

// CloseWithContext initiates graceful shutdown with context (for server alignment).
// Called by cmd/worker/main.go.
func (r *reporter) CloseContext(ctx context.Context) *sync.WaitGroup {
	_ = ctx // Context reserved for future timeout support
	return r.closeImpl()
}

// readLoop reads messages from the server and dispatches them.
// On connection loss it automatically reconnects (unless ctx is cancelled).
func (r *reporter) readLoop(conn *websocket.Conn) {
	log.Println("reporter: connected to server")
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			log.Printf("reporter: read error: %v", err)
			return
		}
		msgType, body, err := protocol.UnmarshalConnMessage(data)
		if err != nil {
			log.Printf("reporter: read error: %v", err)
			continue
		}

		switch msgType {
		case protocol.MessagePong:
			msg, err := protocol.UnmarshalMessage[protocol.PongMsg](body)
			if err != nil {
				log.Printf("reporter: read error: %v", err)
				continue
			}
			r.vMu.Lock()
			if r.lastSentVer != msg.Version {
				log.Println("reporter: reported version mismatch, server overloaded?")
				r.vMu.Unlock()
				continue
			}
			reportedTask := r.lastSentTasks
			r.lastSentTasks = nil
			r.lastSent = nil
			r.vMu.Unlock()
			for _, task := range reportedTask {
				if task.Managed && strings.HasSuffix(task.LocalPath, DownloadedSuffix) {
					err = os.Rename(task.LocalPath, task.FinalPath())
					if err != nil && !os.IsNotExist(err) {
						log.Printf("reporter: rename failed: %v", err)
					}
					downloadingControlPath := strings.TrimSuffix(task.LocalPath, DownloadedSuffix) + DownloadingSuffix + ".aria2"
					if removeErr := os.Remove(downloadingControlPath); removeErr != nil && !os.IsNotExist(removeErr) {
						log.Printf("reporter: remove stale aria2 control file failed: %v", removeErr)
					}
				}
				if task.Managed {
					confirmManagedFile(task.FileID)
				}
			}
			if r.closing != nil {
				r.closing.Done()
				return
			}
		case protocol.MessageTaskSync:
			msg, err := protocol.UnmarshalMessage[protocol.ReclaimsSyncMsg](body)
			if err != nil {
				log.Printf("reporter: bad task assign: %v", err)
				continue
			}
			Claims.Replace(*msg)
		default:
			log.Printf("reporter: unknown message type: %s", msgType)
		}
	}
}

// PushVerified adds a file status report to the pending batch.
// Automatically flushes when batch size is reached.
func (r *reporter) PushVerified(t *Task, sha1, crc32 []byte) {
	report := protocol.VerifyReport{
		FileID: t.FileID,
		SHA1:   sha1,
		CRC32:  crc32,
	}

	r.vMu.Lock()
	defer r.vMu.Unlock()
	r.verified = append(r.verified, report)
	r.verifiedTasks = append(r.verifiedTasks, t)
}

// initReporter initializes the Reporter global from config.Global.
// Pattern: Fail-fast bootstrap - validates prerequisites and panics on invalid state.
// Must be called after EnsureConfig() sets config.Global.
func initReporter() {
	if config.Global.ServerURL == "" {
		log.Fatal("reporter: ServerURL is required but not configured")
	}
	if config.Global.Key == "" {
		log.Fatal("reporter: worker Key is required but not configured")
	}
	Reporter = &reporter{}
}

// InitReporter initializes the Reporter global from config.Global.
// Pattern: Fail-fast bootstrap - validates prerequisites and panics on invalid state.
// Must be called after EnsureConfig() sets config.Global.
func InitReporter() {
	initReporter()
}
