// Package server implements the HTTP server and WebSocket RPC for the distributed download system.
//
// Server runtime patterns (aligned with worker-family):
//   - Pattern 2: sync.Once singleton with Init/Get (DB, Tree) - for stateful services
//   - Pattern 4: Constructor-owned (Handler, Hub) - created in main, passed around
//   - Pattern 6: Immutable config exception (upgrader) - documented exception
//
// All log messages use package-level prefixes for identification:
//   - "server:" - main server lifecycle
//   - "handler:" - HTTP request handling
//   - "db:" - database operations
//   - "tree:" - tree state operations
//   - "wsrpc:" - WebSocket RPC operations
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"myrient-horizon/pkg/protocol"
)

// Handler holds all HTTP handler dependencies.
type Handler struct {
	hub *Hub
}

// New creates a new Handler with the given Hub.
// Pattern: Constructor injection (Pattern 4) - Hub created in main and passed here.
func New(hub *Hub) *Handler {
	return &Handler{hub: hub}
}

// corsMiddleware wraps an http.Handler and logs requests.
// Currently a minimal implementation - logs all requests with "handler:" prefix.
// Pattern: Standard middleware chaining.
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
		log.Printf("handler: %s %s %s", r.Header.Get("ali-real-client-ip"),
			r.Method, r.RequestURI)

		next.ServeHTTP(w, r)

		//latency := time.Since(start)
		//log.Printf("[HTTP] Completed in %v", latency)
	})
}

// Register sets up all HTTP routes on the given mux and returns a CORS-enabled handler.
// Routes:
//   - POST /api/register - Worker registration (public)
//   - GET  /api/ws - WebSocket RPC endpoint
//   - PATCH /api/manage/me/config - Update caller config (auth)
//   - GET  /api/manage/workers - List all workers (auth)
//   - GET  /api/stats/* - Statistics APIs (public except /api/stats/me)
func (h *Handler) Register(mux *http.ServeMux) http.Handler {
	// Worker registration (public).
	mux.HandleFunc("POST /api/register", h.handleRegister)

	// WebSocket RPC.
	mux.HandleFunc("/api/ws", h.hub.HandleWS)

	// Management API (key-authenticated).
	// Note: Claim/Release are deprecated in the new design (workers request status on connect)
	mux.HandleFunc("PATCH /api/manage/me/config", h.authMiddleware(h.handleUpdateConfig))
	mux.HandleFunc("PATCH /api/manage/me/name", h.authMiddleware(h.handleUpdateName))
	mux.HandleFunc("GET /api/manage/claims", h.authMiddleware(h.handleListClaims))
	mux.HandleFunc("POST /api/manage/claims", h.authMiddleware(h.handleCreateClaim))
	mux.HandleFunc("DELETE /api/manage/claims/{id}", h.authMiddleware(h.handleDeleteClaim))
	mux.HandleFunc("GET /api/manage/workers", h.authMiddleware(h.handleListWorkers))
	// Conflicts endpoint - stubbed for now
	mux.HandleFunc("GET /api/manage/conflicts", h.authMiddleware(h.handleConflicts))

	// Statistics API (public).
	mux.HandleFunc("GET /api/stats/overview", h.handleOverview)
	mux.HandleFunc("GET /api/stats/tree", h.handleTree)
	mux.HandleFunc("GET /api/stats/stream", h.handleSSEStream)
	mux.HandleFunc("GET /api/stats/me", h.authMiddleware(h.handleWorkerStats))
	mux.HandleFunc("GET /api/stats/dir", h.handleDirDetail)

	return corsMiddleware(mux)
}

// ---- Auth Middleware ----

type contextKey string

const workerKeyKey contextKey = "worker_key"
const workerIDKey contextKey = "worker_id"

const (
	claimStateNone      = "none"
	claimStateWhitelist = "whitelist"
	claimStateBlacklist = "blacklist"
)

func (h *Handler) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Authorization")
		if len(key) > 7 && key[:7] == "Bearer " {
			key = key[7:]
		} else {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		workerID, err := GetDB().AuthenticateWorker(r.Context(), key)
		if err != nil || workerID == 0 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), workerKeyKey, key)
		ctx = context.WithValue(ctx, workerIDKey, workerID)
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
	_, key, err := GetDB().RegisterWorker(r.Context(), req.Name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"key": key})
}

// ---- Management API ----

type claimDirectory struct {
	ID   int    `json:"id"`
	Name string `json:"name,omitempty"`
	Path string `json:"path,omitempty"`
}

type claimResponse struct {
	ID        int            `json:"id"`
	DirID     int            `json:"dir_id"`
	Directory claimDirectory `json:"directory"`
	IsBlack   bool           `json:"is_black"`
	Effective string         `json:"effective_state"`
	CreatedAt time.Time      `json:"created_at"`
}

