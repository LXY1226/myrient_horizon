package worker

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"myrient-horizon/internal/worker/config"
	mt "myrient-horizon/pkg/myrienttree"
	"myrient-horizon/pkg/protocol"
)

func TestRestoreCachedClaimsRebuildsQueues(t *testing.T) {
	tempDir := setupWorkerRuntimeTest(t)
	parentPath := finalLocalPath(0)
	if err := os.MkdirAll(filepath.Dir(parentPath), 0o755); err != nil {
		t.Fatalf("mkdir parent path: %v", err)
	}
	if err := os.WriteFile(parentPath, []byte("parent"), 0o644); err != nil {
		t.Fatalf("write parent file: %v", err)
	}

	WorkerState = &workerStateStore{
		dir: tempDir,
		claims: protocol.ReclaimsSyncMsg{
			{DirID: 1},
			{DirID: 2, IsBlack: true},
		},
	}

	Claims = newClaimState()
	Claims.RestoreCached(context.Background())

	if got := len(Verifier.taskCh); got != 1 {
		t.Fatalf("verify queue len = %d, want 1", got)
	}
	verifyTask := <-Verifier.taskCh
	if verifyTask.FileID != 0 || verifyTask.LocalPath != parentPath {
		t.Fatalf("verify task = %+v, want file 0 path %q", verifyTask, parentPath)
	}

	if got := len(Downloader.taskCh); got != 1 {
		t.Fatalf("download queue len = %d, want 1", got)
	}
	downloadTask := <-Downloader.taskCh
	if downloadTask.FileID != 2 {
		t.Fatalf("download task file = %d, want 2", downloadTask.FileID)
	}
}

func TestClaimReplaceClearsPendingDownloadsKeepsVerifyQueue(t *testing.T) {
	setupWorkerRuntimeTest(t)
	parentPath := finalLocalPath(0)
	if err := os.MkdirAll(filepath.Dir(parentPath), 0o755); err != nil {
		t.Fatalf("mkdir parent path: %v", err)
	}
	if err := os.WriteFile(parentPath, []byte("parent"), 0o644); err != nil {
		t.Fatalf("write parent file: %v", err)
	}

	Claims.Replace(protocol.ReclaimsSyncMsg{
		{DirID: 1},
		{DirID: 2, IsBlack: true},
	})
	if got := len(Verifier.taskCh); got != 1 {
		t.Fatalf("verify queue len after initial rebuild = %d, want 1", got)
	}
	if got := len(Downloader.taskCh); got != 1 {
		t.Fatalf("download queue len after initial rebuild = %d, want 1", got)
	}

	Claims.Replace(nil)

	if got := len(Verifier.taskCh); got != 1 {
		t.Fatalf("verify queue len after replace = %d, want 1", got)
	}
	if got := len(Downloader.taskCh); got != 0 {
		t.Fatalf("download queue len after replace = %d, want 0", got)
	}
}

func TestPlanFileTreatsDownloadingAria2AsPendingDownload(t *testing.T) {
	setupWorkerRuntimeTest(t)
	parentPath := finalLocalPath(0)
	if err := os.MkdirAll(filepath.Dir(parentPath), 0o755); err != nil {
		t.Fatalf("mkdir parent path: %v", err)
	}
	if err := os.WriteFile(parentPath, []byte("partial"), 0o644); err != nil {
		t.Fatalf("write parent file: %v", err)
	}
	if err := os.WriteFile(downloadingLocalPath(0)+".aria2", []byte("control"), 0o644); err != nil {
		t.Fatalf("write aria2 control file: %v", err)
	}

	Claims.Replace(protocol.ReclaimsSyncMsg{{DirID: 1}, {DirID: 2, IsBlack: true}})

	if got := len(Verifier.taskCh); got != 0 {
		t.Fatalf("verify queue len = %d, want 0", got)
	}
	if got := len(Downloader.taskCh); got != 2 {
		t.Fatalf("download queue len = %d, want 2", got)
	}

	first := <-Downloader.taskCh
	second := <-Downloader.taskCh
	if first.FileID != 0 && second.FileID != 0 {
		t.Fatalf("download queue files = [%d %d], want one task for file 0", first.FileID, second.FileID)
	}
}

func TestPlanFileTreatsDownloadingWithoutAria2AsPendingDownload(t *testing.T) {
	setupWorkerRuntimeTest(t)
	path := downloadingLocalPath(0)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir downloading path: %v", err)
	}
	if err := os.WriteFile(path, []byte("complete"), 0o644); err != nil {
		t.Fatalf("write downloading path: %v", err)
	}

	Claims.Replace(protocol.ReclaimsSyncMsg{{DirID: 1}, {DirID: 2, IsBlack: true}})

	if got := len(Downloader.taskCh); got != 2 {
		t.Fatalf("download queue len = %d, want 2", got)
	}
	first := <-Downloader.taskCh
	second := <-Downloader.taskCh
	if first.FileID != 0 && second.FileID != 0 {
		t.Fatalf("download queue files = [%d %d], want one task for file 0", first.FileID, second.FileID)
	}
	if got := len(Verifier.taskCh); got != 0 {
		t.Fatalf("verify queue len = %d, want 0", got)
	}
}

