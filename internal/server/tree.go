package server

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"sync"

	mt "myrient-horizon/pkg/myrienttree"
	"myrient-horizon/pkg/protocol"
)

// DirExt holds aggregated file counts for a directory (including all descendants).
type DirExt struct {
	Total      int32 `json:"total"`
	Claimed    int32 `json:"claimed"`
	Downloaded int32 `json:"downloaded"`
	Verified   int32 `json:"verified"`
	Archived   int32 `json:"archived"`
	Failed     int32 `json:"failed"`
	Conflict   int32 `json:"conflict"`

	TotalSize      int64 `json:"total_size"`
	ClaimedSize    int64 `json:"claimed_size"`
	DownloadedSize int64 `json:"downloaded_size"`
	VerifiedSize   int64 `json:"verified_size"`
	ArchivedSize   int64 `json:"archived_size"`
}

// FileExt holds per-file mutable state for server-side tracking.
type FileExt struct {
	BestStatus  protocol.TaskStatus
	ReportCount uint8
	HasConflict bool
}

// ServerTree wraps the base tree with a mutex for thread-safe access.
// All mutable state is stored in DirNode.Ext (DirExt) and FileNode.Ext (FileExt).
type ServerTree struct {
	mu   sync.RWMutex
	base *mt.Tree[DirExt, FileExt]
	// Per-file: track known SHA1 per worker for conflict detection.
	// Key: global file index. Value: map[workerKey]sha1Bytes.
	fileHashes []map[string][]byte
}

var (
	treeInstance *ServerTree
	treeSHA1     string
)

// Tree is the global ServerTree instance (deprecated: use GetTree()).
var Tree *ServerTree

func GetTree() *ServerTree {
	return treeInstance
}

func GetTreeSHA1() string {
	return treeSHA1
}

func LoadTree(path string) error {
	base, hash, err := mt.LoadFromFile[DirExt, FileExt](path)
	if err != nil {
		panic(err)
	}
	treeInstance = NewTree(base)
	treeSHA1 = hash
	Tree = treeInstance
	log.Printf("tree: initialized successfully (sha1=%s)", treeSHA1)
	return nil
}

// NewTree creates a ServerTree from a base tree.
func NewTree(base *mt.Tree[DirExt, FileExt]) *ServerTree {
	st := &ServerTree{
		base:       base,
		fileHashes: make([]map[string][]byte, len(base.Files)),
	}
	// Initialize Total counts by walking the tree bottom-up.
	st.initTotals()
	return st
}

// Base returns the underlying tree for read-only access.
// Callers must not modify the returned tree; use ServerTree methods for mutations.
func (st *ServerTree) Base() *mt.Tree[DirExt, FileExt] {
	return st.base
}

// GetDirStats returns a copy of the stats for a directory.
func (st *ServerTree) GetDirStats(dirID int32) DirExt {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.base.Dirs[dirID].Ext
}

// initTotals computes Total file counts and TotalSize for each directory.
// FileStart:FileEnd already spans all descendant files, so no bottom-up aggregation needed.
func (st *ServerTree) initTotals() {
	for i := range st.base.Dirs {
		d := &st.base.Dirs[i]
		d.Ext.Total = d.FileEnd - d.FileStart
		var size int64
		for gIdx := d.FileStart; gIdx < d.FileEnd; gIdx++ {
			size += st.base.Files[gIdx].Size
		}
		d.Ext.TotalSize = size
	}
}

// ApplyReport processes a single file status report using fileID (global file index).
func (st *ServerTree) ApplyReport(fileID int64, workerKey string, status protocol.TaskStatus, sha1 []byte) {
	st.mu.Lock()
	defer st.mu.Unlock()

	if fileID < 0 || fileID >= int64(len(st.base.Files)) {
		return
	}

	file := &st.base.Files[fileID]
	oldStatus := file.Ext.BestStatus
	oldConflict := file.Ext.HasConflict
	// Track per-worker SHA1 for conflict detection.
	if sha1 != nil {
		if st.fileHashes[fileID] == nil {
			st.fileHashes[fileID] = make(map[string][]byte)
		}
		st.fileHashes[fileID][workerKey] = append([]byte(nil), sha1...)
	}

	// Update ReportCount.
	if st.fileHashes[fileID] != nil {
		file.Ext.ReportCount = uint8(len(st.fileHashes[fileID]))
	} else if file.Ext.ReportCount == 0 {
		file.Ext.ReportCount = 1
	}

	// Update BestStatus (take highest).
	if status > file.Ext.BestStatus {
		file.Ext.BestStatus = status
	}

	// Check for SHA1 conflicts.
	newConflict := st.hasConflictLocked(fileID)
	file.Ext.HasConflict = newConflict
	// Bubble dirExt delta up the ancestor chain.
	st.bubbleDelta(file.DirIdx, oldStatus, file.Ext.BestStatus, oldConflict, newConflict, file.Size)
}