type createClaimRequest struct {
	DirID   int   `json:"dir_id"`
	IsBlack *bool `json:"is_black,omitempty"`
}

func (h *Handler) handleListClaims(w http.ResponseWriter, r *http.Request) {
	workerKey, ok := workerKeyFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	reclaims, err := GetDB().GetReclaimsByKey(r.Context(), workerKey)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, claimsResponseFromReclaims(reclaims))
}

func (h *Handler) handleCreateClaim(w http.ResponseWriter, r *http.Request) {
	workerKey, ok := workerKeyFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	workerID, ok := workerIDFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req createClaimRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.DirID < 0 {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	isBlack, err := parseClaimIsBlack(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !claimDirectoryExists(req.DirID) {
		http.Error(w, "directory not found", http.StatusNotFound)
		return
	}

	current, err := GetDB().GetEffectiveReclaim(r.Context(), workerID, req.DirID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	status := http.StatusOK
	response := claimResponseForDirState(req.DirID, claimStateNone)
	switch {
	case current == nil:
		reclaim, err := GetDB().SetEffectiveReclaimByKey(r.Context(), workerKey, req.DirID, isBlack)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		status = http.StatusCreated
		response = claimResponseFromReclaim(*reclaim)
	case current.IsBlack == isBlack:
		response = claimResponseFromReclaim(*current)
	default:
		deleted, err := GetDB().DeleteReclaimByKey(r.Context(), workerKey, current.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !deleted {
			http.Error(w, "claim not found", http.StatusNotFound)
			return
		}
	}
	if err := GetTree().RefreshClaimsFromDB(r.Context(), GetDB()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := h.hub.SyncReclaimsForWorker(r.Context(), workerKey); err != nil {
		log.Printf("handler: reclaim sync failed for worker %s: %v", summarizeWorkerKey(workerKey), err)
	}

	writeJSON(w, status, response)
}

func (h *Handler) handleDeleteClaim(w http.ResponseWriter, r *http.Request) {
	workerKey, ok := workerKeyFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	reclaimID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || reclaimID <= 0 {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	reclaims, err := GetDB().GetReclaimsByKey(r.Context(), workerKey)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var target *Reclaim
	for i := range reclaims {
		if reclaims[i].ID == reclaimID {
			target = &reclaims[i]
			break
		}
	}
	if target == nil {
		http.Error(w, "claim not found", http.StatusNotFound)
		return
	}

	deleted, err := GetDB().DeleteReclaimByKey(r.Context(), workerKey, target.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !deleted {
		http.Error(w, "claim not found", http.StatusNotFound)
		return
	}
	if err := GetTree().RefreshClaimsFromDB(r.Context(), GetDB()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := h.hub.SyncReclaimsForWorker(r.Context(), workerKey); err != nil {
		log.Printf("handler: reclaim sync failed for worker %s: %v", summarizeWorkerKey(workerKey), err)
	}

	writeJSON(w, http.StatusOK, claimResponseForDirState(target.DirID, claimStateNone))
}

func (h *Handler) handleUpdateConfig(w http.ResponseWriter, r *http.Request) {
	workerKey, ok := workerKeyFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var cfg protocol.WorkerConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	configJSON, _ := json.Marshal(cfg)
	if _, err := GetDB().pool.Exec(r.Context(), `UPDATE workers SET config = $1 WHERE key = $2`, configJSON, workerKey); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Push config to worker.
	msg := protocol.ConfigUpdateMsg{Type: "config_update", Config: cfg}
	_ = h.hub.SendToWorker(r.Context(), workerKey, msg)

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (h *Handler) handleUpdateName(w http.ResponseWriter, r *http.Request) {
	workerKey, ok := workerKeyFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	name, err := normalizeWorkerName(req.Name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := GetDB().UpdateWorkerNameByKey(r.Context(), workerKey, name); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"name": name})
}

func (h *Handler) handleListWorkers(w http.ResponseWriter, r *http.Request) {
	workers, err := GetDB().ListWorkers(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	type workerInfo struct {
		Name      string     `json:"name"`
		Online    bool       `json:"online"`
		LastSeen  *time.Time `json:"last_seen,omitempty"`
		KeyPrefix string     `json:"key_prefix,omitempty"`
	}

	result := make([]workerInfo, len(workers))
	conns := h.hub.AllConns()
	workerKeys, err := h.listWorkerKeysByID(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for i, wk := range workers {
		info := workerInfo{
			Name:      wk.Name,
			KeyPrefix: summarizeWorkerKey(workerKeys[wk.ID]),
		}
		if conn, ok := conns[workerKeys[wk.ID]]; ok {
			info.Online = true
			lastSeen := conn.LastSeen
			info.LastSeen = &lastSeen
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
	stats := GetTree().GetDirStats(0) // Root dir = entire tree.
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
		id, ok := GetTree().Base().DirByPath(p)
		if !ok {
			http.Error(w, "directory not found", http.StatusNotFound)
			return
		}
		dirID = id
	}

	nodes := GetTree().BuildTreeNodes(dirID, depth)
	claimSets, err := h.loadTreeClaims(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	applyTreeClaims(nodes, claimSets)
	writeJSON(w, http.StatusOK, map[string]any{"directories": nodes})
}

func (h *Handler) loadTreeClaims(ctx context.Context) (map[int32]TreeClaims, error) {
	workers, err := GetDB().ListWorkers(ctx)
	if err != nil {
		return nil, err
	}

	workerNames := make(map[int]string, len(workers))
	for _, worker := range workers {
		workerNames[worker.ID] = strings.TrimSpace(worker.Name)
	}

	type treeClaimAccumulator struct {
		whitelist []TreeClaimWorker
		blacklist []TreeClaimWorker
	}

	accumulators := make(map[int32]*treeClaimAccumulator)
	err = GetDB().ScanAllReclaims(ctx, func(reclaim *Reclaim) error {
		dirID := int32(reclaim.DirID)
		acc := accumulators[dirID]
		if acc == nil {
			acc = &treeClaimAccumulator{}
			accumulators[dirID] = acc
		}

		worker := TreeClaimWorker{
			WorkerID: reclaim.WorkerID,
			Name:     treeClaimWorkerName(workerNames, reclaim),
		}
		if reclaim.IsBlack {
			acc.blacklist = append(acc.blacklist, worker)
			return nil
		}
		acc.whitelist = append(acc.whitelist, worker)
		return nil
	})
	if err != nil {
		return nil, err
	}

	claimSets := make(map[int32]TreeClaims, len(accumulators))
	for dirID, acc := range accumulators {
		sortTreeClaimWorkers(acc.whitelist)
		sortTreeClaimWorkers(acc.blacklist)
		claimSets[dirID] = TreeClaims{
			Whitelist: TreeClaimGroup{Count: len(acc.whitelist), Workers: acc.whitelist},
			Blacklist: TreeClaimGroup{Count: len(acc.blacklist), Workers: acc.blacklist},
		}
	}
	return claimSets, nil
}

func applyTreeClaims(nodes []TreeNode, claimSets map[int32]TreeClaims) {
	for i := range nodes {
		claims, ok := claimSets[nodes[i].DirID]
		if ok {
			nodes[i].Claims = claims
		} else {
			nodes[i].Claims = emptyTreeClaims()
		}
		if len(nodes[i].Children) > 0 {
			applyTreeClaims(nodes[i].Children, claimSets)
		}
	}
}

func sortTreeClaimWorkers(workers []TreeClaimWorker) {
	sort.Slice(workers, func(i, j int) bool {
		left := strings.ToLower(strings.TrimSpace(workers[i].Name))
		right := strings.ToLower(strings.TrimSpace(workers[j].Name))
		if left == right {
			return workers[i].WorkerID < workers[j].WorkerID
		}
		return left < right
	})
}

func treeClaimWorkerName(workerNames map[int]string, reclaim *Reclaim) string {
	if name := strings.TrimSpace(workerNames[reclaim.WorkerID]); name != "" {
		return name
	}
	return fmt.Sprintf("Worker %d", reclaim.WorkerID)
}

func (h *Handler) handleSSEStream(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		path = GetTree().Base().Dirs[0].Path // root
	}
	dirID, ok := GetTree().Base().DirByPath(path)
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
	children := GetTree().ChildDirStats(dirID)
	data := map[string]any{
		"type":     "dir_stats",
		"path":     path,
		"dir_id":   dirID,
		"stats":    GetTree().GetDirStats(dirID),
		"children": children,
	}
	jsonData, _ := json.Marshal(data)
	fmt.Fprintf(w, "data: %s\n\n", jsonData)
	flusher.Flush()
}

func (h *Handler) handleWorkerStats(w http.ResponseWriter, r *http.Request) {
	workerKey, ok := workerKeyFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	type workerStats struct {
		Name      string                 `json:"name"`
		Config    json.RawMessage        `json:"config,omitempty"`
		CreatedAt time.Time              `json:"created_at"`
		Claims    []claimResponse        `json:"claims"`
		Online    bool                   `json:"online"`
		Heartbeat *protocol.WorkerStatus `json:"heartbeat,omitempty"`
		LastSeen  *time.Time             `json:"last_seen,omitempty"`
	}

	result := workerStats{Claims: []claimResponse{}, Online: false}
	err := GetDB().pool.QueryRow(r.Context(),
		`SELECT name, config, created_at FROM workers WHERE key = $1`, workerKey,
	).Scan(&result.Name, &result.Config, &result.CreatedAt)
	if err != nil {
		http.Error(w, "worker not found", http.StatusNotFound)
		return
	}

	reclaims, err := GetDB().GetReclaimsByKey(r.Context(), workerKey)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	result.Claims = claimsResponseFromReclaims(reclaims)

	if conn := h.hub.GetConn(workerKey); conn != nil {
		result.Online = true
		hb, lastSeen := conn.GetWorkerStatus()
		result.Heartbeat = hb
		result.LastSeen = &lastSeen
	}

	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) handleDirDetail(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		http.Error(w, "missing path", http.StatusBadRequest)
		return
	}
	dirID, ok := GetTree().Base().DirByPath(path)
	if !ok {
		http.Error(w, "directory not found", http.StatusNotFound)
		return
	}

	// Build file list with status from in-memory tree (no DB query needed in new design)
	dir := &GetTree().Base().Dirs[dirID]
	type fileInfo struct {
		Idx      int32  `json:"idx"`
		Name     string `json:"name"`
		Size     int64  `json:"size"`
		Status   uint8  `json:"best_status"`
		Conflict bool   `json:"has_conflict"`
	}

	files := make([]fileInfo, 0, dir.FileEnd-dir.FileStart)

	for i := dir.FileStart; i < dir.FileEnd; i++ {
		f := &GetTree().Base().Files[i]
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
		"stats":  GetTree().GetDirStats(dirID),
		"files":  files,
	})
}

// ---- Helpers ----

// writeJSON writes a JSON response with the given status code.
// Helper: standardizes Content-Type header and encoding.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func workerKeyFromContext(ctx context.Context) (string, bool) {
	workerKey, ok := ctx.Value(workerKeyKey).(string)
	return workerKey, ok && workerKey != ""
}

func parseClaimIsBlack(req createClaimRequest) (bool, error) {
	if req.IsBlack == nil {
		return false, fmt.Errorf("missing is_black")
	}
	return *req.IsBlack, nil
}

func claimDirectoryExists(dirID int) bool {
	tree := GetTree()
	if tree == nil {
		return false
	}
	base := tree.Base()
	return dirID >= 0 && dirID < len(base.Dirs)
}

func normalizeWorkerName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("missing name")
	}
	if len(name) > 128 {
		return "", fmt.Errorf("name too long")
	}
	return name, nil
}

func claimsResponseFromReclaims(reclaims []Reclaim) []claimResponse {
	claims := make([]claimResponse, len(reclaims))
	for i, reclaim := range reclaims {
		claims[i] = claimResponseFromReclaim(reclaim)
	}
	return claims
}

func claimResponseFromReclaim(reclaim Reclaim) claimResponse {
	resp := claimResponse{
		ID:        reclaim.ID,
		DirID:     reclaim.DirID,
		Directory: claimDirectory{ID: reclaim.DirID},
		IsBlack:   reclaim.IsBlack,
		Effective: claimStateWhitelist,
		CreatedAt: reclaim.CreatedAt,
	}
	if reclaim.IsBlack {
		resp.Effective = claimStateBlacklist
	}
	tree := GetTree()
	if tree == nil {
		return resp
	}
	base := tree.Base()
	if reclaim.DirID < 0 || reclaim.DirID >= len(base.Dirs) {
		return resp
	}
	dir := base.Dirs[reclaim.DirID]
	resp.Directory.Name = dir.Name
	resp.Directory.Path = dir.Path
	return resp
}

func claimResponseForDirState(dirID int, state string) claimResponse {
	resp := claimResponse{
		DirID:     dirID,
		Directory: claimDirectory{ID: dirID},
		Effective: state,
	}
	tree := GetTree()
	if tree == nil {
		return resp
	}
	base := tree.Base()
	if dirID < 0 || dirID >= len(base.Dirs) {
		return resp
	}
	dir := base.Dirs[dirID]
	resp.Directory.Name = dir.Name
	resp.Directory.Path = dir.Path
	return resp
}

func workerIDFromContext(ctx context.Context) (int, bool) {
	workerID, ok := ctx.Value(workerIDKey).(int)
	return workerID, ok
}

func (h *Handler) listWorkerKeysByID(ctx context.Context) (map[int]string, error) {
	rows, err := GetDB().pool.Query(ctx, `SELECT id, COALESCE(key, '') FROM workers`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	workerKeys := make(map[int]string)
	for rows.Next() {
		var workerID int
		var workerKey string
		if err := rows.Scan(&workerID, &workerKey); err != nil {
			return nil, err
		}
		workerKeys[workerID] = workerKey
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return workerKeys, nil
}
