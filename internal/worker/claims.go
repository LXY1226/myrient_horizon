package worker

import (
	"context"
	"log"
	"os"
	"sort"
	"sync"

	"myrient-horizon/pkg/protocol"
)

type claimDecision string

const (
	claimAllowed        claimDecision = "allowed"
	claimNoWhitelist    claimDecision = "no whitelist claims configured"
	claimNotWhitelisted claimDecision = "directory is outside whitelist claims"
	claimBlacklisted    claimDecision = "directory is covered by a blacklist claim"
)

type claimState struct {
	mu        sync.RWMutex
	claims    protocol.ReclaimsSyncMsg
	whitelist map[int32]struct{}
	blacklist map[int32]struct{}
	planGen   uint32
}

var Claims = newClaimState()

func newClaimState() *claimState {
	return &claimState{
		whitelist: make(map[int32]struct{}),
		blacklist: make(map[int32]struct{}),
	}
}

func (c *claimState) RestoreCached(ctx context.Context) {
	if WorkerState == nil {
		return
	}
	c.apply(ctx, WorkerState.CachedClaims(), false)
}

func (c *claimState) Replace(reclaims protocol.ReclaimsSyncMsg) {
	c.apply(context.Background(), reclaims, true)
}

func (c *claimState) apply(ctx context.Context, reclaims protocol.ReclaimsSyncMsg, persist bool) {
	normalized := normalizeClaims(reclaims)
	whitelist, blacklist := splitClaims(normalized)

	c.mu.Lock()
	if claimsEqual(c.claims, normalized) {
		c.mu.Unlock()
		return
	}
	c.claims = copyClaims(normalized)
	c.whitelist = whitelist
	c.blacklist = blacklist
	c.planGen++
	planGen := c.planGen
	c.mu.Unlock()

	if persist && WorkerState != nil {
		WorkerState.UpdateClaims(normalized)
	}

	log.Printf("claims: replaced runtime rules: %d whitelist, %d blacklist", len(whitelist), len(blacklist))
	if Downloader != nil {
		drained := Downloader.ResetPendingManaged()
		if drained > 0 {
			log.Printf("claims: cleared %d pending managed download(s)", drained)
		}
	}
	if len(whitelist) == 0 {
		log.Println("claims: worker has no whitelist claims; managed downloads will stay idle")
		return
	}
	if Downloader == nil || Verifier == nil || iTree == nil {
		return
	}
	if err := c.rebuild(ctx, planGen); err != nil {
		log.Printf("claims: rebuild failed: %v", err)
	}
}

func splitClaims(reclaims protocol.ReclaimsSyncMsg) (map[int32]struct{}, map[int32]struct{}) {
	whitelist := make(map[int32]struct{})
	blacklist := make(map[int32]struct{})
	for _, reclaim := range reclaims {
		if reclaim.IsBlack {
			blacklist[reclaim.DirID] = struct{}{}
			continue
		}
		whitelist[reclaim.DirID] = struct{}{}
	}
	return whitelist, blacklist
}

func (c *claimState) rebuild(ctx context.Context, planGen uint32) error {
	c.mu.RLock()
	whitelist := make([]int32, 0, len(c.whitelist))
	blacklist := make(map[int32]struct{}, len(c.blacklist))
	for dirID := range c.whitelist {
		whitelist = append(whitelist, dirID)
	}
	for dirID := range c.blacklist {
		blacklist[dirID] = struct{}{}
	}
	c.mu.RUnlock()

	sort.Slice(whitelist, func(i, j int) bool { return whitelist[i] < whitelist[j] })
	completed := map[int32]struct{}{}
	if WorkerState != nil {
		completed = WorkerState.CompletedDirSet()
	}

	for _, dirID := range whitelist {
		if err := c.walkDir(ctx, dirID, planGen, blacklist, completed); err != nil {
			return err
		}
	}
	return nil
}

func (c *claimState) walkDir(ctx context.Context, dirID int32, planGen uint32, blacklist, completed map[int32]struct{}) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, blocked := blacklist[dirID]; blocked {
		return nil
	}
	if _, done := completed[dirID]; done {
		return nil
	}

	dir := iTree.Dirs[dirID]
	for fileID := dir.FileStart; fileID < dir.FileEnd; fileID++ {
		if iTree.Files[fileID].DirIdx != dirID {
			continue
		}
		if err := c.planFile(ctx, fileID, planGen); err != nil {
			return err
		}
	}
	for _, childID := range dir.SubDirs {
		if err := c.walkDir(ctx, childID, planGen, blacklist, completed); err != nil {
			return err
		}
	}
	return nil
}

