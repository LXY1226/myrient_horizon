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

	stree "myrient-horizon/internal/server"
	"myrient-horizon/pkg/myrienttree"
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
		log.Println("Shutting down...")
		cancel()
	}()

	// 1. Load flatbuffer tree.
	log.Printf("Loading tree from %s...", *dataDir+"/full_tree.fbd")
	baseTree, err := myrienttree.LoadFromFile[stree.DirExt, stree.FileExt](*dataDir + "/full_tree.fbd")
	if err != nil {
		log.Fatalf("Failed to load tree: %v", err)
	}
	log.Printf("Tree loaded: %d dirs, %d files", len(baseTree.Dirs), len(baseTree.Files))

	// 2. Connect to PostgreSQL.
	log.Printf("Connecting to database...")
	stree.DB, err = stree.New(ctx, *dbURL)
	if err != nil {
		log.Fatalf("Failed to connect to DB: %v", err)
	}
	defer stree.DB.Close()
	log.Printf("Database connected, schema migrated")

	// 3. Build server tree.
	stree.Tree = stree.New(baseTree)
	// Note: With the new design, workers request their own status on connect.
	// No need to recover state or load claims here.

	rootStats := stree.Tree.GetDirStats(0)
	log.Printf("State initialized: %d total, %d downloaded, %d verified, %d archived, %d failed, %d conflicts",
		rootStats.Total, rootStats.Downloaded, rootStats.Verified, rootStats.Archived, rootStats.Failed, rootStats.Conflict)

	// 4. Set up WebSocket hub and HTTP handlers.
	hub := stree.NewHub()
	hub.WorkerVersion = *workerVersion
	hub.WorkerDownloadURL = *workerURL
	hub.WorkerSHA256 = *workerSHA256
	if *workerVersion != "" {
		log.Printf("Worker version check enabled: expecting %s", *workerVersion)
	}
	h := stree.New(hub)

	mux := http.NewServeMux()
	handlerWithCors := h.Register(mux)

	server := &http.Server{
		Addr:         *addr,
		Handler:      handlerWithCors,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 0, // SSE needs no write timeout.
		IdleTimeout:  60 * time.Second,
	}

	// 5. Start server.
	go func() {
		ln, err := getListener(*addr)
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("Server listening on %s", ln.Addr())
		if err := server.ServeTLS(ln, *dataDir+"/cert.pem", *dataDir+"/key.pem"); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server error: %v", err)
		}
	}()

	// 6. Wait for shutdown signal via context.
	<-ctx.Done()

	// 7. Graceful shutdown.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("Shutdown error: %v", err)
	}
	log.Println("Server stopped")
}
