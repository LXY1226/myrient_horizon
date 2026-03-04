package worker

import (
	"log"
	mt "myrient-horizon/pkg/myrienttree"
)

type dirExt struct{}
type fileExt struct{}

var iTree *mt.Tree[dirExt, fileExt]

func LoadTree(path string) {
	var err error
	iTree, err = mt.LoadFromFile[dirExt, fileExt](path)
	if err != nil {
		log.Fatalf("Failed to load tree: %v", err)
	}
}

//
//func processDirectory(
//	ctx context.Context,
//	tree *myrienttree.Tree,
//	dirID int32,
//	cfg *config.WorkerConfig,
//	stateManager *worker.StateManager,
//	downloader *worker.Downloader,
//	verifier *worker.Verifier,
//	reporter *worker.Reporter,
//) {
//	dir := &tree.Dirs[dirID]
//	log.Printf("Processing directory: %s (%d files)", dir.Path, dir.FileEnd-dir.FileStart)
//
//	for i := dir.FileStart; i < dir.FileEnd; i++ {
//		if ctx.Err() != nil {
//			return
//		}
//
//		file := &tree.Files[i]
//
//		task := worker.Task{
//			FileID:   int64(i),
//			DirID:    file.DirIdx,
//			Name:     file.Name,
//			LocalDir: filepath.Join(cfg.DownloadDir, tree.Dirs[file.DirIdx].Path),
//			URI:      myrientBaseURL + tree.Dirs[file.DirIdx].Path + url.PathEscape(file.Name),
//		}
//
//		if stateManager.IsDone(task.FileID) {
//			continue
//		}
//
//		freeGB := getDiskFreeGB(cfg.DownloadDir)
//		if freeGB < cfg.DiskMinGB {
//			log.Printf("Disk space low (%.1f GB free), stopping downloads", freeGB)
//			return
//		}
//
//		finalInfo, _ := os.Stat(task.FinalPath())
//		verifiedInfo, _ := os.Stat(task.VerifiedPath())
//		downloadingInfo, _ := os.Stat(task.DownloadingPath())
//
//		switch {
//		case finalInfo != nil && finalInfo.Size() == file.Size:
//			continue
//
//		case verifiedInfo != nil && verifiedInfo.Size() == file.Size:
//			reporter.ReportAndRename(task, nil, nil)
//
//		case downloadingInfo != nil && downloadingInfo.Size() == file.Size:
//			verifier.Submit(task)
//
//		default:
//			os.MkdirAll(task.LocalDir, 0755)
//			if err := downloader.Submit(ctx, task); err != nil {
//				if ctx.Err() != nil {
//					return
//				}
//				log.Printf("Failed to submit download task for %s: %v", task.Name, err)
//			}
//		}
//	}
//}
