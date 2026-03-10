package server

import (
	"testing"

	mt "myrient-horizon/pkg/myrienttree"
	"myrient-horizon/pkg/protocol"
)

func TestHydrateFromDB(t *testing.T) {
	base := testServerTreeBase()
	st := NewTree(base)

	base.Files[0].Ext = FileExt{BestStatus: protocol.StatusFailed, ReportCount: 9, HasConflict: true}
	base.Dirs[0].Ext = DirExt{Conflict: 99, Failed: 99}
	st.fileHashes[0] = map[int][]byte{7: []byte("stale")}

	hashAWorker1 := []byte("hash-a")
	hashAWorker2 := []byte("hash-a")
	hashB := []byte("hash-b")
	hashC := []byte("hash-c")
	rows := []*ItemStatus{
		{WorkerID: 1, FileID: 0, Status: int16(protocol.StatusDownloaded), SHA1: hashAWorker1},
		{WorkerID: 2, FileID: 0, Status: int16(protocol.StatusVerified), SHA1: hashAWorker2},
		{WorkerID: 1, FileID: 1, Status: int16(protocol.StatusArchived), SHA1: hashB},
		{WorkerID: 2, FileID: 1, Status: int16(protocol.StatusFailed), SHA1: hashC},
		{WorkerID: 3, FileID: 99, Status: int16(protocol.StatusFailed), SHA1: []byte("ignored")},
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

	hashAWorker1[0] = 'X'
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

	if got := string(st.fileHashes[0][1]); got != "hash-a" {
		t.Fatalf("stored hash for file0 worker1 = %q, want %q", got, "hash-a")
	}
	if got := string(st.fileHashes[1][1]); got != "hash-b" {
		t.Fatalf("stored hash for file1 worker1 = %q, want %q", got, "hash-b")
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

	st.ApplyReport(0, 1, protocol.StatusVerified, []byte("same"))
	assertDirConflictState(t, st, 0, 0, 1)
	assertDirConflictState(t, st, 1, 0, 1)

	st.ApplyReport(0, 2, protocol.StatusVerified, []byte("other"))
	assertDirConflictState(t, st, 0, 1, 1)
	assertDirConflictState(t, st, 1, 1, 1)
	if !st.base.Files[0].Ext.HasConflict {
		t.Fatalf("file conflict = false, want true")
	}

	st.ApplyReport(0, 2, protocol.StatusVerified, []byte("same"))
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