func (c *claimState) planFile(ctx context.Context, fileID int32, planGen uint32) error {
	if !markFileVisited(fileID, planGen) {
		return nil
	}
	if fileBusyOrConfirmed(fileID) {
		return nil
	}

	finalPath := finalLocalPath(fileID)
	downloadingPath := downloadingLocalPath(fileID)
	downloadedPath := downloadedLocalPath(fileID)
	if _, err := os.Stat(downloadingPath); err == nil {
		setFileQueuedDownload(fileID, true)
		task := Task{FileID: fileID, LocalPath: downloadingPath, Managed: true}
		if err := Downloader.Submit(ctx, task); err != nil {
			setFileQueuedDownload(fileID, false)
			return err
		}
		return nil
	}
	if _, err := os.Stat(downloadingPath + ".aria2"); err == nil {
		setFileQueuedDownload(fileID, true)
		task := Task{FileID: fileID, LocalPath: downloadingPath, Managed: true}
		if err := Downloader.Submit(ctx, task); err != nil {
			setFileQueuedDownload(fileID, false)
			return err
		}
		return nil
	}
	if _, err := os.Stat(downloadedPath); err == nil {
		setFileQueuedVerify(fileID, true)
		Verifier.Submit(&Task{FileID: fileID, LocalPath: downloadedPath, Managed: true})
		return nil
	}
	if _, err := os.Stat(finalPath); err == nil {
		setFileQueuedVerify(fileID, true)
		Verifier.Submit(&Task{FileID: fileID, LocalPath: finalPath, Managed: true})
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	setFileQueuedDownload(fileID, true)
	task := Task{FileID: fileID, LocalPath: downloadingPath, Managed: true}
	if err := Downloader.Submit(ctx, task); err != nil {
		setFileQueuedDownload(fileID, false)
		return err
	}
	return nil
}

func (c *claimState) evaluateDir(dirID int32) claimDecision {
	c.mu.RLock()
	whitelist := c.whitelist
	blacklist := c.blacklist
	c.mu.RUnlock()

	if len(whitelist) == 0 {
		return claimNoWhitelist
	}

	allowed := false
	for dirID >= 0 {
		if _, blocked := blacklist[dirID]; blocked {
			return claimBlacklisted
		}
		if _, ok := whitelist[dirID]; ok {
			allowed = true
		}
		dirID = iTree.Dirs[dirID].ParentIdx
	}

	if !allowed {
		return claimNotWhitelisted
	}
	return claimAllowed
}

func (c *claimState) AllowsTask(task *Task) (bool, string) {
	if task == nil || !task.Managed {
		return true, string(claimAllowed)
	}
	decision := c.evaluateDir(task.GetFile().DirIdx)
	return decision == claimAllowed, string(decision)
}

func confirmManagedFile(fileID int32) {
	if iTree == nil || fileID < 0 || int(fileID) >= len(iTree.Files) {
		return
	}
	completed := map[int32]struct{}{}
	if WorkerState != nil {
		completed = WorkerState.CompletedDirSet()
	}

	treeStateMu.Lock()
	ext := &iTree.Files[fileID].Ext
	ext.confirmedDone = true
	ext.queuedVerify = false
	ext.queuedDownload = false

	for dirID := iTree.Files[fileID].DirIdx; dirID >= 0; dirID = iTree.Dirs[dirID].ParentIdx {
		if !dirCompleteLocked(dirID, completed) {
			break
		}
		completed[dirID] = struct{}{}
		pruneDescendantCompletedLocked(dirID, completed)
	}
	treeStateMu.Unlock()

	if WorkerState != nil {
		WorkerState.UpdateCompletedDirIDs(dirIDsFromSet(completed))
	}
}

func dirCompleteLocked(dirID int32, completed map[int32]struct{}) bool {
	if _, ok := completed[dirID]; ok {
		return true
	}
	dir := iTree.Dirs[dirID]
	for fileID := dir.FileStart; fileID < dir.FileEnd; fileID++ {
		if iTree.Files[fileID].DirIdx != dirID {
			continue
		}
		if !iTree.Files[fileID].Ext.confirmedDone {
			return false
		}
	}
	for _, childID := range dir.SubDirs {
		if !dirCompleteLocked(childID, completed) {
			return false
		}
	}
	return true
}

func pruneDescendantCompletedLocked(dirID int32, completed map[int32]struct{}) {
	for _, childID := range iTree.Dirs[dirID].SubDirs {
		delete(completed, childID)
		pruneDescendantCompletedLocked(childID, completed)
	}
}

func dirIDsFromSet(set map[int32]struct{}) []int32 {
	if len(set) == 0 {
		return nil
	}
	dirIDs := make([]int32, 0, len(set))
	for dirID := range set {
		dirIDs = append(dirIDs, dirID)
	}
	sort.Slice(dirIDs, func(i, j int) bool { return dirIDs[i] < dirIDs[j] })
	return dirIDs
}
