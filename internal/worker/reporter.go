package worker

import (
	"log"
	"myrient-horizon/internal/worker/config"
	"net/http"
	"os"
	"sync"
	"time"

	"myrient-horizon/pkg/protocol"

	"github.com/gorilla/websocket"
)

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

func (r *reporter) Run(wsURL, workerKey string) {
	if len(wsURL) > 4 && wsURL[:4] == "http" {
		wsURL = "ws" + wsURL[4:]
	}
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+workerKey)
	headers.Set("X-Worker-Version", protocol.Version)
	backoff := time.Second
	const maxBackoff = 30 * time.Second
	for {
		conn, resp, err := websocket.DefaultDialer.Dial(wsURL, headers)
		if err != nil {
			if resp != nil && resp.StatusCode == http.StatusUpgradeRequired {
				doUpdate(resp)
			}
			log.Printf("reporter: reconnect failed: %v (retry in %v)", err, backoff)
			backoff = min(backoff*2, maxBackoff)
			continue
		}
		closed := false
		conn.SetReadLimit(1 << 20)
		go r.report(conn, &closed)
		r.readLoop(conn)
		closed = true
	}
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
			r.lastSentTasks = append(r.verifiedTasks, r.verifiedTasks...)
			r.verified = nil
			r.verifiedTasks = nil
		}
		r.vMu.Unlock()
		downloading := downloader.CurrentTask()
		report := protocol.PingMsg{
			Version:     r.lastSentVer,
			Verified:    r.lastSent,
			Downloading: make([]protocol.DownloadReport, 0, len(downloading)),
			Status:      protocol.WorkerStatus{},
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
func (r *reporter) Close() *sync.WaitGroup {
	r.closing = &sync.WaitGroup{}
	r.closing.Add(1)
	r.reportTick.Reset(10 * time.Millisecond)
	return r.closing
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
		//case "config_update":
		//	// do nothing
		//	//var msg protocol.ConfigUpdateMsg
		//	//if json.Unmarshal(body, &msg) == nil && r.OnConfigUpdate != nil {
		//	//	r.OnConfigUpdate(msg.Config)
		//	//}
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
				if task.Managed {
					err = os.Rename(task.VerifiedPath(), task.LocalPath)
					if err != nil {
						log.Printf("reporter: rename failed: %v", err)
					}
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
			_ = msg // TODO generate Black-White task
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
}
