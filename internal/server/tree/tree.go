package tree

import (
	"bytes"
	"context"
	"log"
	"sync"

	"myrient-horizon/internal/server/db"
	mt "myrient-horizon/pkg/myrienttree"
	"myrient-horizon/pkg/protocol"
)

// DirStats holds aggregated file counts for a directory (including all descendants).
type DirStats struct {
	Total       int32 `json:"total"`
	Downloading int32 `json:"downloading"`
	Downloaded  int32 `json:"downloaded"`
	Verifying   int32 `json:"verifying"`
	Verified    int32 `json:"verified"`
	Failed      int32 `json:"failed"`
	Conflict    int32 `json:"conflict"`
}

// ServerTree wraps the base tree and adds mutable aggregation state.
type ServerTree struct {
	mu   sync.RWMutex
	base *mt.Tree
	// Per-directory aggregated stats. Index = dirID.
	stats []DirStats
	// Per-file: track known SHA1 per worker for conflict detection.
	// Key: global file index. Value: map[workerID]sha1Bytes.
	fileHashes []map[int][]byte
}

// New creates a ServerTree from a base tree.
func New(base *mt.Tree) *ServerTree {
	st := &ServerTree{
		base:       base,
		stats:      make([]DirStats, len(base.Dirs)),
		fileHashes: make([]map[int][]byte, len(base.Files)),
	}
	// Initialize Total counts by walking the tree bottom-up.
	st.initTotals()
	return st
}

// Base returns the underlying tree for read-only access.
func (st *ServerTree) Base() *mt.Tree {
	return st.base
}

// GetDirStats returns a copy of the stats for a directory.
func (st *ServerTree) GetDirStats(dirID int32) DirStats {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.stats[dirID]
}

// GetFileNode returns a copy of a file node.
func (st *ServerTree) GetFileNode(globalIdx int32) mt.FileNode {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.base.Files[globalIdx]
}

// initTotals computes Total file counts for each directory (direct + all descendants).
func (st *ServerTree) initTotals() {
	// Bottom-up: process in reverse DFS order (children before parents).
	for i := len(st.base.Dirs) - 1; i >= 0; i-- {
		d := &st.base.Dirs[i]
		direct := d.FileEnd - d.FileStart
		st.stats[i].Total = direct
		for _, subID := range d.SubDirs {
			st.stats[i].Total += st.stats[subID].Total
		}
	}
}

// RecoverFromDB replays all file_status records to rebuild in-memory state.
// Records are streamed one-by-one to avoid buffering the entire table in memory.
func (st *ServerTree) RecoverFromDB(ctx context.Context, store *db.Store) error {
	st.mu.Lock()
	defer st.mu.Unlock()

	var count int
	err := store.ScanAllFileStatus(ctx, func(r *db.FileStatus) error {
		st.applyReportLocked(r.DirID, r.FileIdx, int(r.WorkerID), uint8(r.Status), r.SHA1)
		count++
		return nil
	})
	if err != nil {
		return err
	}
	log.Printf("tree: recovered from %d file_status records", count)

	// Recompute all DirStats from scratch after bulk load.
	st.recomputeAllStatsLocked()
	return nil
}

// ApplyReport processes a single file status report: updates file state, detects conflicts, bubbles stats.
func (st *ServerTree) ApplyReport(dirID, fileIdx int32, workerID int, status uint8, sha1 []byte) {
	st.mu.Lock()
	defer st.mu.Unlock()

	oldStatus := st.applyReportLocked(dirID, fileIdx, workerID, status, sha1)
	file := &st.base.Files[st.base.FileGlobalIndex(dirID, fileIdx)]

	// Bubble DirStats delta up the ancestor chain.
	st.bubbleDelta(dirID, oldStatus, file.BestStatus, file.HasConflict)
}

