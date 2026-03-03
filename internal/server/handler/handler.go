package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"myrient-horizon/internal/server/db"
	stree "myrient-horizon/internal/server/tree"
	"myrient-horizon/internal/server/wsrpc"
	"myrient-horizon/pkg/protocol"
)

// Handler holds all HTTP handler dependencies.
type Handler struct {
	store *db.Store
	tree  *stree.ServerTree
	hub   *wsrpc.Hub
}

// New creates a new Handler.
func New(store *db.Store, tree *stree.ServerTree, hub *wsrpc.Hub) *Handler {
	return &Handler{store: store, tree: tree, hub: hub}
}

// corsMiddleware wraps an http.Handler and adds CORS headers to every response.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		w.Header().Set("Access-Control-Max-Age", "86400")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// Register sets up all HTTP routes on the given mux and returns a CORS-enabled handler.
func (h *Handler) Register(mux *http.ServeMux) http.Handler {
	// Worker registration (public).
	mux.HandleFunc("POST /api/register", h.handleRegister)

	// WebSocket RPC.
	mux.HandleFunc("/ws", h.hub.HandleWS)

	// Management API (key-authenticated).
	mux.HandleFunc("POST /api/manage/claim", h.authMiddleware(h.handleClaim))
	mux.HandleFunc("DELETE /api/manage/claim", h.authMiddleware(h.handleRelease))
	mux.HandleFunc("PATCH /api/manage/worker/{id}/config", h.authMiddleware(h.handleUpdateConfig))
	mux.HandleFunc("GET /api/manage/workers", h.authMiddleware(h.handleListWorkers))
	mux.HandleFunc("GET /api/manage/conflicts", h.authMiddleware(h.handleConflicts))

	// Statistics API (public).
	mux.HandleFunc("GET /api/stats/overview", h.handleOverview)
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
		workerID, err := h.store.AuthenticateWorker(r.Context(), key)
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
	id, key, err := h.store.RegisterWorker(r.Context(), req.Name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "key": key})
}

// ---- Management API ----

