package worker

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"myrient-horizon/internal/worker/config"
	mt "myrient-horizon/pkg/myrienttree"
)

// Task is the unit flowing through downloader and verifier.
type Task struct {
	FileID     int32
	LocalPath  string
	Managed    bool
	BadZipSHA1 []byte
}

const (
	DownloadingSuffix = ".downloading"
	DownloadedSuffix  = ".downloaded"
)

func (t *Task) FinalPath() string {
	if strings.HasSuffix(t.LocalPath, DownloadingSuffix) {
		return strings.TrimSuffix(t.LocalPath, DownloadingSuffix)
	}
	if strings.HasSuffix(t.LocalPath, DownloadedSuffix) {
		return strings.TrimSuffix(t.LocalPath, DownloadedSuffix)
	}
	return t.LocalPath
}

func (t *Task) DownloadedPath() string {
	if strings.HasSuffix(t.LocalPath, DownloadingSuffix) {
		return strings.TrimSuffix(t.LocalPath, DownloadingSuffix) + DownloadedSuffix
	}
	if strings.HasSuffix(t.LocalPath, DownloadedSuffix) {
		return t.LocalPath
	}
	return t.FinalPath() + DownloadedSuffix
}

type dirExt struct{}

type fileExt struct {
	planGen        uint32
	confirmedDone  bool
	queuedDownload bool
	queuedVerify   bool
}

const myrientBaseURL = "https://myrient.erista.me/files"

var (
	iTree       *mt.Tree[dirExt, fileExt]
	TreeSHA1    string
	treeStateMu sync.RWMutex
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
	dirPath := iTree.Dirs[f.DirIdx].Path
	sb.WriteString(dirPath)
	if !strings.HasSuffix(dirPath, "/") {
		sb.WriteString("/")
	}
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
	if dir != "" && !strings.HasSuffix(dir, "/") {
		dir += "/"
	}
	if !strings.HasPrefix(dir, "/") {
		dir = "/" + dir
	}
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
	rel = strings.Trim(strings.ReplaceAll(rel, string(os.PathSeparator), "/"), "/")
	idx := strings.LastIndex(rel, "/")
	if idx < 0 {
		return "", rel
	}
	return rel[:idx], rel[idx+1:]
}

func finalLocalPath(fileID int32) string {
	return filepath.Join(config.Global.DownloadDir, filepath.FromSlash(strings.TrimPrefix(taskPath(fileID), "/")))
}

func downloadingLocalPath(fileID int32) string {
	return finalLocalPath(fileID) + DownloadingSuffix
}

func downloadedLocalPath(fileID int32) string {
	return finalLocalPath(fileID) + DownloadedSuffix
}

func taskPath(fileID int32) string {
	task := Task{FileID: fileID}
	return task.GetPath()
}

func markFileVisited(fileID int32, planGen uint32) bool {
	treeStateMu.Lock()
	defer treeStateMu.Unlock()
	ext := &iTree.Files[fileID].Ext
	if ext.planGen == planGen {
		return false
	}
	ext.planGen = planGen
	return true
}

func fileBusyOrConfirmed(fileID int32) bool {
	treeStateMu.RLock()
	defer treeStateMu.RUnlock()
	ext := iTree.Files[fileID].Ext
	return ext.confirmedDone || ext.queuedDownload || ext.queuedVerify
}

func setFileQueuedDownload(fileID int32, queued bool) {
	treeStateMu.Lock()
	defer treeStateMu.Unlock()
	iTree.Files[fileID].Ext.queuedDownload = queued
}

func setFileQueuedVerify(fileID int32, queued bool) {
	treeStateMu.Lock()
	defer treeStateMu.Unlock()
	iTree.Files[fileID].Ext.queuedVerify = queued
}