// applyReportLocked updates a file's aggregated state. Returns the old BestStatus.
// Caller must hold st.mu write lock.
func (st *ServerTree) applyReportLocked(dirID, fileIdx int32, workerID int, status uint8, sha1 []byte) uint8 {
	gIdx := st.base.FileGlobalIndex(dirID, fileIdx)
	if gIdx < 0 || gIdx >= int32(len(st.base.Files)) {
		return 0
	}
	file := &st.base.Files[gIdx]
	oldBest := file.BestStatus
	wasConflict := file.HasConflict

	// Track per-worker SHA1 for conflict detection.
	if sha1 != nil {
		if st.fileHashes[gIdx] == nil {
			st.fileHashes[gIdx] = make(map[int][]byte)
		}
		st.fileHashes[gIdx][workerID] = sha1
	}

	// Update ReportCount.
	if st.fileHashes[gIdx] != nil {
		file.ReportCount = uint8(len(st.fileHashes[gIdx]))
	} else {
		// First report without SHA1 (e.g., downloaded but not yet verified).
		if file.ReportCount == 0 {
			file.ReportCount = 1
		}
	}

	// Update BestStatus (take highest).
	if status > file.BestStatus {
		file.BestStatus = status
	}

	// Check for SHA1 conflicts.
	file.HasConflict = st.hasConflictLocked(gIdx)

	// If conflict status changed during bulk recovery, we'll handle it in recomputeAllStats.
	_ = wasConflict
	_ = oldBest

	return oldBest
}

// hasConflictLocked checks if a file has SHA1 conflicts among reporters.
func (st *ServerTree) hasConflictLocked(gIdx int32) bool {
	hashes := st.fileHashes[gIdx]
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

// bubbleDelta updates DirStats along the ancestor chain when a file's status changes.
func (st *ServerTree) bubbleDelta(dirID int32, oldStatus, newStatus uint8, hasConflict bool) {
	if oldStatus == newStatus {
		return
	}
	for id := dirID; id >= 0; id = st.base.Dirs[id].ParentIdx {
		s := &st.stats[id]
		decrStatus(s, oldStatus)
		incrStatus(s, newStatus)
	}
	// Conflict counting handled in recompute or inline.
}

func decrStatus(s *DirStats, status uint8) {
	switch status {
	case protocol.StatusDownloading:
		s.Downloading--
	case protocol.StatusDownloaded:
		s.Downloaded--
	case protocol.StatusVerifying:
		s.Verifying--
	case protocol.StatusVerified:
		s.Verified--
	case protocol.StatusFailed:
		s.Failed--
	}
}

func incrStatus(s *DirStats, status uint8) {
	switch status {
	case protocol.StatusDownloading:
		s.Downloading++
	case protocol.StatusDownloaded:
		s.Downloaded++
	case protocol.StatusVerifying:
		s.Verifying++
	case protocol.StatusVerified:
		s.Verified++
	case protocol.StatusFailed:
		s.Failed++
	}
}

// recomputeAllStatsLocked rebuilds all DirStats from scratch.
// Used after bulk recovery to ensure consistency. Caller must hold write lock.
func (st *ServerTree) recomputeAllStatsLocked() {
	// Reset all stats.
	for i := range st.stats {
		st.stats[i] = DirStats{}
	}

	// Count direct files per directory.
	for i := range st.base.Dirs {
		d := &st.base.Dirs[i]
		for gIdx := d.FileStart; gIdx < d.FileEnd; gIdx++ {
			file := &st.base.Files[gIdx]
			s := &st.stats[i]
			incrStatus(s, file.BestStatus)
			if file.HasConflict {
				s.Conflict++
			}
		}
	}

	// Sum up children (bottom-up).
	for i := len(st.base.Dirs) - 1; i >= 0; i-- {
		d := &st.base.Dirs[i]
		direct := d.FileEnd - d.FileStart
		st.stats[i].Total = direct
		for _, subID := range d.SubDirs {
			sub := &st.stats[subID]
			s := &st.stats[i]
			s.Total += sub.Total
			s.Downloading += sub.Downloading
			s.Downloaded += sub.Downloaded
			s.Verifying += sub.Verifying
			s.Verified += sub.Verified
			s.Failed += sub.Failed
			s.Conflict += sub.Conflict
		}
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
			Stats: st.stats[subID],
		})
	}
	return result
}

// ChildStats pairs a directory name with its stats.
type ChildStats struct {
	Name  string   `json:"name"`
	DirID int32    `json:"dir_id"`
	Stats DirStats `json:"stats"`
}
