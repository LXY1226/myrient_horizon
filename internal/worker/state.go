package worker

import (
	"sync"

	"myrient-horizon/pkg/protocol"
)

// StateManager维护worker的内存状态
type StateManager struct {
	mu     sync.RWMutex
	status map[int64]uint8 // file_id -> status
}

func NewStateManager() *StateManager {
	return &StateManager{status: make(map[int64]uint8)}
}

// LoadFromServer 从服务器同步全量状态
func (sm *StateManager) LoadFromServer(records []protocol.ItemStatus) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	for _, r := range records {
		sm.status[r.FileID] = r.Status
	}
}

// GetStatus 获取文件状态
func (sm *StateManager) GetStatus(fileID int64) uint8 {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.status[fileID]
}

// IsDone 检查文件是否已完成（verified或archived）
func (sm *StateManager) IsDone(fileID int64) bool {
	status := sm.GetStatus(fileID)
	return status == protocol.StatusVerified || status == protocol.StatusArchived
}

// UpdateStatus 更新文件状态
func (sm *StateManager) UpdateStatus(fileID int64, status uint8) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.status[fileID] = status
}
