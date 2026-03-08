package worker

import (
	"bytes"
	"fmt"
	"log"
	mt "myrient-horizon/pkg/myrienttree"
	"path/filepath"
	"strings"
)

// Task is the unit flowing through downloader and verifier.
type Task struct {
	FileID     int32  // 全局文件索引
	LocalPath  string // 本地路径（最终的绝对路径）
	Managed    bool
	BadZipSHA1 []byte
}

const (
	DownloadingSuffix = ".downloading"
	VerifiedSuffix    = ".verified"
)

func (t *Task) DownloadingPath() string { return t.LocalPath + DownloadingSuffix }
func (t *Task) VerifiedPath() string    { return t.LocalPath + VerifiedSuffix }

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

//func BuildTask(fileID int64, downloadRoot string) (Task, bool) {
//	if iTree == nil || fileID < 0 || fileID >= int64(len(iTree.Files)) {
//		return Task{}, false
//	}
//
//	file := &iTree.Files[fileID]
//	dir := &iTree.Dirs[file.DirIdx]
//	relDir := strings.Trim(dir.Path, "/")
//	finalPath := filepath.Join(downloadRoot, filepath.FromSlash(relDir), file.Name)
//	uri := myrientBaseURL + dir.Path + url.PathEscape(file.Name)
//
//	return Task{
//		FileID:    fileID,
//		DirID:     file.DirIdx,
//		Name:      file.Name,
//		LocalPath: finalPath,
//		URI:       uri,
//		Size:      file.Size,
//	}, true
//}

//func MatchFileByPath(path string) (fileID int, err error) {
//
//}

func MatchTaskByPath(dir, name string) (*Task, error) {
	dirID, ok := iTree.DirByPath(dir)
	if !ok {
		return nil, fmt.Errorf("no matching dir: %s", dir)
	}
	dirEnt := &iTree.Dirs[dirID]
	for idx := dirEnt.FileStart; idx < dirEnt.FileEnd; idx++ {
		file := &iTree.Files[idx]
		if file.DirIdx != dirID || file.Name != name {
			continue
		}
		return &Task{
			FileID:    idx,
			LocalPath: filepath.Join(dir, name),
		}, nil
	}
	return nil, fmt.Errorf("no matching file: %s", name)
}

func splitRelativePath(rel string) (dirPart, fileName string) {
	rel = strings.Trim(rel, "/")
	idx := strings.LastIndex(rel, "/")
	if idx < 0 {
		return "", rel
	}
	return rel[:idx], rel[idx+1:]
}
