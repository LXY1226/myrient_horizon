package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"myrient-horizon/pkg/protocol"
)

// Handler holds all HTTP handler dependencies.
type Handler struct {
	hub *Hub
}

// New creates a new Handler.
func New(hub *Hub) *Handler {
	return &Handler{hub: hub}
}

// corsMiddleware wraps an http.Handler and adds CORS headers to every response.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		//w.Header().Set("Access-Control-Allow-Origin", "*")
		//w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		//w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		//w.Header().Set("Access-Control-Max-Age", "7200")

		//if r.Method == http.MethodOptions {
		//	w.WriteHeader(http.StatusNoContent)
		//	return
		//}

		//start := time.Now()
		log.Printf("%s %s %s %s", time.Now().Format("15:04:05"),
			r.Header.Get("ali-real-client-ip"),
			r.Method, r.RequestURI)

		next.ServeHTTP(w, r)

		//latency := time.Since(start)
		//log.Printf("[HTTP] Completed in %v", latency)
	})
}

// Register sets up all HTTP routes on the given mux and returns a CORS-enabled handler.
func (h *Handler) Register(mux *http.ServeMux) http.Handler {
	// Worker registration (public).
	mux.HandleFunc("POST /api/register", h.handleRegister)

	// WebSocket RPC.
	mux.HandleFunc("/api/ws", h.hub.HandleWS)

	// Management API (key-authenticated).
	// Note: Claim/Release are deprecated in the new design (workers request status on connect)
	mux.HandleFunc("PATCH /api/manage/worker/{id}/config", h.authMiddleware(h.handleUpdateConfig))
	mux.HandleFunc("GET /api/manage/workers", h.authMiddleware(h.handleListWorkers))
	// Conflicts endpoint - stubbed for now
	mux.HandleFunc("GET /api/manage/conflicts", h.authMiddleware(h.handleConflicts))

	// Statistics API (public).
	mux.HandleFunc("GET /api/stats/overview", h.handleOverview)
	mux.HandleFunc("GET /api/stats/tree", h.handleTree)
	mux.HandleFunc("GET /api/stats/stream", h.handleSSEStream)
	mux.HandleFunc("GET /api/stats/worker/{id}", h.handleWorkerStats)
	mux.HandleFunc("GET /api/stats/dir", h.handleDirDetail)

	return corsMiddleware(mux)
}

// ---- Auth Middleware ----

type contextKey string

const workerIDKey contextKey = "worker_id"

func (h *Handler) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Authorization")
		if len(key) > 7 && key[:7] == "Bearer " {
			key = key[7:]
		} else {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		workerID, err := DB.AuthenticateWorker(r.Context(), key)
		if err != nil || workerID == 0 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), workerIDKey, workerID)
		next(w, r.WithContext(ctx))
	}
}

// ---- Registration ----

func (h *Handler) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	id, key, err := DB.RegisterWorker(r.Context(), req.Name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "key": key})
}

// ---- Management API ----