func (st *ServerTree) HydrateFromDB(ctx context.Context, db *Store) error {
	var reclaims []Reclaim
	err := db.ScanAllReclaims(ctx, func(reclaim *Reclaim) error {
		reclaims = append(reclaims, *reclaim)
		return nil
	})
	if err != nil {
		return err
	}

	return st.hydrateFromState(func(fn func(*ItemStatus) error) error {
		return db.ScanAllItemStatus(ctx, fn)
	}, reclaims)
}

func (st *ServerTree) hydrateFromItemStatus(scan func(func(*ItemStatus) error) error) error {
	return st.hydrateFromState(scan, nil)
}

func (st *ServerTree) RefreshClaimsFromDB(ctx context.Context, db *Store) error {
	var reclaims []Reclaim
	err := db.ScanAllReclaims(ctx, func(reclaim *Reclaim) error {
		reclaims = append(reclaims, *reclaim)
		return nil
	})
	if err != nil {
		return err
	}

	st.mu.Lock()
	defer st.mu.Unlock()
	st.recomputeClaimedStatsLocked(reclaims)
	return nil
}

func (st *ServerTree) hydrateFromState(scan func(func(*ItemStatus) error) error, reclaims []Reclaim) error {
	st.mu.Lock()
	defer st.mu.Unlock()

	for i := range st.base.Files {
		st.base.Files[i].Ext = FileExt{}
	}
	st.fileHashes = make([]map[string][]byte, len(st.base.Files))

	err := scan(func(item *ItemStatus) error {
		fileID := int(item.FileID)
		if fileID < 0 || fileID >= len(st.base.Files) {
			log.Printf("tree: skipping item_status for out-of-range file_id=%d worker=%s", item.FileID, summarizeWorkerKey(item.WorkerKey))
			return nil
		}

		file := &st.base.Files[fileID]
		status := protocol.TaskStatus(item.Status)
		if status > file.Ext.BestStatus {
			file.Ext.BestStatus = status
		}

		if st.fileHashes[fileID] == nil {
			st.fileHashes[fileID] = make(map[string][]byte)
		}
		st.fileHashes[fileID][item.WorkerKey] = append([]byte(nil), item.SHA1...)

		return nil
	})
	if err != nil {
		return err
	}

	for fileID := range st.base.Files {
		hashes := st.fileHashes[fileID]
		st.base.Files[fileID].Ext.ReportCount = uint8(len(hashes))
		st.base.Files[fileID].Ext.HasConflict = st.hasConflictLocked(int64(fileID))
	}

	st.recomputeAllStatsLocked(reclaims)
	return nil
}

// hasConflictLocked checks if a file has SHA1 conflicts among reporters.
func (st *ServerTree) hasConflictLocked(fileID int64) bool {
	hashes := st.fileHashes[fileID]
	if len(hashes) < 2 {
		return false
	}
	var first []byte
	for _, h := range hashes {
		if first == nil {
			first = h
			continue
		}
		if !bytes.Equal(first, h) {
			return true
		}
	}
	return false
}

// bubbleDelta updates dirExt along the ancestor chain when a file's status changes.
func (st *ServerTree) bubbleDelta(dirID int32, oldStatus, newStatus protocol.TaskStatus, oldConflict, newConflict bool, fileSize int64) {
	if oldStatus == newStatus && oldConflict == newConflict {
		return
	}
	for id := dirID; id >= 0; id = st.base.Dirs[id].ParentIdx {
		s := &st.base.Dirs[id].Ext
		decrStatus(s, oldStatus, fileSize)
		incrStatus(s, newStatus, fileSize)
		if oldConflict && !newConflict {
			s.Conflict--
		} else if !oldConflict && newConflict {
			s.Conflict++
		}
	}
}

func decrStatus(s *DirExt, status protocol.TaskStatus, size int64) {
	switch status {
	case protocol.StatusDownloaded:
		s.Downloaded--
		s.DownloadedSize -= size
	case protocol.StatusVerified:
		s.Verified--
		s.VerifiedSize -= size
	case protocol.StatusArchived:
		s.Archived--
		s.ArchivedSize -= size
	case protocol.StatusFailed:
		s.Failed--
	}
}

func incrStatus(s *DirExt, status protocol.TaskStatus, size int64) {
	switch status {
	case protocol.StatusDownloaded:
		s.Downloaded++
		s.DownloadedSize += size
	case protocol.StatusVerified:
		s.Verified++
		s.VerifiedSize += size
	case protocol.StatusArchived:
		s.Archived++
		s.ArchivedSize += size
	case protocol.StatusFailed:
		s.Failed++
	}
}