func (h *Handler) handleClaim(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WorkerID int    `json:"worker_id"`
		DirPath  string `json:"dir_path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	dirID, ok := h.tree.Base().DirByPath(req.DirPath)
	if !ok {
		http.Error(w, "directory not found", http.StatusNotFound)
		return
	}
	claimID, err := h.store.ClaimDir(r.Context(), req.WorkerID, dirID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Push task_assign to the worker via WebSocket.
	msg := protocol.TaskAssignMsg{Type: "task_assign", DirIDs: []int32{dirID}}
	_ = h.hub.SendToWorker(r.Context(), req.WorkerID, msg)

	writeJSON(w, http.StatusOK, map[string]any{"claim_id": claimID, "dir_id": dirID})
}

func (h *Handler) handleRelease(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WorkerID int    `json:"worker_id"`
		DirPath  string `json:"dir_path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	dirID, ok := h.tree.Base().DirByPath(req.DirPath)
	if !ok {
		http.Error(w, "directory not found", http.StatusNotFound)
		return
	}
	if err := h.store.ReleaseDir(r.Context(), req.WorkerID, dirID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Push task_revoke.
	msg := protocol.TaskRevokeMsg{Type: "task_revoke", DirIDs: []int32{dirID}}
	_ = h.hub.SendToWorker(r.Context(), req.WorkerID, msg)

	writeJSON(w, http.StatusOK, map[string]string{"status": "released"})
}

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
	if err := h.store.UpdateWorkerConfig(r.Context(), workerID, configJSON); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Push config to worker.
	msg := protocol.ConfigUpdateMsg{Type: "config_update", Config: cfg}
	_ = h.hub.SendToWorker(r.Context(), workerID, msg)

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (h *Handler) handleListWorkers(w http.ResponseWriter, r *http.Request) {
	workers, err := h.store.ListWorkers(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	type workerInfo struct {
		db.Worker
		Online    bool                   `json:"online"`
		Heartbeat *protocol.HeartbeatMsg `json:"heartbeat,omitempty"`
	}

	result := make([]workerInfo, len(workers))
	conns := h.hub.AllConns()
	for i, wk := range workers {
		info := workerInfo{Worker: wk}
		if conn, ok := conns[wk.ID]; ok {
			info.Online = true
			info.Heartbeat, _ = conn.GetHeartbeat()
		}
		result[i] = info
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) handleConflicts(w http.ResponseWriter, r *http.Request) {
	dirIDStr := r.URL.Query().Get("dir_id")
	dirID, err := strconv.Atoi(dirIDStr)
	if err != nil {
		http.Error(w, "invalid dir_id", http.StatusBadRequest)
		return
	}
	idxs, err := h.store.ConflictFiles(r.Context(), int32(dirID))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// For each conflicting file, get details.
	type conflictInfo struct {
		FileIdx  int32           `json:"file_idx"`
		FileName string          `json:"file_name"`
		Reports  []db.FileStatus `json:"reports"`
	}

	var conflicts []conflictInfo
	for _, idx := range idxs {
		gIdx := h.tree.Base().FileGlobalIndex(int32(dirID), idx)
		name := h.tree.Base().Files[gIdx].Name
		reports, _ := h.store.FileStatusByDir(r.Context(), int32(dirID))
		// Filter to this file.
		var fileReports []db.FileStatus
		for _, rpt := range reports {
			if rpt.FileIdx == idx {
				fileReports = append(fileReports, rpt)
			}
		}
		conflicts = append(conflicts, conflictInfo{
			FileIdx:  idx,
			FileName: name,
			Reports:  fileReports,
		})
	}
	writeJSON(w, http.StatusOK, conflicts)
}

// ---- Statistics API ----

func (h *Handler) handleOverview(w http.ResponseWriter, r *http.Request) {
	stats := h.tree.GetDirStats(0) // Root dir = entire tree.
	writeJSON(w, http.StatusOK, map[string]any{
		"total_files": stats.Total,
		"downloading": stats.Downloading,
		"downloaded":  stats.Downloaded,
		"verifying":   stats.Verifying,
		"verified":    stats.Verified,
		"failed":      stats.Failed,
		"conflicts":   stats.Conflict,
	})
}

func (h *Handler) handleSSEStream(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		path = h.tree.Base().Dirs[0].Path // root
	}
	dirID, ok := h.tree.Base().DirByPath(path)
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
	children := h.tree.ChildDirStats(dirID)
	data := map[string]any{
		"type":     "dir_stats",
		"path":     path,
		"dir_id":   dirID,
		"stats":    h.tree.GetDirStats(dirID),
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

	worker, err := h.store.GetWorker(r.Context(), workerID)
	if err != nil {
		http.Error(w, "worker not found", http.StatusNotFound)
		return
	}

	claims, _ := h.store.ActiveClaimsForWorker(r.Context(), workerID)

	result := map[string]any{
		"worker": worker,
		"claims": claims,
		"online": false,
	}

	if conn := h.hub.GetConn(workerID); conn != nil {
		result["online"] = true
		hb, lastSeen := conn.GetHeartbeat()
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
	dirID, ok := h.tree.Base().DirByPath(path)
	if !ok {
		http.Error(w, "directory not found", http.StatusNotFound)
		return
	}

	records, err := h.store.FileStatusByDir(r.Context(), dirID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Build file list with status.
	dir := &h.tree.Base().Dirs[dirID]
	type fileInfo struct {
		Idx     int32           `json:"idx"`
		Name    string          `json:"name"`
		Size    int64           `json:"size"`
		Status  uint8           `json:"best_status"`
		Reports []db.FileStatus `json:"reports,omitempty"`
	}

	files := make([]fileInfo, 0, dir.FileEnd-dir.FileStart)
	reportsByIdx := make(map[int32][]db.FileStatus)
	for _, r := range records {
		reportsByIdx[r.FileIdx] = append(reportsByIdx[r.FileIdx], r)
	}

	for i := dir.FileStart; i < dir.FileEnd; i++ {
		f := &h.tree.Base().Files[i]
		localIdx := i - dir.FileStart
		fi := fileInfo{
			Idx:     localIdx,
			Name:    f.Name,
			Size:    f.Size,
			Status:  f.BestStatus,
			Reports: reportsByIdx[localIdx],
		}
		files = append(files, fi)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"dir_id": dirID,
		"path":   path,
		"stats":  h.tree.GetDirStats(dirID),
		"files":  files,
	})
}

// ---- Helpers ----

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