func (h *Handler) handleUpdateConfig(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	workerID, err := strconv.Atoi(idStr)
	if err != nil {
		http.Error(w, "invalid worker id", http.StatusBadRequest)
		return
	}

	var cfg protocol.WorkerConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	configJSON, _ := json.Marshal(cfg)
	if err := DB.UpdateWorkerConfig(r.Context(), workerID, configJSON); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Push config to worker.
	msg := protocol.ConfigUpdateMsg{Type: "config_update", Config: cfg}
	_ = h.hub.SendToWorker(r.Context(), workerID, msg)

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (h *Handler) handleListWorkers(w http.ResponseWriter, r *http.Request) {
	workers, err := DB.ListWorkers(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	type workerInfo struct {
		Worker
		Online    bool                   `json:"online"`
		Heartbeat *protocol.WorkerStatus `json:"heartbeat,omitempty"`
		LastSeen  time.Time              `json:"last_seen"`
	}

	result := make([]workerInfo, len(workers))
	conns := h.hub.AllConns()
	for i, wk := range workers {
		info := workerInfo{Worker: wk}
		if conn, ok := conns[wk.ID]; ok {
			info.Online = true
			info.c
			info.Heartbeat, _ = conn.GetWorkerStatus()
		}
		result[i] = info
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) handleConflicts(w http.ResponseWriter, r *http.Request) {
	// Stub: Conflicts are tracked in-memory by ServerTree now, not in DB
	// TODO: Implement conflict query based on HasConflict flag in tree
	writeJSON(w, http.StatusOK, []any{})
}

// ---- Statistics API ----

func (h *Handler) handleOverview(w http.ResponseWriter, r *http.Request) {
	stats := Tree.GetDirStats(0) // Root dir = entire tree.
	writeJSON(w, http.StatusOK, StatsToOverview(stats))
}

func (h *Handler) handleTree(w http.ResponseWriter, r *http.Request) {
	depth := 1
	if d := r.URL.Query().Get("depth"); d != "" {
		if v, err := strconv.Atoi(d); err == nil && v > 0 && v <= 5 {
			depth = v
		}
	}

	dirID := int32(0)
	if p := r.URL.Query().Get("path"); p != "" {
		id, ok := Tree.Base().DirByPath(p)
		if !ok {
			http.Error(w, "directory not found", http.StatusNotFound)
			return
		}
		dirID = id
	}

	nodes := Tree.BuildTreeNodes(dirID, depth)
	writeJSON(w, http.StatusOK, map[string]any{"directories": nodes})
}

func (h *Handler) handleSSEStream(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		path = Tree.Base().Dirs[0].Path // root
	}
	dirID, ok := Tree.Base().DirByPath(path)
	if !ok {
		http.Error(w, "directory not found", http.StatusNotFound)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	// Send initial data immediately.
	h.sendSSEDirStats(w, flusher, dirID, path)

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			h.sendSSEDirStats(w, flusher, dirID, path)
		}
	}
}

func (h *Handler) sendSSEDirStats(w http.ResponseWriter, flusher http.Flusher, dirID int32, path string) {
	children := Tree.ChildDirStats(dirID)
	data := map[string]any{
		"type":     "dir_stats",
		"path":     path,
		"dir_id":   dirID,
		"stats":    Tree.GetDirStats(dirID),
		"children": children,
	}
	jsonData, _ := json.Marshal(data)
	fmt.Fprintf(w, "data: %s\n\n", jsonData)
	flusher.Flush()
}

func (h *Handler) handleWorkerStats(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	workerID, err := strconv.Atoi(idStr)
	if err != nil {
		http.Error(w, "invalid worker id", http.StatusBadRequest)
		return
	}

	worker, err := DB.GetWorker(r.Context(), workerID)
	if err != nil {
		http.Error(w, "worker not found", http.StatusNotFound)
		return
	}

	// Note: Claims are deprecated in the new design
	result := map[string]any{
		"worker": worker,
		"claims": []any{},
		"online": false,
	}

	if conn := h.hub.GetConn(workerID); conn != nil {
		result["online"] = true
		hb, lastSeen := conn.GetWorkerStatus()
		result["heartbeat"] = hb
		result["last_seen"] = lastSeen
	}

	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) handleDirDetail(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		http.Error(w, "missing path", http.StatusBadRequest)
		return
	}
	dirID, ok := Tree.Base().DirByPath(path)
	if !ok {
		http.Error(w, "directory not found", http.StatusNotFound)
		return
	}

	// Build file list with status from in-memory tree (no DB query needed in new design)
	dir := &Tree.Base().Dirs[dirID]
	type fileInfo struct {
		Idx      int32  `json:"idx"`
		Name     string `json:"name"`
		Size     int64  `json:"size"`
		Status   uint8  `json:"best_status"`
		Conflict bool   `json:"has_conflict"`
	}

	files := make([]fileInfo, 0, dir.FileEnd-dir.FileStart)

	for i := dir.FileStart; i < dir.FileEnd; i++ {
		f := &Tree.Base().Files[i]
		localIdx := i - dir.FileStart
		fi := fileInfo{
			Idx:      localIdx,
			Name:     f.Name,
			Size:     f.Size,
			Status:   uint8(f.Ext.BestStatus),
			Conflict: f.Ext.HasConflict,
		}
		files = append(files, fi)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"dir_id": dirID,
		"path":   path,
		"stats":  Tree.GetDirStats(dirID),
		"files":  files,
	})
}

// ---- Helpers ----

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
