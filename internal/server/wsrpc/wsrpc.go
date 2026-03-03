package wsrpc

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"myrient-horizon/internal/server/db"
	stree "myrient-horizon/internal/server/tree"
	"myrient-horizon/pkg/protocol"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true }, // Allow cross-origin for dev.
}

// WorkerConn represents an active WebSocket connection from a worker.
type WorkerConn struct {
	WorkerID int
	Conn     *websocket.Conn
	cancel   context.CancelFunc

	wmu       sync.Mutex // protects writes to Conn
	mu        sync.Mutex
	Heartbeat *protocol.HeartbeatMsg
	LastSeen  time.Time
}

// GetHeartbeat returns the latest heartbeat and last seen time.
func (wc *WorkerConn) GetHeartbeat() (*protocol.HeartbeatMsg, time.Time) {
	wc.mu.Lock()
	defer wc.mu.Unlock()
	return wc.Heartbeat, wc.LastSeen
}

// Send sends a JSON message to the worker.
func (wc *WorkerConn) Send(_ context.Context, msg any) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	wc.wmu.Lock()
	defer wc.wmu.Unlock()
	return wc.Conn.WriteMessage(websocket.TextMessage, data)
}

// Hub manages all worker WebSocket connections.
type Hub struct {
	mu    sync.RWMutex
	conns map[int]*WorkerConn // workerID → active connection

	store *db.Store
	tree  *stree.ServerTree
}

// NewHub creates a new WebSocket hub.
func NewHub(store *db.Store, tree *stree.ServerTree) *Hub {
	return &Hub{
		conns: make(map[int]*WorkerConn),
		store: store,
		tree:  tree,
	}
}

// GetConn returns the active connection for a worker, if any.
func (h *Hub) GetConn(workerID int) *WorkerConn {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.conns[workerID]
}

// AllConns returns a snapshot of all active connections.
func (h *Hub) AllConns() map[int]*WorkerConn {
	h.mu.RLock()
	defer h.mu.RUnlock()
	cp := make(map[int]*WorkerConn, len(h.conns))
	for k, v := range h.conns {
		cp[k] = v
	}
	return cp
}

// HandleWS is the HTTP handler for WebSocket upgrade at /ws.
func (h *Hub) HandleWS(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	if key == "" {
		http.Error(w, "missing key", http.StatusUnauthorized)
		return
	}

	workerID, err := h.store.AuthenticateWorker(r.Context(), key)
	if err != nil || workerID == 0 {
		http.Error(w, "invalid key", http.StatusUnauthorized)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws: accept error for worker %d: %v", workerID, err)
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	wc := &WorkerConn{
		WorkerID: workerID,
		Conn:     conn,
		cancel:   cancel,
		LastSeen: time.Now(),
	}

	// Disconnect any existing connection for this worker.
	h.mu.Lock()
	if old, ok := h.conns[workerID]; ok {
		old.cancel()
		old.Conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseGoingAway, "replaced by new connection"))
		old.Conn.Close()
		log.Printf("ws: worker %d old connection replaced", workerID)
	}
	h.conns[workerID] = wc
	h.mu.Unlock()

	log.Printf("ws: worker %d connected", workerID)

	// Send current task assignments on connect.
	go h.sendInitialTasks(ctx, wc)

	// Read loop.
	h.readLoop(ctx, wc)

	// Cleanup on disconnect.
	h.mu.Lock()
	if h.conns[workerID] == wc {
		delete(h.conns, workerID)
	}
	h.mu.Unlock()
	cancel()
	conn.WriteMessage(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	conn.Close()
	log.Printf("ws: worker %d disconnected", workerID)
}

func (h *Hub) sendInitialTasks(ctx context.Context, wc *WorkerConn) {
	claims, err := h.store.ActiveClaimsForWorker(ctx, wc.WorkerID)
	if err != nil {
		log.Printf("ws: failed to load claims for worker %d: %v", wc.WorkerID, err)
		return
	}
	if len(claims) == 0 {
		return
	}
	dirIDs := make([]int32, len(claims))
	for i, c := range claims {
		dirIDs[i] = int32(c.DirID)
	}
	msg := protocol.TaskAssignMsg{Type: "task_assign", DirIDs: dirIDs}
	if err := wc.Send(ctx, msg); err != nil {
		log.Printf("ws: failed to send initial tasks to worker %d: %v", wc.WorkerID, err)
	}
}

func (h *Hub) readLoop(ctx context.Context, wc *WorkerConn) {
	for {
		_, data, err := wc.Conn.ReadMessage()
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("ws: read error for worker %d: %v", wc.WorkerID, err)
			}
			return
		}

		env, err := protocol.ParseEnvelope(data)
		if err != nil {
			log.Printf("ws: bad message from worker %d: %v", wc.WorkerID, err)
			continue
		}

		switch env.Type {
		case "file_report":
			h.handleFileReport(ctx, wc, data)
		case "heartbeat":
			h.handleHeartbeat(wc, data)
		default:
			log.Printf("ws: unknown message type %q from worker %d", env.Type, wc.WorkerID)
		}
	}
}

func (h *Hub) handleFileReport(ctx context.Context, wc *WorkerConn, data []byte) {
	var msg protocol.FileReportMsg
	if err := json.Unmarshal(data, &msg); err != nil {
		log.Printf("ws: bad file_report from worker %d: %v", wc.WorkerID, err)
		return
	}

	// Batch upsert to DB.
	records := make([]db.FileStatus, len(msg.Reports))
	for i, r := range msg.Reports {
		var sha1, crc32 []byte
		if r.SHA1 != "" {
			sha1, _ = hex.DecodeString(r.SHA1)
		}
		if r.CRC32 != "" {
			crc32, _ = hex.DecodeString(r.CRC32)
		}
		records[i] = db.FileStatus{
			DirID:    r.DirID,
			FileIdx:  r.FileIdx,
			WorkerID: wc.WorkerID,
			Status:   int16(r.Status),
			SHA1:     sha1,
			CRC32:    crc32,
		}
	}

	if err := h.store.UpsertFileStatusBatch(ctx, records); err != nil {
		log.Printf("ws: db upsert error for worker %d: %v", wc.WorkerID, err)
	}

	// Update in-memory tree.
	for _, r := range msg.Reports {
		var sha1 []byte
		if r.SHA1 != "" {
			sha1, _ = hex.DecodeString(r.SHA1)
		}
		h.tree.ApplyReport(r.DirID, r.FileIdx, wc.WorkerID, r.Status, sha1)
	}
}

func (h *Hub) handleHeartbeat(wc *WorkerConn, data []byte) {
	var msg protocol.HeartbeatMsg
	if err := json.Unmarshal(data, &msg); err != nil {
		return
	}
	wc.mu.Lock()
	wc.Heartbeat = &msg
	wc.LastSeen = time.Now()
	wc.mu.Unlock()
}

// SendToWorker sends a message to a specific worker.
func (h *Hub) SendToWorker(ctx context.Context, workerID int, msg any) error {
	wc := h.GetConn(workerID)
	if wc == nil {
		return nil // Worker not connected, silently ignore.
	}
	return wc.Send(ctx, msg)
}
