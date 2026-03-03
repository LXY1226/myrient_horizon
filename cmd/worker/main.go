package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	"myrient-horizon/internal/worker/aria2"
	"myrient-horizon/internal/worker/config"
	"myrient-horizon/internal/worker/reporter"
	"myrient-horizon/internal/worker/verify"
	"myrient-horizon/pkg/myrienttree"
	"myrient-horizon/pkg/protocol"

	"golang.org/x/sys/windows"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle signals.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("Shutting down...")
		cancel()
	}()

	workDir, _ := os.Getwd()

	// 1. Load or register config.
	cfg, err := config.Load(workDir)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}
	if cfg == nil {
		// First run: create a default config file for the user to edit.
		hostname, _ := os.Hostname()
		cfg = &config.WorkerConfig{
			Name: "worker-" + hostname,
		}
		if err := config.Save(workDir, cfg); err != nil {
			log.Fatalf("Failed to save default config: %v", err)
		}
		log.Fatalf("No config found. A default worker.json has been created in %s — please edit it and restart.", workDir)
	}
	if cfg.ID == 0 {
		// Config exists but not yet registered.
		log.Printf("Registering with server as %q...", cfg.Name)
		cfg, err = config.Register(workDir, cfg.ServerURL, cfg.Name)
		if err != nil {
			log.Fatalf("Failed to register: %v", err)
		}
		log.Printf("Registered as worker %d", cfg.ID)
	} else {
		log.Printf("Loaded config: worker %d", cfg.ID)
	}

	// 2. Load flatbuffer tree.
	log.Printf("Loading tree from %s...", cfg.TreeFile)
	tree, err := myrienttree.LoadFromFile(cfg.TreeFile)
	if err != nil {
		log.Fatalf("Failed to load tree: %v", err)
	}
	log.Printf("Tree loaded: %d dirs, %d files", len(tree.Dirs), len(tree.Files))

	// 3. Connect to server.
	log.Printf("Connecting to server...")
	rpt, err := reporter.New(ctx, cfg.ServerURL, cfg.Key)
	if err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	defer rpt.Close(ctx)
	log.Printf("Connected to server")

	// 4. Start aria2c.
	os.MkdirAll(cfg.DownloadDir, 0755)
	sessionFile := filepath.Join(workDir, ".aria2-session")
	aria2Cfg := aria2.Config{
		Aria2cPath:  cfg.Aria2cPath,
		DownloadDir: cfg.DownloadDir,
		RPCPort:     cfg.Aria2Port,
		MaxConcur:   5,
		SessionFile: sessionFile,
	}
	aria2Client, err := aria2.NewClient(aria2Cfg)
	if err != nil {
		log.Fatalf("Failed to start aria2c: %v", err)
	}
	defer aria2Client.Close()

	// 5. Set up verification queue.
	verifyQueue := verify.NewQueue(2)

	// Track active download/verify counts.
	var downloadingCount atomic.Int32
	var verifyingCount atomic.Int32

	// 6. Wire up server message callbacks.
	rpt.OnConfigUpdate = func(cfg protocol.WorkerConfig) {
		log.Printf("Config update: download=%d verify=%d simultaneous=%v",
			cfg.DownloadConcurrency, cfg.VerifyConcurrency, cfg.Simultaneous)
		verifyQueue.SetConcurrency(cfg.VerifyConcurrency)
		// aria2c concurrency would be updated via aria2 API.
	}

	rpt.OnTaskAssign = func(dirIDs []int32) {
		log.Printf("Task assigned: %d directories", len(dirIDs))
		for _, dirID := range dirIDs {
			go processDirectory(ctx, tree, dirID, cfg.DownloadDir, aria2Client, verifyQueue, rpt,
				&downloadingCount, &verifyingCount, cfg.DiskMinGB)
		}
	}

	rpt.OnTaskRevoke = func(dirIDs []int32) {
		log.Printf("Task revoked: %v (not yet implemented)", dirIDs)
		// TODO: cancel in-progress downloads for these directories.
	}

	// 7. Start reading server messages.
	go rpt.ReadLoop(ctx)

	// 8. Process verification results and report them.
	go func() {
		for result := range verifyQueue.Results() {
			verifyingCount.Add(-1)
			if result.OK {
				// Rename .downloading → final name if needed.
				if result.FilePath != result.FinalPath {
					if err := os.Rename(result.FilePath, result.FinalPath); err != nil {
						log.Printf("Rename failed %s → %s: %v", result.FilePath, result.FinalPath, err)
						rpt.ReportFile(ctx, result.DirID, result.FileIdx,
							protocol.StatusFailed, nil, nil)
						continue
					}
				}
				rpt.ReportFile(ctx, result.DirID, result.FileIdx,
					protocol.StatusVerified, result.SHA1, result.CRC32)
			} else {
				log.Printf("Verification failed for %s: %v", result.Name, result.Err)
				// Remove bad .downloading file so it can be re-downloaded.
				if result.FilePath != result.FinalPath {
					os.Remove(result.FilePath)
				}
				rpt.ReportFile(ctx, result.DirID, result.FileIdx,
					protocol.StatusFailed, nil, nil)
			}
		}
	}()

	// 9. Heartbeat loop.
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				freeGB := getDiskFreeGB(cfg.DownloadDir)
				aria2Status := "ok"
				if !aria2Client.IsAlive() {
					aria2Status = "dead"
				}
				rpt.SendHeartbeat(ctx, freeGB,
					int(downloadingCount.Load()), int(verifyingCount.Load()), aria2Status)
			}
		}
	}()

	// Wait for shutdown.
	<-ctx.Done()
	rpt.Flush(context.Background())
	log.Println("Worker stopped")
}