// recomputeAllStatsLocked rebuilds all dirExt from scratch.
// Used after bulk recovery to ensure consistency. Caller must hold write lock.
func (st *ServerTree) recomputeAllStatsLocked(reclaims []Reclaim) {
	for i := range st.base.Dirs {
		st.base.Dirs[i].Ext = DirExt{}
	}

	for i := range st.base.Dirs {
		d := &st.base.Dirs[i]
		s := &st.base.Dirs[i].Ext
		s.Total = d.FileEnd - d.FileStart
		for gIdx := d.FileStart; gIdx < d.FileEnd; gIdx++ {
			file := &st.base.Files[gIdx]
			s.TotalSize += file.Size
			incrStatus(s, file.Ext.BestStatus, file.Size)
			if file.Ext.HasConflict {
				s.Conflict++
			}
		}
	}

	st.recomputeClaimedStatsLocked(reclaims)
}

func (st *ServerTree) recomputeClaimedStatsLocked(reclaims []Reclaim) {
	for i := range st.base.Dirs {
		st.base.Dirs[i].Ext.Claimed = 0
		st.base.Dirs[i].Ext.ClaimedSize = 0
	}
	if len(reclaims) == 0 || len(st.base.Files) == 0 || len(st.base.Dirs) == 0 {
		return
	}

	type ownerClaims struct {
		whitelist map[int32]struct{}
		blacklist map[int32]struct{}
	}
	owners := make(map[string]*ownerClaims)
	for _, reclaim := range reclaims {
		if reclaim.DirID < 0 || reclaim.DirID >= len(st.base.Dirs) {
			log.Printf("tree: skipping reclaim for out-of-range dir_id=%d owner=%s", reclaim.DirID, summarizeWorkerKey(reclaimOwnerKey(reclaim)))
			continue
		}
		ownerKey := reclaimOwnerKey(reclaim)
		claims := owners[ownerKey]
		if claims == nil {
			claims = &ownerClaims{
				whitelist: make(map[int32]struct{}),
				blacklist: make(map[int32]struct{}),
			}
			owners[ownerKey] = claims
		}
		dirID := int32(reclaim.DirID)
		if reclaim.IsBlack {
			claims.blacklist[dirID] = struct{}{}
			continue
		}
		claims.whitelist[dirID] = struct{}{}
	}
	if len(owners) == 0 {
		return
	}

	claimedFiles := make([]bool, len(st.base.Files))
	for _, claims := range owners {
		if len(claims.whitelist) == 0 {
			continue
		}

		dirClaimed := make([]bool, len(st.base.Dirs))
		type state struct {
			dirID       int32
			whitelisted bool
			blocked     bool
		}
		stack := []state{{dirID: 0}}
		for len(stack) > 0 {
			current := stack[len(stack)-1]
			stack = stack[:len(stack)-1]

			if _, ok := claims.blacklist[current.dirID]; ok {
				current.blocked = true
			}
			if _, ok := claims.whitelist[current.dirID]; ok {
				current.whitelisted = true
			}
			dirClaimed[current.dirID] = current.whitelisted && !current.blocked

			for _, childID := range st.base.Dirs[current.dirID].SubDirs {
				stack = append(stack, state{dirID: childID, whitelisted: current.whitelisted, blocked: current.blocked})
			}
		}

		for fileID, file := range st.base.Files {
			if dirClaimed[file.DirIdx] {
				claimedFiles[fileID] = true
			}
		}
	}

	claimedPrefix := make([]int32, len(st.base.Files)+1)
	claimedSizePrefix := make([]int64, len(st.base.Files)+1)
	for fileID, file := range st.base.Files {
		claimedPrefix[fileID+1] = claimedPrefix[fileID]
		claimedSizePrefix[fileID+1] = claimedSizePrefix[fileID]
		if !claimedFiles[fileID] {
			continue
		}
		claimedPrefix[fileID+1]++
		claimedSizePrefix[fileID+1] += file.Size
	}

	for dirID := range st.base.Dirs {
		dir := st.base.Dirs[dirID]
		stats := &st.base.Dirs[dirID].Ext
		stats.Claimed = claimedPrefix[dir.FileEnd] - claimedPrefix[dir.FileStart]
		stats.ClaimedSize = claimedSizePrefix[dir.FileEnd] - claimedSizePrefix[dir.FileStart]
	}
}

func reclaimOwnerKey(reclaim Reclaim) string {
	if reclaim.WorkerKey != "" {
		return reclaim.WorkerKey
	}
	return fmt.Sprintf("worker-id:%d", reclaim.WorkerID)
}

