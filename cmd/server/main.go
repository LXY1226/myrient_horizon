package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"myrient-horizon/internal/server"
)

func main() {
	addr := flag.String("addr", ":8099", "HTTP listen address")
	dbURL := flag.String("db", "postgres://myrient:myrient_dev_password@localhost:5432/myrient?sslmode=disable", "PostgreSQL connection string")
	dataDir := flag.String("data", "data", "data directory")
	workerVersion := flag.String("worker-version", "", "expected worker version (empty = skip check)")
	workerURL := flag.String("worker-url", "", "download URL for the latest worker binary")
	workerSHA256 := flag.String("worker-sha256", "", "SHA-256 hex digest of the latest worker binary")
	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("server: shutting down...")
		cancel()
	}()

	// 1. Load flatbuffer tree.
	treePath := *dataDir + "/full_tree.fbd"
	log.Printf("server: loading tree from %s...", treePath)
	serverTree, err := server.LoadTree(treePath)
	if err != nil {
		log.Fatalf("server: failed to load tree: %v", err)
	}
	log.Printf("server: tree loaded: %d dirs, %d files", len(serverTree.Base().Dirs), len(serverTree.Base().Files))

	// 2. Connect to PostgreSQL.
	log.Printf("server: connecting to database...")
	server.InitDB(*dbURL)
	defer server.GetDB().Close()
	log.Printf("server: database connected, schema migrated")

	rootStats := serverTree.GetDirStats(0)
	log.Printf("server: state initialized: %d total, %d downloaded, %d verified, %d archived, %d failed, %d conflicts",
		rootStats.Total, rootStats.Downloaded, rootStats.Verified, rootStats.Archived, rootStats.Failed, rootStats.Conflict)

	// 4. Set up WebSocket hub and HTTP handlers.
	hub := server.NewHub()
	hub.WorkerVersion = *workerVersion
	hub.WorkerDownloadURL = *workerURL
	hub.WorkerSHA256 = *workerSHA256
	if *workerVersion != "" {
		log.Printf("server: worker version check enabled: expecting %s", *workerVersion)
	}
	h := server.New(hub)

	mux := http.NewServeMux()
	handlerWithCors := h.Register(mux)

	srv := &http.Server{
		Addr:         *addr,
		Handler:      handlerWithCors,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 0,
		IdleTimeout:  60 * time.Second,
	}

	// 5. Create listener synchronously.
	ln, err := getListener(*addr)
	if err != nil {
		log.Fatalf("server: %v", err)
	}
	log.Printf("server: listening on %s", ln.Addr())

	errCh := make(chan error, 1)

	// 6. Start goroutines.
	go hub.Run(ctx)
	go func() {
		if err := srv.ServeTLS(ln, *dataDir+"/cert.pem", *dataDir+"/key.pem"); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	// 7. Wait for shutdown signal or error.
	select {
	case <-ctx.Done():
	case err := <-errCh:
		log.Printf("server: server error: %v", err)
		cancel()
	}

	// 8. Graceful shutdown.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("server: shutdown error: %v", err)
	}

	log.Println("server: stopped")
}