func TestPlanFileTreatsDownloadedFileAsPendingVerify(t *testing.T) {
	setupWorkerRuntimeTest(t)
	path := downloadedLocalPath(0)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir downloaded path: %v", err)
	}
	if err := os.WriteFile(path, []byte("verified"), 0o644); err != nil {
		t.Fatalf("write downloaded path: %v", err)
	}

	Claims.Replace(protocol.ReclaimsSyncMsg{{DirID: 1}, {DirID: 2, IsBlack: true}})

	if got := len(Downloader.taskCh); got != 1 {
		t.Fatalf("download queue len = %d, want 1", got)
	}
	_ = <-Downloader.taskCh
	if got := len(Verifier.taskCh); got != 1 {
		t.Fatalf("verify queue len = %d, want 1", got)
	}
	verifyTask := <-Verifier.taskCh
	if verifyTask.FileID != 0 || verifyTask.LocalPath != path {
		t.Fatalf("verify task = %+v, want file 0 path %q", verifyTask, path)
	}
}

func TestConfirmManagedFileFoldsToLargestCompletedDirectory(t *testing.T) {
	setupWorkerRuntimeTest(t)

	confirmManagedFile(1)
	if got := WorkerState.CompletedDirSet(); len(got) != 1 {
		t.Fatalf("completed dirs after first child = %v, want one dir", got)
	}

	confirmManagedFile(2)
	confirmManagedFile(0)

	completed := WorkerState.CompletedDirSet()
	if len(completed) != 1 {
		t.Fatalf("completed dirs = %v, want one folded dir", completed)
	}
	if _, ok := completed[1]; !ok {
		t.Fatalf("completed dirs = %v, want dir 1", completed)
	}
}

func TestUpdateStateSkipsUnchangedValues(t *testing.T) {
	tempDir := setupWorkerRuntimeTest(t)
	WorkerState = &workerStateStore{
		dir:             tempDir,
		claims:          protocol.ReclaimsSyncMsg{{DirID: 1}},
		completedDirIDs: []int32{2},
	}

	if changed := WorkerState.UpdateClaims(protocol.ReclaimsSyncMsg{{DirID: 1}}); changed {
		t.Fatal("UpdateClaims() changed = true, want false")
	}
	if changed := WorkerState.UpdateCompletedDirIDs([]int32{2}); changed {
		t.Fatal("UpdateCompletedDirIDs() changed = true, want false")
	}
}

func setupWorkerRuntimeTest(t *testing.T) string {
	t.Helper()

	oldTree := iTree
	oldTreeSHA := TreeSHA1
	oldClaims := Claims
	oldDownloader := Downloader
	oldVerifier := Verifier
	oldWorkerState := WorkerState
	oldConfig := config.Global

	tempDir := t.TempDir()
	config.Global = config.WorkerConfig{
		Key:         "mh_test",
		ServerURL:   "https://example.test/api",
		DownloadDir: tempDir,
	}
	iTree = testWorkerTree()
	TreeSHA1 = "test"
	Claims = newClaimState()
	Verifier = &verifierRuntime{taskCh: make(chan *Task, 16)}
	Downloader = &downloader{
		taskCh:  make(chan Task, 16),
		gidMap:  make(map[string]*Task),
		pathGID: make(map[string]string),
		pending: make(map[string]struct{}),
	}
	WorkerState = &workerStateStore{dir: tempDir}

	t.Cleanup(func() {
		iTree = oldTree
		TreeSHA1 = oldTreeSHA
		Claims = oldClaims
		Downloader = oldDownloader
		Verifier = oldVerifier
		WorkerState = oldWorkerState
		config.Global = oldConfig
	})

	return tempDir
}

func testWorkerTree() *mt.Tree[dirExt, fileExt] {
	return &mt.Tree[dirExt, fileExt]{
		Dirs: []mt.DirNode[dirExt]{
			{ID: 0, Name: "/", ParentIdx: -1, Path: "/", FileStart: 0, FileEnd: 4, SubDirs: []int32{1, 4}},
			{ID: 1, Name: "allowed", ParentIdx: 0, Path: "/allowed/", FileStart: 0, FileEnd: 3, SubDirs: []int32{2, 3}},
			{ID: 2, Name: "blocked", ParentIdx: 1, Path: "/allowed/blocked/", FileStart: 1, FileEnd: 2},
			{ID: 3, Name: "sibling", ParentIdx: 1, Path: "/allowed/sibling/", FileStart: 2, FileEnd: 3},
			{ID: 4, Name: "other", ParentIdx: 0, Path: "/other/", FileStart: 3, FileEnd: 4},
		},
		Files: []mt.FileNode[fileExt]{
			{Name: "parent.bin", DirIdx: 1},
			{Name: "blocked.bin", DirIdx: 2},
			{Name: "sibling.bin", DirIdx: 3},
			{Name: "other.bin", DirIdx: 4},
		},
	}
}
