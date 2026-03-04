package worker

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"log"
	"myrient-horizon/internal/worker/config"
	"net/http"
	"sync"
	"time"

	"myrient-horizon/pkg/protocol"

	"github.com/gorilla/websocket"
)

type Reporter struct {
	vMu      sync.Mutex
	verified []protocol.FileReport
}

var reporter *Reporter

func (r *Reporter) Run(wsURL, workerKey string) {
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
		closed := make(chan struct{})
		conn.SetReadLimit(1 << 20)
		go r.report(conn, closed)
		r.readLoop(conn)
		closed <- struct{}{}
	}
}

func (r *Reporter) sendReport(conn *websocket.Conn) error {
	report := protocol.WorkerReportMsg{
		Type:    protocol.WorkerReport,
		Reports: r.verified,
		Metric:  protocol.WorkerMetric{},
	}

	// TODO downloading... downloaded... verified...
	// TODO currentDownloadBytes...
}

func (r *Reporter) report(conn *websocket.Conn, closed chan struct{}) {
	t := time.NewTicker(time.Duration(config.Global.HeartBeatIntv) * time.Second)
	defer conn.Close()
	for {
		select {
		case <-t.C:
			r.vMu.Lock()
			err := r.sendReport(conn)
			if err != nil {
				log.Printf("reporter: report failed: %v", err)
				return
			}
			doneReport := r.verified
			r.verified = nil
			r.vMu.Unlock()
			for _, task := range doneReport {
				// TODO task rename back to no suffix
			}
		case <-closed:
			return
		}
	}
}

// readLoop reads messages from the server and dispatches them.
// On connection loss it automatically reconnects (unless ctx is cancelled).
func (r *Reporter) readLoop(conn *websocket.Conn) {
	log.Println("reporter: connected to server")
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			log.Printf("reporter: read error: %v", err)
			return
		}

		env, err := protocol.ParseEnvelope(data)
		if err != nil {
			log.Printf("reporter: bad message: %v", err)
			continue
		}

		switch env.Type {
		//case "config_update":
		//	// do nothing
		//	//var msg protocol.ConfigUpdateMsg
		//	//if json.Unmarshal(data, &msg) == nil && r.OnConfigUpdate != nil {
		//	//	r.OnConfigUpdate(msg.Config)
		//	//}
		case "reclaim_map":
			var msg protocol.TaskAssignMsg
			if json.Unmarshal(data, &msg) == nil {
				// TODO reinit download map and replace download planner
			}
		default:
			log.Printf("reporter: unknown message type: %s", env.Type)
		}
	}
}

// PushVerified adds a file status report to the pending batch.
// Automatically flushes when batch size is reached.
func (r *Reporter) PushVerified(ctx context.Context, fileID int64, status uint8, sha1, crc32 []byte) {
	report := protocol.FileReport{
		FileID: fileID,
		Status: status,
	}
	if sha1 != nil {
		report.SHA1 = hex.EncodeToString(sha1)
	}
	if crc32 != nil {
		report.CRC32 = hex.EncodeToString(crc32)
	}

	r.vMu.Lock()
	defer r.vMu.Unlock()
	r.verified = append(r.verified, report)
}
