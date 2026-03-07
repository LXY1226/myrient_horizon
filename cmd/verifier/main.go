package main

import (
"bufio"
"context"
"log"
"myrient-horizon/internal/worker"
	"myrient-horizon/internal/worker/config"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
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
		log.Fatalf("No registered worker.json found in %s; run the worker first to register, or create one manually.", workDir)
	}
	config.Global = *cfg
	log.Printf("Loaded config: worker %s", cfg.Name)

	// Bootstrap: Initialize reporter and verifier
	worker.InitReporter()
	worker.InitVerifier()

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

	worker.LoadTree(cfg.TreeFile)

	// Start reporter in goroutine (non-blocking)
	go worker.Reporter.Run(cfg.ServerURL+"/ws", cfg.Key)

	badReport, err := os.OpenFile("bad_report.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Fatalf("Failed to open bad_report.log: %v", err)
	}
	bw := bufio.NewWriter(badReport)

	var matchedCount, verifiedCount, failedCount int
	log.Printf("Scanning files...")
	err = filepath.Walk(scanDir, func(path string, fi os.FileInfo, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil || fi.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".downloading") {
			return nil
		}
		if _, err := os.Stat(path + ".aria2"); err == nil {
			return nil
		}

		task, err := worker.MatchTaskByPath(filepath.Split(path))
		if err != nil {
			bw.WriteString(path)
			bw.WriteString(": ")
			bw.WriteString(err.Error())
			bw.WriteString("\n")
			return nil
		}
		matchedCount++

		err = task.Verify()
		if err != nil {
			bw.WriteString(path)
			bw.WriteString(": Verification failed: ")
			bw.WriteString(err.Error())
			bw.WriteString("\n")
			log.Printf("Verification failed for %s: %v", task.GetFile().Name, err)
			failedCount++
			//worker.Reporter.PushVerified(ctx, task.FileID, protocol.StatusFailed, nil, nil)
			return nil
		}

		verifiedCount++
		return nil
	})
	if err != nil && ctx.Err() == nil {
		log.Printf("Walk error: %v", err)
	}

	log.Printf("Done: %d matched, %d verified, %d failed, waiting for reports...", matchedCount, verifiedCount, failedCount)
	wg := worker.Reporter.Close()
	bw.Flush()
	badReport.Close()
	wg.Wait()
}
