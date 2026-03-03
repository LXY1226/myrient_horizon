package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"myrient-horizon/internal/worker/config"
	"myrient-horizon/internal/worker/reporter"
	"myrient-horizon/internal/worker/verify"
	"myrient-horizon/pkg/myrienttree"
	"myrient-horizon/pkg/protocol"
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

	// 1. Load config (same worker.json format).
	cfg, err := config.Load(workDir)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}
	if cfg == nil || cfg.ID == 0 {
		log.Fatalf("No registered worker.json found in %s — run the worker first to register, or create one manually.", workDir)
	}
	log.Printf("Loaded config: worker %d", cfg.ID)

	// 2. Determine scan directory: CLI arg or config's DownloadDir.
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

	// 3. Load flatbuffer tree.
	log.Printf("Loading tree from %s...", cfg.TreeFile)
	tree, err := myrienttree.LoadFromFile(cfg.TreeFile)
	if err != nil {
		log.Fatalf("Failed to load tree: %v", err)
	}
	log.Printf("Tree loaded: %d dirs, %d files", len(tree.Dirs), len(tree.Files))

	// 4. Connect to server.
	log.Printf("Connecting to server...")
	rpt, err := reporter.New(ctx, cfg.ServerURL, cfg.Key)
	if err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	defer rpt.Close(ctx)
	go rpt.ReadLoop(ctx) // drain server messages
	log.Printf("Connected to server")

	// 5. Set up verification queue.
	verifyQueue := verify.NewQueue(4)
	var verifyingCount atomic.Int32
	var matchedCount, verifiedCount, failedCount atomic.Int32

	// 6. Process verification results.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for result := range verifyQueue.Results() {
			verifyingCount.Add(-1)
			if result.OK {
				verifiedCount.Add(1)
				rpt.ReportFile(ctx, result.DirID, result.FileIdx,
					protocol.StatusVerified, result.SHA1, result.CRC32)
			} else {
				failedCount.Add(1)
				log.Printf("Verification failed for %s: %v", result.Name, result.Err)
				rpt.ReportFile(ctx, result.DirID, result.FileIdx,
					protocol.StatusFailed, nil, nil)
			}
		}
	}()

	// 7. Heartbeat while running.
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-ticker.C:
				rpt.SendHeartbeat(ctx, 0,
					0, int(verifyingCount.Load()), "verifier")
			}
		}
	}()

	// 8. Walk scan directory and match files against tree.
	log.Printf("Scanning files...")
	err = filepath.Walk(scanDir, func(path string, fi os.FileInfo, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil || fi.IsDir() {
			return nil
		}

		// Compute relative path from scan root.
		relPath, _ := filepath.Rel(scanDir, path)
		// Normalize to forward slashes and split into dir + filename.
		relPath = filepath.ToSlash(relPath)

		// Skip .downloading files — they are incomplete.
		if strings.HasSuffix(relPath, verify.DownloadingSuffix) {
			return nil
		}

		dirPart, fileName := splitPath(relPath)
		// Tree paths look like "/No-Intro/Nintendo/", so prepend "/" and append "/".
		treeDirPath := "/" + dirPart
		if !strings.HasSuffix(treeDirPath, "/") {
			treeDirPath += "/"
		}

		dirID, ok := tree.DirByPath(treeDirPath)
		if !ok {
			return nil // directory not in tree
		}

		// Find matching file in the directory by name and size.
		dir := &tree.Dirs[dirID]
		for i := dir.FileStart; i < dir.FileEnd; i++ {
			f := &tree.Files[i]
			if f.Name == fileName && f.Size == fi.Size() {
				localIdx := i - dir.FileStart
				matchedCount.Add(1)
				verifyingCount.Add(1)
				verifyQueue.Submit(dirID, localIdx, path, path)
				break
			}
		}
		return nil
	})
	if err != nil && ctx.Err() == nil {
		log.Printf("Walk error: %v", err)
	}

	// 9. Wait for all verifications to complete.
	log.Printf("Matched %d files, waiting for verification...", matchedCount.Load())
	verifyQueue.Wait()
	<-done

	// 10. Final flush and summary.
	rpt.Flush(ctx)
	log.Printf("Done: %d matched, %d verified, %d failed",
		matchedCount.Load(), verifiedCount.Load(), failedCount.Load())
}

// splitPath splits "a/b/c/file.zip" into ("a/b/c", "file.zip").
// For "file.zip" (no dir), returns ("", "file.zip").
func splitPath(relPath string) (dir, file string) {
	idx := strings.LastIndex(relPath, "/")
	if idx < 0 {
		return "", relPath
	}
	return relPath[:idx], relPath[idx+1:]
}
