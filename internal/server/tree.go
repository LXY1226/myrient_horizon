package server

import (
	"bytes"
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
	BestStatus  uint8
	ReportCount uint8
	HasConflict bool
}

// ServerTree wraps the base tree with a mutex for thread-safe access.
// All mutable state is stored in DirNode.Ext (DirExt) and FileNode.Ext (FileExt).
type ServerTree struct {
	mu   sync.RWMutex
	base *mt.Tree[DirExt, FileExt]
	// Per-file: track known SHA1 per worker for conflict detection.
	// Key: global file index. Value: map[workerID]sha1Bytes.
	fileHashes []map[int][]byte
}

// New creates a ServerTree from a base tree.
func New(base *mt.Tree[DirExt, FileExt]) *ServerTree {
	st := &ServerTree{
		base:       base,
		fileHashes: make([]map[int][]byte, len(base.Files)),
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
func (st *ServerTree) ApplyReport(fileID int64, workerID int, status uint8, sha1 []byte) {
	st.mu.Lock()
	defer st.mu.Unlock()

	if fileID < 0 || fileID >= int64(len(st.base.Files)) {
		return
	}

	file := &st.base.Files[fileID]
	oldStatus := file.Ext.BestStatus

	// Track per-worker SHA1 for conflict detection.
	if sha1 != nil {
		if st.fileHashes[fileID] == nil {
			st.fileHashes[fileID] = make(map[int][]byte)
		}
		st.fileHashes[fileID][workerID] = sha1
	}

	// Update ReportCount.
	if st.fileHashes[fileID] != nil {
		file.Ext.ReportCount = uint8(len(st.fileHashes[fileID]))
	} else {
		if file.Ext.ReportCount == 0 {
			file.Ext.ReportCount = 1
		}
	}

	// Update BestStatus (take highest).
	if status > file.Ext.BestStatus {
		file.Ext.BestStatus = status
	}

	// Check for SHA1 conflicts.
	file.Ext.HasConflict = st.hasConflictLocked(fileID)

	// Get dirID from file
	dirID := file.DirIdx

	// Bubble dirExt delta up the ancestor chain.
	st.bubbleDelta(dirID, oldStatus, file.Ext.BestStatus, file.Ext.HasConflict, file.Size)
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
func (st *ServerTree) bubbleDelta(dirID int32, oldStatus, newStatus uint8, hasConflict bool, fileSize int64) {
	if oldStatus == newStatus {
		return
	}
	for id := dirID; id >= 0; id = st.base.Dirs[id].ParentIdx {
		s := &st.base.Dirs[id].Ext
		decrStatus(s, oldStatus, fileSize)
		incrStatus(s, newStatus, fileSize)
	}
}

func decrStatus(s *DirExt, status uint8, size int64) {
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

func incrStatus(s *DirExt, status uint8, size int64) {
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
func (st *ServerTree) recomputeAllStatsLocked() {
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
}

// ChildStats pairs a directory name with its stats.
type ChildStats struct {
	Name  string `json:"name"`
	DirID int32  `json:"dir_id"`
	Stats DirExt `json:"stats"`
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
