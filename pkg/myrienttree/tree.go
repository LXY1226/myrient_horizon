package myrienttree

import (
	"fmt"
	"log"
	"os"
	"unsafe"

	fb "myrient-horizon/pkg/myrienttree/MyrientTree"
)

// DirNode represents a directory in the in-memory tree.
type DirNode[DirExt any] struct {
	Name      string  // unsafe.String pointing into flatbuf memory
	ID        int32   // index in Tree.Dirs
	ParentIdx int32   // parent directory index in Dirs, -1 for root
	FileStart int32   // start index of this dir's files in Tree.Files (inclusive)
	FileEnd   int32   // end index (exclusive)
	SubDirs   []int32 // child directory indices in Dirs
	Path      string  // full path from root, e.g. "/No-Intro/Nintendo/"

	Ext DirExt
}

// FileNode represents a file in the in-memory tree.
type FileNode[FileExt any] struct {
	Name   string // unsafe.String pointing into flatbuf memory
	Size   int64
	DirIdx int32 // owning directory index in Tree.Dirs

	Ext FileExt // Extension field for generic type (holds mutable state in server)
}

// Tree is the core in-memory representation loaded from a flatbuffer.
// Both server and worker share this structure; DFS order is deterministic.
type Tree[DirExt any, FileExt any] struct {
	Flatbuf []byte              // raw flatbuffer buffer, kept alive for string references
	Dirs    []DirNode[DirExt]   // dirs[dirID]
	Files   []FileNode[FileExt] // files[fileID]

	// PathIndex maps directory path → dirID for path-based lookups (API use).
	PathIndex map[string]int32
}

// LoadFromFile reads a flatbuffer file and builds the tree.
func LoadFromFile[DirExt any, FileExt any](path string) (*Tree[DirExt, FileExt], error) {
	log.Printf("Loading tree from %s...", path)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read flatbuffer: %w", err)
	}
	return LoadFromBytes[DirExt, FileExt](data)
}

// LoadFromBytes builds the tree from raw flatbuffer bytes.
func LoadFromBytes[DirExt any, FileExt any](data []byte) (*Tree[DirExt, FileExt], error) {
	root := fb.GetRootAsNode(data, 0)
	if root == nil {
		return nil, fmt.Errorf("invalid flatbuffer: nil root")
	}

	t := &Tree[DirExt, FileExt]{
		Flatbuf:   data,
		PathIndex: make(map[string]int32),
	}

	// Pre-allocate with rough estimates.
	t.Dirs = make([]DirNode[DirExt], 0, 65536)
	t.Files = make([]FileNode[FileExt], 0, 3000000)

	// DFS traversal.
	t.buildDFS(root, -1, "")
	log.Printf("Tree loaded: %d dirs, %d files", len(t.Dirs), len(t.Files))
	return t, nil
}

// bytesToString performs a zero-copy conversion from []byte to string.
// The caller must ensure the backing byte slice outlives the returned string.
func bytesToString(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return unsafe.String(&b[0], len(b))
}

// buildDFS recursively traverses the flatbuffer tree in DFS order.
func (t *Tree[DirExt, FileExt]) buildDFS(fbNode *fb.Node, parentIdx int32, parentPath string) {
	name := bytesToString(fbNode.Name())
	path := parentPath + name
	if name != "/" {
		path += "/"
	}

	dirIdx := int32(len(t.Dirs))
	t.Dirs = append(t.Dirs, DirNode[DirExt]{
		Name:      name,
		ID:        dirIdx,
		ParentIdx: parentIdx,
		Path:      path,
	})
	t.PathIndex[path] = dirIdx

	// Separate children into files and subdirectories.
	childCount := fbNode.ChildrenLength()
	fileStart := int32(len(t.Files))

	var child fb.Node
	var subDirNodes []int // indices into fbNode's children that are directories

	for i := 0; i < childCount; i++ {
		if !fbNode.Children(&child, i) {
			continue
		}
		if child.ChildrenLength() > 0 {
			// Directory - process later to keep files contiguous.
			subDirNodes = append(subDirNodes, i)
		} else {
			// File (leaf node).
			t.Files = append(t.Files, FileNode[FileExt]{
				Name:   bytesToString(child.Name()),
				Size:   child.Size(),
				DirIdx: dirIdx,
			})
		}
	}

	t.Dirs[dirIdx].FileStart = fileStart

	// Now recurse into subdirectories.
	subDirIDs := make([]int32, 0, len(subDirNodes))
	for _, ci := range subDirNodes {
		fbNode.Children(&child, ci)
		nextDirIdx := int32(len(t.Dirs))
		subDirIDs = append(subDirIDs, nextDirIdx)
		t.buildDFS(&child, dirIdx, path)
	}
	t.Dirs[dirIdx].SubDirs = subDirIDs

	// fileEnd captured after recursion so it includes all descendant files.
	t.Dirs[dirIdx].FileEnd = int32(len(t.Files))
}

// DirFileCount returns the number of direct files in a directory.
func (t *Tree[DirExt, FileExt]) DirFileCount(dirID int32) int32 {
	d := &t.Dirs[dirID]
	return d.FileEnd - d.FileStart
}

// FileGlobalIndex converts (dirID, fileIdx within dir) to a global file index.
func (t *Tree[DirExt, FileExt]) FileGlobalIndex(dirID, fileIdx int32) int32 {
	return t.Dirs[dirID].FileStart + fileIdx
}

// DirByPath looks up a directory by its full path.
func (t *Tree[DirExt, FileExt]) DirByPath(path string) (int32, bool) {
	id, ok := t.PathIndex[path]
	return id, ok
}
