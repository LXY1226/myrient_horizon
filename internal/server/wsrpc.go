package server

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sync"
	"time"

	"myrient-horizon/pkg/protocol"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

type WorkerConn struct {
	WorkerID int
	Conn     *websocket.Conn
	cancel   context.CancelFunc

	writeMu sync.Mutex
	stateMu sync.RWMutex

	WorkerStatus *protocol.WorkerStatus
	LastSeen     time.Time
}

func (wc *WorkerConn) GetWorkerStatus() (*protocol.WorkerStatus, time.Time) {
	wc.stateMu.RLock()
	defer wc.stateMu.RUnlock()
	return wc.WorkerStatus, wc.LastSeen
}

func (wc *WorkerConn) close(code int, text string) {
	wc.writeMu.Lock()
	defer wc.writeMu.Unlock()
	_ = wc.Conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(code, text))
	_ = wc.Conn.Close()
}

type Hub struct {
	mu        sync.RWMutex
	conns     map[int]*WorkerConn
	closeOnce sync.Once

	WorkerVersion     string
	WorkerDownloadURL string
	WorkerSHA256      string
}

func NewHub() *Hub {
	return &Hub{conns: make(map[int]*WorkerConn)}
}

func (h *Hub) Run(ctx context.Context) {
	log.Println("hub: run loop started")
	<-ctx.Done()
	log.Println("hub: run loop stopping")
	h.Close()
}

func (h *Hub) Close() {
	h.closeOnce.Do(func() {
		h.mu.Lock()
		conns := make(map[int]*WorkerConn, len(h.conns))
		for workerID, conn := range h.conns {
			conns[workerID] = conn
		}
		h.conns = make(map[int]*WorkerConn)
		h.mu.Unlock()

		log.Printf("hub: closing %d worker connection(s)", len(conns))
		for workerID, conn := range conns {
			if conn.cancel != nil {
				conn.cancel()
			}
			conn.close(websocket.CloseGoingAway, "server shutdown")
			log.Printf("hub: worker %d connection closed", workerID)
		}
	})
}

func (h *Hub) GetConn(workerID int) *WorkerConn {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.conns[workerID]
}

func (h *Hub) AllConns() map[int]*WorkerConn {
	h.mu.RLock()
	defer h.mu.RUnlock()
	cp := make(map[int]*WorkerConn, len(h.conns))
	for workerID, conn := range h.conns {
		cp[workerID] = conn
	}
	return cp
}

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
			_ = json.NewEncoder(w).Encode(resp)
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
		if old.cancel != nil {
			old.cancel()
		}
		old.close(websocket.CloseGoingAway, "replaced by new connection")
		log.Printf("hub: worker %d old connection replaced", workerID)
	}
	h.conns[workerID] = wc
	h.mu.Unlock()

	log.Printf("ws: worker %d connected", workerID)
	if err := h.sendInitialReclaims(ctx, wc); err != nil {
		log.Printf("ws: initial reclaim sync failed for worker %d: %v", workerID, err)
	}

	h.readLoop(ctx, wc)

	h.mu.Lock()
	if h.conns[workerID] == wc {
		delete(h.conns, workerID)
	}
	h.mu.Unlock()

	cancel()
	wc.close(websocket.CloseNormalClosure, "")
	log.Printf("ws: worker %d disconnected", workerID)
}

