package worker

import (
	"bytes"
	"fmt"
	"log"
	mt "myrient-horizon/pkg/myrienttree"
	"os"
	"strings"
)

// Task is the unit flowing through downloader and verifier.
type Task struct {
	FileID     int32  // 全局文件索引
	LocalPath  string // XXX for verifier, XXX.downloading for downloader
	Managed    bool
	BadZipSHA1 []byte
}

const (
	DownloadingSuffix = ".downloading"
)

func (t *Task) FinalPath() string { return t.LocalPath[:len(t.LocalPath)-len(DownloadingSuffix)] }

type dirExt struct{}
type fileExt struct{}

const myrientBaseURL = "https://myrient.erista.me/files"

var (
	iTree    *mt.Tree[dirExt, fileExt]
	TreeSHA1 string
)

func LoadTree(path string) {
	if err := initTree(path); err != nil {
		log.Fatalf("Failed to load tree: %v", err)
	}
}

func initTree(path string) error {
	tree, hash, err := mt.LoadFromFile[dirExt, fileExt](path)
	if err != nil {
		return err
	}
	iTree = tree
	TreeSHA1 = hash
	return nil
}

func (t *Task) GetFile() mt.FileNode[fileExt] {
	return iTree.Files[t.FileID]
}

func (t *Task) GetURI() string {
	sb := strings.Builder{}
	sb.WriteString(myrientBaseURL)
	return t.getPath(&sb).String()
}

func (t *Task) getPath(sb *strings.Builder) *strings.Builder {
	f := t.GetFile()
	sb.WriteString(iTree.Dirs[f.DirIdx].Path)
	sb.WriteString("/")
	sb.WriteString(f.Name)
	return sb
}

func (t *Task) GetPath() string {
	return t.getPath(&strings.Builder{}).String()
}

func (t *Task) ShouldSkipZipCheck(sha1 []byte) bool {
	return len(t.BadZipSHA1) > 0 && bytes.Equal(t.BadZipSHA1, sha1)
}

func (t *Task) RememberBadZipSHA1(sha1 []byte) {
	t.BadZipSHA1 = append(t.BadZipSHA1[:0], sha1...)
}

func (t *Task) ClearBadZipSHA1() {
	t.BadZipSHA1 = nil
}

func MatchFileByPath(dir, name string) (int32, error) {
	dir = strings.ReplaceAll(dir, string(os.PathSeparator), "/")
	dirID, ok := iTree.DirByPath(dir)
	if !ok {
		return -1, fmt.Errorf("no matching dir: %s", dir)
	}
	dirEnt := &iTree.Dirs[dirID]
	for idx := dirEnt.FileStart; idx < dirEnt.FileEnd; idx++ {
		file := &iTree.Files[idx]
		if file.DirIdx != dirID || file.Name != name {
			continue
		}
		return idx, nil
	}
	return -1, fmt.Errorf("no matching file: %s", name)
}

func splitRelativePath(rel string) (dirPart, fileName string) {
	rel = strings.Trim(rel, "/")
	idx := strings.LastIndex(rel, "/")
	if idx < 0 {
		return "", rel
	}
	return rel[:idx], rel[idx+1:]
}
