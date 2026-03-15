package worker

import (
	"log"
	"sort"
	"sync"

	"myrient-horizon/internal/worker/config"
	"myrient-horizon/pkg/protocol"
)

type workerStateStore struct {
	mu              sync.RWMutex
	dir             string
	claims          protocol.ReclaimsSyncMsg
	completedDirIDs []int32
}

var WorkerState *workerStateStore

func InitWorkerState(dir string, cfg *config.WorkerConfig) {
	state := &workerStateStore{dir: dir}
	if cfg != nil {
		state.claims = copyClaims(normalizeClaims(cfg.RuntimeState.Claims))
		state.completedDirIDs = normalizeDirIDs(cfg.RuntimeState.CompletedDirIDs)
	}
	WorkerState = state
}

func (s *workerStateStore) CachedClaims() protocol.ReclaimsSyncMsg {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return copyClaims(s.claims)
}

func (s *workerStateStore) CompletedDirSet() map[int32]struct{} {
	result := make(map[int32]struct{})
	if s == nil {
		return result
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, dirID := range s.completedDirIDs {
		result[dirID] = struct{}{}
	}
	return result
}

func (s *workerStateStore) UpdateClaims(reclaims protocol.ReclaimsSyncMsg) bool {
	if s == nil {
		return false
	}
	normalized := normalizeClaims(reclaims)

	s.mu.Lock()
	defer s.mu.Unlock()
	if claimsEqual(s.claims, normalized) {
		return false
	}
	s.claims = copyClaims(normalized)
	if err := s.saveLocked(); err != nil {
		log.Printf("worker: failed to persist cached claims: %v", err)
	}
	return true
}

func (s *workerStateStore) UpdateCompletedDirIDs(dirIDs []int32) bool {
	if s == nil {
		return false
	}
	normalized := normalizeDirIDs(dirIDs)

	s.mu.Lock()
	defer s.mu.Unlock()
	if int32SlicesEqual(s.completedDirIDs, normalized) {
		return false
	}
	s.completedDirIDs = append(s.completedDirIDs[:0], normalized...)
	if err := s.saveLocked(); err != nil {
		log.Printf("worker: failed to persist completed directories: %v", err)
	}
	return true
}

func (s *workerStateStore) saveLocked() error {
	cfg := config.Global
	cfg.RuntimeState = config.RuntimeState{
		Claims:          copyClaims(s.claims),
		CompletedDirIDs: append([]int32(nil), s.completedDirIDs...),
	}
	if err := config.Save(s.dir, &cfg); err != nil {
		return err
	}
	config.Global.RuntimeState = cfg.RuntimeState
	return nil
}

func normalizeClaims(reclaims protocol.ReclaimsSyncMsg) protocol.ReclaimsSyncMsg {
	if len(reclaims) == 0 {
		return nil
	}
	normalized := copyClaims(reclaims)
	sort.Slice(normalized, func(i, j int) bool {
		if normalized[i].DirID != normalized[j].DirID {
			return normalized[i].DirID < normalized[j].DirID
		}
		if normalized[i].IsBlack == normalized[j].IsBlack {
			return false
		}
		return !normalized[i].IsBlack && normalized[j].IsBlack
	})

	result := normalized[:0]
	for _, reclaim := range normalized {
		if len(result) > 0 && result[len(result)-1] == reclaim {
			continue
		}
		result = append(result, reclaim)
	}
	return append(protocol.ReclaimsSyncMsg(nil), result...)
}

func normalizeDirIDs(dirIDs []int32) []int32 {
	if len(dirIDs) == 0 {
		return nil
	}
	normalized := append([]int32(nil), dirIDs...)
	sort.Slice(normalized, func(i, j int) bool { return normalized[i] < normalized[j] })
	result := normalized[:0]
	for _, dirID := range normalized {
		if len(result) > 0 && result[len(result)-1] == dirID {
			continue
		}
		result = append(result, dirID)
	}
	return append([]int32(nil), result...)
}

func copyClaims(reclaims protocol.ReclaimsSyncMsg) protocol.ReclaimsSyncMsg {
	if len(reclaims) == 0 {
		return nil
	}
	return append(protocol.ReclaimsSyncMsg(nil), reclaims...)
}

func claimsEqual(left, right protocol.ReclaimsSyncMsg) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func int32SlicesEqual(left, right []int32) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
