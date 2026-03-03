package reporter

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"myrient-horizon/pkg/protocol"

	"github.com/gorilla/websocket"
)

// Reporter manages the WebSocket connection to the server and batches status reports.
type Reporter struct {
	conn    *websocket.Conn
	wmu     sync.Mutex // protects writes to conn
	mu      sync.Mutex
	pending []protocol.FileReport

	batchSize   int
	flushTicker *time.Ticker

	// Incoming messages from server.
	OnConfigUpdate func(protocol.WorkerConfig)
	OnTaskAssign   func([]int32)
	OnTaskRevoke   func([]int32)
}

// New creates a Reporter connected to the server.
func New(_ context.Context, serverURL, key string) (*Reporter, error) {
	wsURL := serverURL + "/ws?key=" + key
	// Convert http(s) to ws(s).
	if len(wsURL) > 4 && wsURL[:4] == "http" {
		wsURL = "ws" + wsURL[4:]
	}

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, http.Header{})
	if err != nil {
		return nil, err
	}
	// Set a large read limit for potential large messages.
	conn.SetReadLimit(1 << 20)

	r := &Reporter{
		conn:      conn,
		batchSize: 50,
	}
	return r, nil
}

// ReadLoop reads messages from the server and dispatches them.
func (r *Reporter) ReadLoop(ctx context.Context) {
	for {
		_, data, err := r.conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("reporter: read error: %v", err)
			return
		}

		env, err := protocol.ParseEnvelope(data)
		if err != nil {
			log.Printf("reporter: bad message: %v", err)
			continue
		}

		switch env.Type {
		case "config_update":
			var msg protocol.ConfigUpdateMsg
			if json.Unmarshal(data, &msg) == nil && r.OnConfigUpdate != nil {
				r.OnConfigUpdate(msg.Config)
			}
		case "task_assign":
			var msg protocol.TaskAssignMsg
			if json.Unmarshal(data, &msg) == nil && r.OnTaskAssign != nil {
				r.OnTaskAssign(msg.DirIDs)
			}
		case "task_revoke":
			var msg protocol.TaskRevokeMsg
			if json.Unmarshal(data, &msg) == nil && r.OnTaskRevoke != nil {
				r.OnTaskRevoke(msg.DirIDs)
			}
		default:
			log.Printf("reporter: unknown message type: %s", env.Type)
		}
	}
}

// ReportFile adds a file status report to the pending batch.
// Automatically flushes when batch size is reached.
func (r *Reporter) ReportFile(ctx context.Context, dirID, fileIdx int32, status uint8, sha1, crc32 []byte) {
	report := protocol.FileReport{
		DirID:   dirID,
		FileIdx: fileIdx,
		Status:  status,
	}
	if sha1 != nil {
		report.SHA1 = hex.EncodeToString(sha1)
	}
	if crc32 != nil {
		report.CRC32 = hex.EncodeToString(crc32)
	}

	r.mu.Lock()
	r.pending = append(r.pending, report)
	shouldFlush := len(r.pending) >= r.batchSize
	r.mu.Unlock()

	if shouldFlush {
		r.Flush(ctx)
	}
}

// Flush sends all pending reports to the server.
func (r *Reporter) Flush(ctx context.Context) {
	r.mu.Lock()
	if len(r.pending) == 0 {
		r.mu.Unlock()
		return
	}
	reports := r.pending
	r.pending = nil
	r.mu.Unlock()

	msg := protocol.FileReportMsg{
		Type:    "file_report",
		Reports: reports,
	}
	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("reporter: marshal error: %v", err)
		return
	}
	r.wmu.Lock()
	err = r.conn.WriteMessage(websocket.TextMessage, data)
	r.wmu.Unlock()
	if err != nil {
		log.Printf("reporter: write error: %v", err)
	}
}

// SendHeartbeat sends a heartbeat message to the server.
func (r *Reporter) SendHeartbeat(ctx context.Context, diskFreeGB float64, downloading, verifying int, aria2Status string) {
	msg := protocol.HeartbeatMsg{
		Type:        "heartbeat",
		DiskFreeGB:  diskFreeGB,
		Downloading: downloading,
		Verifying:   verifying,
		Aria2Status: aria2Status,
	}
	data, _ := json.Marshal(msg)
	r.wmu.Lock()
	err := r.conn.WriteMessage(websocket.TextMessage, data)
	r.wmu.Unlock()
	if err != nil {
		log.Printf("reporter: heartbeat error: %v", err)
	}
}

// Close flushes pending reports and closes the connection.
func (r *Reporter) Close(ctx context.Context) {
	r.Flush(ctx)
	r.conn.WriteMessage(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, "shutdown"))
	r.conn.Close()
}
