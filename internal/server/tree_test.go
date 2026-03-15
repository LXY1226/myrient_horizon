package server

import (
	"context"
	"testing"

	mt "myrient-horizon/pkg/myrienttree"
	"myrient-horizon/pkg/protocol"
)

func TestHydrateFromDB(t *testing.T) {
	base := testServerTreeBase()
	st := NewTree(base)

	base.Files[0].Ext = FileExt{BestStatus: protocol.StatusFailed, ReportCount: 9, HasConflict: true}
	base.Dirs[0].Ext = DirExt{Conflict: 99, Failed: 99}
	st.fileHashes[0] = map[string][]byte{"worker-7": []byte("stale")}

	hashAOwner := []byte("hash-a")
	hashAReplica := []byte("hash-a")
	hashB := []byte("hash-b")
	hashC := []byte("hash-c")
	rows := []*ItemStatus{
		{WorkerID: 99, WorkerKey: "worker-alpha", FileID: 0, Status: int16(protocol.StatusDownloaded), SHA1: hashAOwner},
		{WorkerID: 99, WorkerKey: "worker-beta", FileID: 0, Status: int16(protocol.StatusVerified), SHA1: hashAReplica},
		{WorkerID: 99, WorkerKey: "worker-alpha", FileID: 1, Status: int16(protocol.StatusArchived), SHA1: hashB},
		{WorkerID: 99, WorkerKey: "worker-beta", FileID: 1, Status: int16(protocol.StatusFailed), SHA1: hashC},
		{WorkerID: 99, WorkerKey: "worker-gamma", FileID: 99, Status: int16(protocol.StatusFailed), SHA1: []byte("ignored")},
	}

	if err := st.hydrateFromItemStatus(func(fn func(*ItemStatus) error) error {
		for _, row := range rows {
			if err := fn(row); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("hydrateFromItemStatus() error = %v", err)
	}

	hashAOwner[0] = 'X'
	hashB[0] = 'Y'

	file0 := st.base.Files[0].Ext
	if file0.BestStatus != protocol.StatusVerified {
		t.Fatalf("file0 BestStatus = %v, want %v", file0.BestStatus, protocol.StatusVerified)
	}
	if file0.ReportCount != 2 {
		t.Fatalf("file0 ReportCount = %d, want 2", file0.ReportCount)
	}
	if file0.HasConflict {
		t.Fatalf("file0 HasConflict = true, want false")
	}

	file1 := st.base.Files[1].Ext
	if file1.BestStatus != protocol.StatusFailed {
		t.Fatalf("file1 BestStatus = %v, want %v", file1.BestStatus, protocol.StatusFailed)
	}
	if file1.ReportCount != 2 {
		t.Fatalf("file1 ReportCount = %d, want 2", file1.ReportCount)
	}
	if !file1.HasConflict {
		t.Fatalf("file1 HasConflict = false, want true")
	}

	if got := string(st.fileHashes[0]["worker-alpha"]); got != "hash-a" {
		t.Fatalf("stored hash for file0 worker-alpha = %q, want %q", got, "hash-a")
	}
	if got := string(st.fileHashes[1]["worker-alpha"]); got != "hash-b" {
		t.Fatalf("stored hash for file1 worker-alpha = %q, want %q", got, "hash-b")
	}

	rootStats := st.base.Dirs[0].Ext
	if rootStats.Total != 2 || rootStats.TotalSize != 30 {
		t.Fatalf("root totals = %+v, want total=2 total_size=30", rootStats)
	}
	if rootStats.Verified != 1 || rootStats.VerifiedSize != 10 {
		t.Fatalf("root verified = %+v, want verified=1 verified_size=10", rootStats)
	}
	if rootStats.Failed != 1 || rootStats.Conflict != 1 {
		t.Fatalf("root failed/conflict = %+v, want failed=1 conflict=1", rootStats)
	}

	childStats := st.base.Dirs[1].Ext
	if childStats.Total != 1 || childStats.TotalSize != 20 {
		t.Fatalf("child totals = %+v, want total=1 total_size=20", childStats)
	}
	if childStats.Failed != 1 || childStats.Conflict != 1 {
		t.Fatalf("child failed/conflict = %+v, want failed=1 conflict=1", childStats)
	}
}

func TestConflictBubbling(t *testing.T) {
	st := NewTree(testSingleFileTreeBase())

	st.ApplyReport(0, "worker-1", protocol.StatusVerified, []byte("same"))
	assertDirConflictState(t, st, 0, 0, 1)
	assertDirConflictState(t, st, 1, 0, 1)

	st.ApplyReport(0, "worker-2", protocol.StatusVerified, []byte("other"))
	assertDirConflictState(t, st, 0, 1, 1)
	assertDirConflictState(t, st, 1, 1, 1)
	if !st.base.Files[0].Ext.HasConflict {
		t.Fatalf("file conflict = false, want true")
	}

	st.ApplyReport(0, "worker-2", protocol.StatusVerified, []byte("same"))
	assertDirConflictState(t, st, 0, 0, 1)
	assertDirConflictState(t, st, 1, 0, 1)
	if st.base.Files[0].Ext.HasConflict {
		t.Fatalf("file conflict = true, want false")
	}
	if st.base.Files[0].Ext.ReportCount != 2 {
		t.Fatalf("file report count = %d, want 2", st.base.Files[0].Ext.ReportCount)
	}
}

func assertDirConflictState(t *testing.T, st *ServerTree, dirID int32, wantConflict, wantVerified int32) {
	t.Helper()

	stats := st.base.Dirs[dirID].Ext
	if stats.Conflict != wantConflict {
		t.Fatalf("dir %d conflict = %d, want %d", dirID, stats.Conflict, wantConflict)
	}
	if stats.Verified != wantVerified {
		t.Fatalf("dir %d verified = %d, want %d", dirID, stats.Verified, wantVerified)
	}
	if stats.VerifiedSize != 10 {
		t.Fatalf("dir %d verified_size = %d, want 10", dirID, stats.VerifiedSize)
	}
}

func TestHydrateFromDBRecomputesClaimedStatsFromPersistedClaims(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	workerID, key, err := store.RegisterWorker(ctx, "alpha")
	if err != nil {
		t.Fatalf("RegisterWorker() error = %v", err)
	}
	if err := store.UpsertItemStatusByKey(ctx, key, 3, int16(protocol.StatusVerified), []byte("sha1"), nil); err != nil {
		t.Fatalf("UpsertItemStatusByKey() error = %v", err)
	}
	if err := store.InsertReclaim(ctx, workerID, 1, false); err != nil {
		t.Fatalf("InsertReclaim(whitelist) error = %v", err)
	}
	if err := store.InsertReclaim(ctx, workerID, 2, true); err != nil {
		t.Fatalf("InsertReclaim(blacklist) error = %v", err)
	}

	st := NewTree(testClaimedStatsTreeBase())
	if err := st.HydrateFromDB(ctx, store); err != nil {
		t.Fatalf("HydrateFromDB() error = %v", err)
	}

	rootStats := st.base.Dirs[0].Ext
	if rootStats.Claimed != 2 || rootStats.ClaimedSize != 40 {
		t.Fatalf("root claimed = %+v, want claimed=2 claimed_size=40", rootStats)
	}
	if rootStats.Verified != 1 || rootStats.VerifiedSize != 30 {
		t.Fatalf("root verified = %+v, want verified=1 verified_size=30", rootStats)
	}

	claimedStats := st.base.Dirs[1].Ext
	if claimedStats.Claimed != 2 || claimedStats.ClaimedSize != 40 {
		t.Fatalf("claimed dir stats = %+v, want claimed=2 claimed_size=40", claimedStats)
	}

	blockedStats := st.base.Dirs[2].Ext
	if blockedStats.Claimed != 0 || blockedStats.ClaimedSize != 0 {
		t.Fatalf("blocked dir stats = %+v, want claimed=0 claimed_size=0", blockedStats)
	}

	allowedStats := st.base.Dirs[3].Ext
	if allowedStats.Claimed != 1 || allowedStats.ClaimedSize != 30 {
		t.Fatalf("allowed dir stats = %+v, want claimed=1 claimed_size=30", allowedStats)
	}
}

func TestHydrateFromDBIgnoresBlacklistOnlyOwnersAndUnionsWhitelists(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	workerAID, _, err := store.RegisterWorker(ctx, "alpha")
	if err != nil {
		t.Fatalf("RegisterWorker(alpha) error = %v", err)
	}
	workerBID, _, err := store.RegisterWorker(ctx, "beta")
	if err != nil {
		t.Fatalf("RegisterWorker(beta) error = %v", err)
	}
	workerCID, _, err := store.RegisterWorker(ctx, "gamma")
	if err != nil {
		t.Fatalf("RegisterWorker(gamma) error = %v", err)
	}

	if err := store.InsertReclaim(ctx, workerAID, 1, false); err != nil {
		t.Fatalf("InsertReclaim(alpha whitelist) error = %v", err)
	}
	if err := store.InsertReclaim(ctx, workerBID, 2, true); err != nil {
		t.Fatalf("InsertReclaim(beta blacklist only) error = %v", err)
	}
	if err := store.InsertReclaim(ctx, workerCID, 3, false); err != nil {
		t.Fatalf("InsertReclaim(gamma whitelist) error = %v", err)
	}

	st := NewTree(testClaimedStatsTreeBase())
	if err := st.HydrateFromDB(ctx, store); err != nil {
		t.Fatalf("HydrateFromDB() error = %v", err)
	}

	rootStats := st.base.Dirs[0].Ext
	if rootStats.Claimed != 3 || rootStats.ClaimedSize != 60 {
		t.Fatalf("root claimed = %+v, want claimed=3 claimed_size=60", rootStats)
	}

	blockedStats := st.base.Dirs[2].Ext
	if blockedStats.Claimed != 1 || blockedStats.ClaimedSize != 20 {
		t.Fatalf("blocked dir stats = %+v, want claimed=1 claimed_size=20", blockedStats)
	}

	allowedStats := st.base.Dirs[3].Ext
	if allowedStats.Claimed != 1 || allowedStats.ClaimedSize != 30 {
		t.Fatalf("allowed dir stats = %+v, want claimed=1 claimed_size=30", allowedStats)
	}
}

func TestHydrateFromDBReplacesSameDirectoryWhitelistWithBlacklist(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	workerID, _, err := store.RegisterWorker(ctx, "alpha")
	if err != nil {
		t.Fatalf("RegisterWorker() error = %v", err)
	}

	if err := store.InsertReclaim(ctx, workerID, 1, false); err != nil {
		t.Fatalf("InsertReclaim(whitelist) error = %v", err)
	}
	if err := store.InsertReclaim(ctx, workerID, 1, true); err != nil {
		t.Fatalf("InsertReclaim(replacing opposite action) error = %v", err)
	}

	st := NewTree(testServerTreeBase())
	if err := st.HydrateFromDB(ctx, store); err != nil {
		t.Fatalf("HydrateFromDB() error = %v", err)
	}

	rootStats := st.base.Dirs[0].Ext
	if rootStats.Claimed != 0 || rootStats.ClaimedSize != 0 {
		t.Fatalf("root claimed = %+v, want claimed=0 claimed_size=0", rootStats)
	}

	childStats := st.base.Dirs[1].Ext
	if childStats.Claimed != 0 || childStats.ClaimedSize != 0 {
		t.Fatalf("child claimed = %+v, want claimed=0 claimed_size=0", childStats)
	}
}

func testServerTreeBase() *mt.Tree[DirExt, FileExt] {
	return &mt.Tree[DirExt, FileExt]{
		Dirs: []mt.DirNode[DirExt]{
			{Name: "/", ID: 0, ParentIdx: -1, FileStart: 0, FileEnd: 2, Path: "/", SubDirs: []int32{1}},
			{Name: "child", ID: 1, ParentIdx: 0, FileStart: 1, FileEnd: 2, Path: "/child/"},
		},
		Files: []mt.FileNode[FileExt]{
			{Name: "root.bin", Size: 10, DirIdx: 0},
			{Name: "child.bin", Size: 20, DirIdx: 1},
		},
		PathIndex: map[string]int32{"/": 0, "/child/": 1},
	}
}

func testSingleFileTreeBase() *mt.Tree[DirExt, FileExt] {
	return &mt.Tree[DirExt, FileExt]{
		Dirs: []mt.DirNode[DirExt]{
			{Name: "/", ID: 0, ParentIdx: -1, FileStart: 0, FileEnd: 1, Path: "/", SubDirs: []int32{1}},
			{Name: "child", ID: 1, ParentIdx: 0, FileStart: 0, FileEnd: 1, Path: "/child/"},
		},
		Files: []mt.FileNode[FileExt]{
			{Name: "conflict.bin", Size: 10, DirIdx: 1},
		},
		PathIndex: map[string]int32{"/": 0, "/child/": 1},
	}
}

func testClaimedStatsTreeBase() *mt.Tree[DirExt, FileExt] {
	return &mt.Tree[DirExt, FileExt]{
		Dirs: []mt.DirNode[DirExt]{
			{Name: "/", ID: 0, ParentIdx: -1, FileStart: 0, FileEnd: 4, Path: "/", SubDirs: []int32{1}},
			{Name: "claimed", ID: 1, ParentIdx: 0, FileStart: 1, FileEnd: 4, Path: "/claimed/", SubDirs: []int32{2, 3}},
			{Name: "blocked", ID: 2, ParentIdx: 1, FileStart: 2, FileEnd: 3, Path: "/claimed/blocked/"},
			{Name: "allowed", ID: 3, ParentIdx: 1, FileStart: 3, FileEnd: 4, Path: "/claimed/allowed/"},
		},
		Files: []mt.FileNode[FileExt]{
			{Name: "root.bin", Size: 5, DirIdx: 0},
			{Name: "parent.bin", Size: 10, DirIdx: 1},
			{Name: "blocked.bin", Size: 20, DirIdx: 2},
			{Name: "allowed.bin", Size: 30, DirIdx: 3},
		},
		PathIndex: map[string]int32{"/": 0, "/claimed/": 1, "/claimed/blocked/": 2, "/claimed/allowed/": 3},
	}
}
