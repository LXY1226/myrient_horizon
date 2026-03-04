package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"myrient-horizon/internal/worker"
	"myrient-horizon/internal/worker/aria2"
	"myrient-horizon/pkg/protocol"
)

const myrientBaseURL = "https://myrient.erista.me/files"

func main() {
	log.Printf("Worker version: %s", protocol.Version)
	worker.CleanOldBinary()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("Shutting down...")
		cancel()
	}()
	cfg := worker.EnsureConfig()
	worker.LoadTree(cfg.TreeFile)

	log.Printf("Connecting to server...")
	result, err := worker.InitReporter(cfg.ServerURL, cfg.Key, Version)
	if err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}

	if result.UpdateResp != nil {
		log.Printf("Server requires a different worker version, initiating self-update...")
		body, _ := io.ReadAll(result.UpdateResp.Body)
		result.UpdateResp.Body.Close()
		var updateInfo protocol.UpdateRequiredResponse
		if err := json.Unmarshal(body, &updateInfo); err != nil {
			log.Fatalf("Failed to parse update response: %v", err)
		}
		log.Printf("Current: %s → Latest: %s", updateInfo.CurrentVersion, updateInfo.LatestVersion)
		if err := worker.Apply(updateInfo); err != nil {
			log.Fatalf("Self-update failed: %v", err)
		}
		os.Exit(0)
	}

	rpt := worker.GetReporter()
	defer rpt.Close(ctx)
	log.Printf("Connected to server")

	stateManager := worker.NewStateManager()
	rpt.SetStateManager(stateManager)

	os.MkdirAll(cfg.DownloadDir, 0755)
	aria2Cfg := aria2.Config{
		Aria2cPath:     cfg.Aria2cPath,
		DownloadDir:    cfg.DownloadDir,
		RPCPort:        cfg.Aria2Port,
		ConfPath:       cfg.Aria2Conf,
		ExtraArgs:      cfg.Aria2Args,
		ExternalRPCURL: cfg.Aria2RPCURL,
	}
	aria2Client, err := aria2.NewClient(aria2Cfg)
	if err != nil {
		log.Fatalf("Failed to start aria2c: %v", err)
	}
	defer aria2Client.Close()

	verifier := worker.InitVerifier(2)
	verifier.SetReporter(rpt)

	downloader := worker.InitDownloader(aria2Client, 1024)
	downloader.SetVerifier(verifier)

	go downloader.Run(ctx)

	var assignedMu sync.Mutex
	var assignedDirIDs []int32

	rpt.OnConfigUpdate = func(cfg protocol.WorkerConfig) {
		log.Printf("Config update: download=%d verify=%d simultaneous=%v",
			cfg.DownloadConcurrency, cfg.VerifyConcurrency, cfg.Simultaneous)
		verifier.SetConcurrency(cfg.VerifyConcurrency)
	}

	rpt.OnTaskAssign = func(dirIDs []int32) {
		log.Printf("Task assigned: %d directories", len(dirIDs))
		assignedMu.Lock()
		assignedDirIDs = append(assignedDirIDs, dirIDs...)
		assignedMu.Unlock()

		for _, dirID := range dirIDs {
			go processDirectory(ctx, tree, dirID, cfg, stateManager, downloader, verifier, rpt)
		}
	}

	rpt.OnTaskRevoke = func(dirIDs []int32) {
		log.Printf("Task revoked: %v (not yet implemented)", dirIDs)
	}

	go rpt.readLoop(ctx)

	go func() {
		select {
		case <-ctx.Done():
			return
		case resp := <-rpt.UpdateRequired:
			log.Printf("Server requires a different worker version (reconnect 426), initiating self-update...")
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			var updateInfo protocol.UpdateRequiredResponse
			if err := json.Unmarshal(body, &updateInfo); err != nil {
				log.Printf("Failed to parse update response: %v", err)
				return
			}
			log.Printf("Current: %s → Latest: %s", updateInfo.CurrentVersion, updateInfo.LatestVersion)
			if err := worker.Apply(updateInfo); err != nil {
				log.Printf("Self-update failed: %v", err)
				return
			}
			os.Exit(0)
		}
	}()

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
				rpt.SendHeartbeat(ctx, freeGB, int(downloader.Downloading()), int(verifier.Count()), aria2Status)
			}
		}
	}()

	<-ctx.Done()
	rpt.Flush(context.Background())
	log.Println("Worker stopped")
}