func (h *Hub) sendInitialReclaims(ctx context.Context, wc *WorkerConn) error {
	reclaims, err := DB.GetReclaimsByWorker(ctx, wc.WorkerID)
	if err != nil {
		return err
	}
	if len(reclaims) == 0 {
		log.Printf("ws: worker %d has no reclaims to sync", wc.WorkerID)
		return nil
	}

	reclaimList := make([]protocol.Reclaim, len(reclaims))
	for i, reclaim := range reclaims {
		reclaimList[i] = protocol.Reclaim{
			DirID:   int32(reclaim.DirID),
			IsBlack: reclaim.IsBlack,
		}
	}

	if err := h.send(wc, protocol.MessageTaskSync, protocol.ReclaimsSyncMsg(reclaimList)); err != nil {
		return err
	}
	log.Printf("ws: synced %d reclaim(s) to worker %d", len(reclaimList), wc.WorkerID)
	return nil
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
	msg, err := protocol.UnmarshalMessage[protocol.PingMsg](data)
	if err != nil {
		log.Printf("ws: bad ping from worker %d: %v", wc.WorkerID, err)
		return
	}

	h.setWorkerStatus(wc, msg.Status)
	if err := h.handleFileReport(ctx, wc, msg); err != nil {
		log.Printf("ws: failed to persist ping from worker %d: %v", wc.WorkerID, err)
		return
	}

	if err := h.send(wc, protocol.MessagePong, protocol.PongMsg{Version: msg.Version}); err != nil {
		log.Printf("ws: failed to send pong to worker %d: %v", wc.WorkerID, err)
	}
}

func (h *Hub) handleFileReport(ctx context.Context, wc *WorkerConn, msg *protocol.PingMsg) error {
	for _, report := range msg.Verified {
		sha1 := append([]byte(nil), report.SHA1...)
		crc32 := append([]byte(nil), report.CRC32...)
		status := protocol.StatusVerified

		item := ItemStatus{
			WorkerID: wc.WorkerID,
			FileID:   report.FileID,
			Status:   int16(status),
			SHA1:     sha1,
			CRC32:    crc32,
		}
		if err := DB.UpsertItemStatus(ctx, &item); err != nil {
			return err
		}

		Tree.ApplyReport(int64(report.FileID), wc.WorkerID, status, sha1)
	}

	return nil
}

func (h *Hub) setWorkerStatus(wc *WorkerConn, status protocol.WorkerStatus) {
	statusCopy := status
	wc.stateMu.Lock()
	wc.WorkerStatus = &statusCopy
	wc.LastSeen = time.Now()
	wc.stateMu.Unlock()
}

func (h *Hub) handleHeartbeat(wc *WorkerConn, data []byte) {
	if _, err := protocol.UnmarshalMessage[protocol.HeartbeatMsg](data); err != nil {
		log.Printf("ws: bad heartbeat from worker %d: %v", wc.WorkerID, err)
		return
	}
	wc.stateMu.Lock()
	wc.LastSeen = time.Now()
	wc.stateMu.Unlock()
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

	resp := protocol.StatusSyncResponse{Type: "status_sync_response", Records: records}
	if err := h.send(wc, "status_sync_response", resp); err != nil {
		log.Printf("ws: failed to send status sync response to worker %d: %v", wc.WorkerID, err)
		return
	}
	log.Printf("ws: synced %d status record(s) to worker %d", len(records), wc.WorkerID)
}

func (h *Hub) SendToWorker(ctx context.Context, workerID int, msg any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	wc := h.GetConn(workerID)
	if wc == nil {
		return nil
	}

	msgType, payload, err := marshalServerMessage(msg)
	if err != nil {
		return err
	}
	return h.send(wc, msgType, payload)
}

func (h *Hub) send(wc *WorkerConn, msgType string, payload any) error {
	data := protocol.MarshalConnMessage(msgType, payload)
	wc.writeMu.Lock()
	defer wc.writeMu.Unlock()
	return wc.Conn.WriteMessage(websocket.BinaryMessage, data)
}

func marshalServerMessage(msg any) (string, any, error) {
	switch typed := msg.(type) {
	case protocol.ConfigUpdateMsg:
		return "config_update", typed, nil
	case protocol.PongMsg:
		return protocol.MessagePong, typed, nil
	case protocol.ReclaimsSyncMsg:
		return protocol.MessageTaskSync, typed, nil
	case protocol.StatusSyncResponse:
		return "status_sync_response", typed, nil
	default:
		return "", nil, errors.New("ws: unsupported outbound message type")
	}
}