// ChildStats pairs a directory name with its stats.
type ChildStats struct {
	Name  string `json:"name"`
	DirID int32  `json:"dir_id"`
	Stats DirExt `json:"stats"`
}

type TreeClaimWorker struct {
	WorkerID int    `json:"worker_id"`
	Name     string `json:"name"`
}

type TreeClaimGroup struct {
	Count   int               `json:"count"`
	Workers []TreeClaimWorker `json:"workers"`
}

type TreeClaims struct {
	Whitelist TreeClaimGroup `json:"whitelist"`
	Blacklist TreeClaimGroup `json:"blacklist"`
}

func emptyTreeClaims() TreeClaims {
	return TreeClaims{
		Whitelist: TreeClaimGroup{Workers: []TreeClaimWorker{}},
		Blacklist: TreeClaimGroup{Workers: []TreeClaimWorker{}},
	}
}

// ChildDirStats returns stats for all immediate subdirectories of a directory.
func (st *ServerTree) ChildDirStats(dirID int32) []ChildStats {
	st.mu.RLock()
	defer st.mu.RUnlock()

	d := &st.base.Dirs[dirID]
	result := make([]ChildStats, 0, len(d.SubDirs))
	for _, subID := range d.SubDirs {
		result = append(result, ChildStats{
			Name:  st.base.Dirs[subID].Name,
			DirID: subID,
			Stats: st.base.Dirs[subID].Ext,
		})
	}
	return result
}

// TreeNode is a frontend-friendly directory tree node.
type TreeNode struct {
	Name            string     `json:"name"`
	DirID           int32      `json:"dir_id"`
	Path            string     `json:"path"`
	HasChildren     bool       `json:"has_children"`
	TotalFiles      int32      `json:"total_files"`
	ClaimedFiles    int32      `json:"claimed_files"`
	DownloadedFiles int32      `json:"downloaded_files"`
	VerifiedFiles   int32      `json:"verified_files"`
	ArchivedFiles   int32      `json:"archived_files"`
	TotalSize       int64      `json:"total_size"`
	ClaimedSize     int64      `json:"claimed_size"`
	DownloadedSize  int64      `json:"downloaded_size"`
	VerifiedSize    int64      `json:"verified_size"`
	ArchivedSize    int64      `json:"archived_size"`
	Claims          TreeClaims `json:"claims"`
	Children        []TreeNode `json:"children,omitempty"`
}

// BuildTreeNodes returns the sub-directory tree under dirID, up to maxDepth levels.
func (st *ServerTree) BuildTreeNodes(dirID int32, maxDepth int) []TreeNode {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.buildTreeNodesLocked(dirID, maxDepth, 0)
}

func (st *ServerTree) buildTreeNodesLocked(dirID int32, maxDepth, depth int) []TreeNode {
	d := &st.base.Dirs[dirID]
	result := make([]TreeNode, 0, len(d.SubDirs))
	for _, subID := range d.SubDirs {
		s := &st.base.Dirs[subID].Ext
		node := TreeNode{
			Name:            st.base.Dirs[subID].Name,
			DirID:           subID,
			Path:            st.base.Dirs[subID].Path,
			HasChildren:     len(st.base.Dirs[subID].SubDirs) > 0,
			TotalFiles:      s.Total,
			ClaimedFiles:    s.Claimed,
			DownloadedFiles: s.Downloaded,
			VerifiedFiles:   s.Verified,
			ArchivedFiles:   s.Archived,
			TotalSize:       s.TotalSize,
			ClaimedSize:     s.ClaimedSize,
			DownloadedSize:  s.DownloadedSize,
			VerifiedSize:    s.VerifiedSize,
			ArchivedSize:    s.ArchivedSize,
			Claims:          emptyTreeClaims(),
		}
		if depth < maxDepth-1 && len(st.base.Dirs[subID].SubDirs) > 0 {
			node.Children = st.buildTreeNodesLocked(subID, maxDepth, depth+1)
		}
		result = append(result, node)
	}
	return result
}

// StatsToOverview converts root DirExt into the overview the frontend expects.
func StatsToOverview(s DirExt) map[string]any {
	return map[string]any{
		"total_files":      s.Total,
		"total_size":       s.TotalSize,
		"claimed_files":    s.Claimed,
		"claimed_size":     s.ClaimedSize,
		"downloaded_files": s.Downloaded,
		"downloaded_size":  s.DownloadedSize,
		"verified_files":   s.Verified,
		"verified_size":    s.VerifiedSize,
		"archived_files":   s.Archived,
		"archived_size":    s.ArchivedSize,
	}
}
