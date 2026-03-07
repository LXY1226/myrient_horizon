package server

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"myrient-horizon/pkg/protocol"

	"github.com/go4org/hashtriemap"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// WorkerConn represents an active WebSocket connection from a worker.
type WorkerConn struct {
	WorkerID int
	Conn     *websocket.Conn
	//cancel   context.CancelFunc
	wMutex sync.Mutex

	//wmu          sync.Mutex
	//mu           sync.Mutex
	WorkerStatus *protocol.WorkerStatus
	LastSeen     time.Time
}

// GetWorkerStatus returns the latest worker status and last seen time.
func (wc *WorkerConn) GetWorkerStatus() (*protocol.WorkerStatus, time.Time) {
	return wc.WorkerStatus, wc.LastSeen
}

// Send sends a framed protocol message to the worker.
func (wc *WorkerConn) Send(data []byte) error {
	wc.wMutex.Lock()
	defer wc.wMutex.Unlock()
	return wc.Conn.WriteMessage(websocket.BinaryMessage, data)
}

// Hub manages all worker WebSocket connections.
type Hub struct {
	mu    sync.RWMutex
	conns hashtriemap.HashTrieMap[int, *WorkerConn]

	WorkerVersion     string
	WorkerDownloadURL string
	WorkerSHA256      string
}

// NewHub creates a new WebSocket hub.
func NewHub() *Hub {
	return &Hub{}
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
	key := ""
	if auth := r.Header.Get("Authorization"); len(auth) > 7 && auth[:7] == "Bearer " {
		key = auth[7:]
	} else {
		key = r.URL.Query().Get("key")
	}
	if key == "" {
		http.Error(w, "missing key", http.StatusUnauthorized)
		return
	}

	workerID, err := DB.AuthenticateWorker(r.Context(), key)
	if err != nil || workerID == 0 {
		http.Error(w, "invalid key", http.StatusUnauthorized)
		return
	}

	if h.WorkerVersion != "" {
		clientVersion := r.Header.Get("X-Worker-Version")
		if clientVersion != "verifier" && clientVersion != h.WorkerVersion {
			log.Printf("ws: worker %d version mismatch: got %q, want %q", workerID, clientVersion, h.WorkerVersion)
			resp := protocol.UpdateRequiredResponse{
				Error:          "update_required",
				CurrentVersion: clientVersion,
				LatestVersion:  h.WorkerVersion,
				DownloadURL:    h.WorkerDownloadURL,
				SHA256:         h.WorkerSHA256,
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUpgradeRequired)
			json.NewEncoder(w).Encode(resp)
			return
		}
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

	go h.sendInitialReclaims(ctx, wc)

	h.readLoop(ctx, wc)

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

func (h *Hub) sendInitialReclaims(ctx context.Context, wc *WorkerConn) {
	reclaims, err := DB.GetReclaimsByWorker(ctx, wc.WorkerID)
	if err != nil {
		log.Printf("ws: failed to load reclaims for worker %d: %v", wc.WorkerID, err)
		return
	}
	if len(reclaims) == 0 {
		return
	}

	reclaimList := make([]protocol.Reclaim, len(reclaims))
	for i, r := range reclaims {
		reclaimList[i] = protocol.Reclaim{
			DirID:   int32(r.DirID),
			IsBlack: r.IsBlack,
		}
	}

	msg := protocol.ReclaimsSyncMsg{
		Type:     "reclaims_sync",
		Reclaims: reclaimList,
	}
	if err := wc.Send(ctx, msg); err != nil {
		log.Printf("ws: failed to send reclaims sync to worker %d: %v", wc.WorkerID, err)
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

		msgType, body, err := protocol.UnmarshalConnMessage(data)
		if err != nil {
			log.Printf("ws: bad message from worker %d: %v", wc.WorkerID, err)
			continue
		}

		switch msgType {
		case protocol.MessagePing:
			h.handlePing(ctx, wc, body)
		case "heartbeat":
			h.handleHeartbeat(wc, body)
		case "status_sync_request":
			h.handleStatusSyncRequest(ctx, wc)
		default:
			log.Printf("ws: unknown message type %q from worker %d", msgType, wc.WorkerID)
		}
	}
}

func (h *Hub) handlePing(ctx context.Context, wc *WorkerConn, data []byte) {
	var msg protocol.PingMsg
	if err := json.Unmarshal(data, &msg); err != nil {
		log.Printf("ws: bad ping from worker %d: %v", wc.WorkerID, err)
		return
	}

	h.setWorkerStatus(wc, msg.Status)

	if err := h.handleFileReport(ctx, wc, &msg); err != nil {
		log.Printf("ws: failed to persist ping from worker %d: %v", wc.WorkerID, err)
		return
	}

	if err := wc.Send(ctx, protocol.PongMsg{Version: msg.Version}); err != nil {
		log.Printf("ws: failed to send pong to worker %d: %v", wc.WorkerID, err)
	}
}

func (h *Hub) handleFileReport(ctx context.Context, wc *WorkerConn, msg *protocol.PingMsg) error {
	for _, r := range msg.Verified {
		sha1 := append([]byte(nil), r.SHA1...)
		crc32 := append([]byte(nil), r.CRC32...)
		status := protocol.StatusVerified

		item := ItemStatus{
			WorkerID: wc.WorkerID,
			FileID:   r.FileID,
			Status:   int16(status),
			SHA1:     sha1,
			CRC32:    crc32,
		}
		if err := DB.UpsertItemStatus(ctx, &item); err != nil {
			return err
		}

		Tree.ApplyReport(int64(r.FileID), wc.WorkerID, status, sha1)
	}

	return nil
}

func (h *Hub) setWorkerStatus(wc *WorkerConn, status protocol.WorkerStatus) {
	statusCopy := status
	wc.mu.Lock()
	wc.WorkerStatus = &statusCopy
	wc.LastSeen = time.Now()
	wc.mu.Unlock()
}

func (h *Hub) handleHeartbeat(wc *WorkerConn, data []byte) {
	var msg protocol.HeartbeatMsg
	if err := json.Unmarshal(data, &msg); err != nil {
		return
	}
	wc.mu.Lock()
	wc.LastSeen = time.Now()
	wc.mu.Unlock()
}

func (h *Hub) handleStatusSyncRequest(ctx context.Context, wc *WorkerConn) {
	var records []protocol.ItemStatus
	err := DB.ScanAllItemStatusByWorker(ctx, wc.WorkerID, func(item *ItemStatus) error {
		var sha1, crc32 string
		if item.SHA1 != nil {
			sha1 = hex.EncodeToString(item.SHA1)
		}
		if item.CRC32 != nil {
			crc32 = hex.EncodeToString(item.CRC32)
		}
		records = append(records, protocol.ItemStatus{
			FileID: int64(item.FileID),
			Status: uint8(item.Status),
			SHA1:   sha1,
			CRC32:  crc32,
		})
		return nil
	})

	if err != nil {
		log.Printf("ws: failed to scan item status for worker %d: %v", wc.WorkerID, err)
		return
	}

	resp := protocol.StatusSyncResponse{
		Type:    "status_sync_response",
		Records: records,
	}
	if err := wc.Send(ctx, resp); err != nil {
		log.Printf("ws: failed to send status sync response to worker %d: %v", wc.WorkerID, err)
	}
}

// SendToWorker sends a message to a specific worker.
func (h *Hub) SendToWorker(ctx context.Context, workerID int, msg any) error {
	wc := h.GetConn(workerID)
	if wc == nil {
		return nil
	}
	return wc.Send(ctx, msg)
}
