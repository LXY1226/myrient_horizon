package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

"myrient-horizon/internal/worker"
"myrient-horizon/internal/worker/aria2"
"myrient-horizon/pkg/protocol"
)

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

	worker.InitWorker(aria2Client)

	go worker.Reporter.RunContext(ctx)
	go worker.Verifier.Run(ctx)
	go worker.Downloader.Run(ctx)

	<-ctx.Done()

	// Graceful shutdown: close reporter and wait for cleanup
	if worker.Reporter != nil {
		wg := worker.Reporter.Close()
		wg.Wait()
	}

	log.Println("Worker stopped")
}