// processDirectory handles downloading and verifying all files in a directory.
func processDirectory(
	ctx context.Context,
	tree *myrienttree.Tree,
	dirID int32,
	downloadDir string,
	aria2Client *aria2.Client,
	verifyQ *verify.Queue,
	rpt *reporter.Reporter,
	downloadingCount, verifyingCount *atomic.Int32,
	diskMinGB float64,
) {
	dir := &tree.Dirs[dirID]
	log.Printf("Processing directory: %s (%d files)", dir.Path, dir.FileEnd-dir.FileStart)

	// Build the download base URL from the directory path.
	// Myrient base URL.
	baseURL := "https://myrient.erista.me/files" + dir.Path

	for i := dir.FileStart; i < dir.FileEnd; i++ {
		if ctx.Err() != nil {
			return
		}

		file := &tree.Files[i]
		localIdx := i - dir.FileStart
		finalPath := filepath.Join(downloadDir, dir.Path, file.Name)
		downloadingPath := finalPath + verify.DownloadingSuffix

		// Check if already verified (final name exists with correct size).
		if info, err := os.Stat(finalPath); err == nil && info.Size() == file.Size {
			// Already verified in a previous run — submit with same path.
			verifyingCount.Add(1)
			verifyQ.Submit(dirID, localIdx, finalPath, finalPath)
			continue
		}

		// Check if downloaded but not yet verified (.downloading exists).
		if info, err := os.Stat(downloadingPath); err == nil && info.Size() == file.Size {
			verifyingCount.Add(1)
			verifyQ.Submit(dirID, localIdx, downloadingPath, finalPath)
			continue
		}

		// Check disk space.
		freeGB := getDiskFreeGB(downloadDir)
		if freeGB < diskMinGB {
			log.Printf("Disk space low (%.1f GB free), stopping downloads", freeGB)
			return
		}

		// Submit download to aria2c with .downloading suffix.
		uri := baseURL + file.Name
		downloadingCount.Add(1)
		rpt.ReportFile(ctx, dirID, localIdx, protocol.StatusDownloading, nil, nil)

		dirPath := filepath.Join(downloadDir, dir.Path)
		os.MkdirAll(dirPath, 0755)
		_, err := aria2Client.AddURI(ctx, uri, dirPath, file.Name+verify.DownloadingSuffix)
		if err != nil {
			log.Printf("Failed to add download %s: %v", file.Name, err)
			downloadingCount.Add(-1)
			continue
		}

		// Wait for download to complete (poll .downloading file).
		go func(dirID, localIdx int32, dlPath, fnlPath string, expectSize int64) {
			defer downloadingCount.Add(-1)
			for {
				if ctx.Err() != nil {
					return
				}
				time.Sleep(2 * time.Second)
				info, err := os.Stat(dlPath)
				if err == nil && info.Size() == expectSize {
					// File downloaded, report and submit for verification.
					rpt.ReportFile(ctx, dirID, localIdx, protocol.StatusDownloaded, nil, nil)
					verifyingCount.Add(1)
					verifyQ.Submit(dirID, localIdx, dlPath, fnlPath)
					return
				}
			}
		}(dirID, localIdx, downloadingPath, finalPath, file.Size)
	}
}

// getDiskFreeGB returns the free disk space in GB for the drive containing the given path.
func getDiskFreeGB(path string) float64 {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return 0
	}

	var freeBytes uint64
	pathPtr, _ := windows.UTF16PtrFromString(filepath.VolumeName(absPath) + "\\")
	err = windows.GetDiskFreeSpaceEx(pathPtr, nil, nil, &freeBytes)
	if err != nil {
		return 0
	}
	return float64(freeBytes) / (1024 * 1024 * 1024)
}
