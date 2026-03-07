package server

import (
	"bytes"
	"sync"

	mt "myrient-horizon/pkg/myrienttree"
	"myrient-horizon/pkg/protocol"
)

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

type FileExt struct {
	BestStatus  protocol.TaskStatus
	ReportCount uint8
	HasConflict bool
}

type ServerTree struct {
	mu         sync.RWMutex
	base       *mt.Tree[DirExt, FileExt]
	fileHashes []map[int][]byte
}

var (
	treeInstance *ServerTree
	treeOnce     sync.Once
)

func InitTree(base *mt.Tree[DirExt, FileExt]) *ServerTree {
	treeOnce.Do(func() {
		treeInstance = newServerTree(base)
	})
	return treeInstance
}

func GetTree() *ServerTree {
	if treeInstance == nil {
		panic("server: Tree not initialized")
	}
	return treeInstance
}

func LoadTree(path string) (*ServerTree, error) {
	tree, err := mt.LoadFromFile[DirExt, FileExt](path)
	if err != nil {
		return nil, err
	}
	return InitTree(tree), nil
}

func newServerTree(base *mt.Tree[DirExt, FileExt]) *ServerTree {
	st := &ServerTree{
		base:       base,
		fileHashes: make([]map[int][]byte, len(base.Files)),
	}
	st.initTotals()
	return st
}

func (st *ServerTree) Base() *mt.Tree[DirExt, FileExt] {
	return st.base
}

func (st *ServerTree) GetDirStats(dirID int32) DirExt {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.base.Dirs[dirID].Ext
}

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

func (st *ServerTree) ApplyReport(fileID int64, workerID int, status protocol.TaskStatus, sha1 []byte) {
	st.mu.Lock()
	defer st.mu.Unlock()

	if fileID < 0 || fileID >= int64(len(st.base.Files)) {
		return
	}

	file := &st.base.Files[fileID]
	oldStatus := file.Ext.BestStatus

	if sha1 != nil {
		if st.fileHashes[fileID] == nil {
			st.fileHashes[fileID] = make(map[int][]byte)
		}
		st.fileHashes[fileID][workerID] = sha1
	}

	if st.fileHashes[fileID] != nil {
		file.Ext.ReportCount = uint8(len(st.fileHashes[fileID]))
	} else if file.Ext.ReportCount == 0 {
		file.Ext.ReportCount = 1
	}

	if status > file.Ext.BestStatus {
		file.Ext.BestStatus = status
	}

	file.Ext.HasConflict = st.hasConflictLocked(fileID)
	st.bubbleDelta(file.DirIdx, oldStatus, file.Ext.BestStatus, file.Ext.HasConflict, file.Size)
}

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

func (st *ServerTree) bubbleDelta(dirID int32, oldStatus, newStatus protocol.TaskStatus, hasConflict bool, fileSize int64) {
	if oldStatus == newStatus {
		return
	}
	for id := dirID; id >= 0; id = st.base.Dirs[id].ParentIdx {
		s := &st.base.Dirs[id].Ext
		decrStatus(s, oldStatus, fileSize)
		incrStatus(s, newStatus, fileSize)
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

type ChildStats struct {
	Name  string `json:"name"`
	DirID int32  `json:"dir_id"`
	Stats DirExt `json:"stats"`
}

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
