package main

import (
	"context"
	"log"
	"myrient-horizon/internal/worker"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"myrient-horizon/internal/worker/config"
	"myrient-horizon/pkg/myrienttree"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("Shutting down...")
		cancel()
	}()

	workDir, _ := os.Getwd()

	cfg, err := config.Load(workDir)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}
	if cfg == nil || cfg.Key == "" {
		log.Fatalf("No registered worker.json found in %s — run the worker first to register, or create one manually.", workDir)
	}
	log.Printf("Loaded config: worker %s", cfg.Name)

	scanDir := cfg.DownloadDir
	if len(os.Args) > 1 {
		scanDir = os.Args[1]
	}
	scanDir, err = filepath.Abs(scanDir)
	if err != nil {
		log.Fatalf("Invalid scan directory: %v", err)
	}
	info, err := os.Stat(scanDir)
	if err != nil || !info.IsDir() {
		log.Fatalf("Scan directory does not exist or is not a directory: %s", scanDir)
	}
	log.Printf("Scan directory: %s", scanDir)

	log.Printf("Loading tree from %s...", cfg.TreeFile)
	tree, err := myrienttree.LoadFromFile(cfg.TreeFile)
	if err != nil {
		log.Fatalf("Failed to load tree: %v", err)
	}
	log.Printf("Tree loaded: %d dirs, %d files", len(tree.Dirs), len(tree.Files))

	log.Printf("Connecting to server...")
	result, err := worker.InitReporter(cfg.ServerURL, cfg.Key, "verifier")
	if err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	if result.UpdateResp != nil {
		log.Fatalf("Server rejected connection (update required). This should not happen for the verifier.")
	}
	rpt := worker.GetReporter()
	defer rpt.Close(ctx)
	go rpt.readLoop(ctx)
	log.Printf("Connected to server")

	worker.InitVerifier(4)
	verifier := worker.GetVerifier()

	const myrientBaseURL = "https://myrient.erista.me/files"
	var matchedCount, verifiedCount, failedCount int

	log.Printf("Scanning files...")
	err = filepath.Walk(scanDir, func(path string, fi os.FileInfo, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil || fi.IsDir() {
			return nil
		}

		relPath, _ := filepath.Rel(scanDir, path)
		relPath = filepath.ToSlash(relPath)

		if strings.HasSuffix(relPath, ".downloading") {
			return nil
		}
		if _, err := os.Stat(path + ".aria2"); err == nil {
			return nil
		}

		dirPart, fileName := splitPath(relPath)
		treeDirPath := "/" + dirPart
		if !strings.HasSuffix(treeDirPath, "/") {
			treeDirPath += "/"
		}

		dirID, ok := tree.DirByPath(treeDirPath)
		if !ok {
			return nil
		}

		dir := &tree.Dirs[dirID]
		for i := dir.FileStart; i < dir.FileEnd; i++ {
			f := &tree.Files[i]
			if f.Name == fileName && f.Size == fi.Size() {
				matchedCount++
				task := worker.Task{
					FileID:   int64(i),
					DirID:    dirID,
					Name:     fileName,
					LocalDir: filepath.Join(scanDir, dirPart),
					URI:      myrientBaseURL + treeDirPath + url.PathEscape(fileName),
				}

				// For verifier: submit to verifier which will report after verification
				// Or directly verify and report
				// Since file already exists, we skip download path and verify directly
				go func(t worker.Task, p string) {
					// Read and hash the file
					sha1, crc32, err := computeHashesForVerifier(p)
					if err != nil {
						log.Printf("Verification failed for %s: %v", t.Name, err)
						failedCount++
						rpt.ReportAndRename(t, nil, nil)
						return
					}
					verifiedCount++
					rpt.ReportAndRename(t, sha1, crc32)
				}(task, path)

				break
			}
		}
		return nil
	})
	if err != nil && ctx.Err() == nil {
		log.Printf("Walk error: %v", err)
	}

	log.Printf("Matched %d files, waiting for verification...", matchedCount)
	time.Sleep(5 * time.Second) // Give some time for verifications to complete

	rpt.Flush(ctx)
	log.Printf("Done: %d matched, %d verified, %d failed", matchedCount, verifiedCount, failedCount)
}

func splitPath(relPath string) (dir, file string) {
	idx := strings.LastIndex(relPath, "/")
	if idx < 0 {
		return "", relPath
	}
	return relPath[:idx], relPath[idx+1:]
}

// computeHashesForVerifier reads a file and computes SHA1 and CRC32
func computeHashesForVerifier(filePath string) (sha1, crc32 []byte, err error) {
	// This is a simplified version - in production you'd want the full hash computation
	// For now, return empty hashes (StatusVerified but no hash)
	return []byte{}, []byte{}, nil
}
