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

	sigCh := make(chan os.Signal, 3)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		count := 0
		for sig := range sigCh {
			count++
			switch count {
			case 1:
				log.Printf("server: received %s, shutting down...", sig)
				cancel()
			case 2:
				log.Printf("server: received %s again, press Ctrl+C once more to force exit", sig)
			default:
				log.Printf("server: received %s third time, forcing exit", sig)
				os.Exit(1)
			}
		}
	}()

	// 1. Load flatbuffer tree.
	log.Printf("server: loading tree from %s...", *dataDir+"/full_tree.fbd")
	baseTree, err := myrienttree.LoadFromFile[stree.DirExt, stree.FileExt](*dataDir + "/full_tree.fbd")
	if err != nil {
		log.Fatalf("server: failed to load tree: %v", err)
	}
	log.Printf("server: tree loaded: %d dirs, %d files", len(baseTree.Dirs), len(baseTree.Files))

	// 2. Connect to PostgreSQL.
	log.Printf("server: connecting to database...")
	stree.DB, err = stree.NewStore(ctx, *dbURL)
	if err != nil {
		log.Fatalf("server: failed to connect to DB: %v", err)
	}
	defer stree.DB.Close()
	log.Printf("server: database connected, schema migrated")

	// 3. Build server tree.
	stree.Tree = stree.NewTree(baseTree)
	rootStats := stree.Tree.GetDirStats(0)
	log.Printf("server: state initialized: %d total, %d downloaded, %d verified, %d archived, %d failed, %d conflicts",
		rootStats.Total, rootStats.Downloaded, rootStats.Verified, rootStats.Archived, rootStats.Failed, rootStats.Conflict)

	// 4. Set up WebSocket hub and HTTP handlers.
	hub := stree.NewHub()
	go hub.Run(ctx)
	hub.WorkerVersion = *workerVersion
	hub.WorkerDownloadURL = *workerURL
	hub.WorkerSHA256 = *workerSHA256
	if *workerVersion != "" {
		log.Printf("server: worker version check enabled: expecting %s", *workerVersion)
	}
	h := stree.New(hub)

	mux := http.NewServeMux()
	handlerWithCors := h.Register(mux)

	server := &http.Server{
		Addr:         *addr,
		Handler:      handlerWithCors,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 0,
		IdleTimeout:  60 * time.Second,
	}

	// 5. Start server.
	go func() {
		ln, err := getListener(*addr)
		if err != nil {
			log.Fatalf("server: %v", err)
		}
		log.Printf("server: listening on %s", ln.Addr())
		if err := server.ServeTLS(ln, *dataDir+"/cert.pem", *dataDir+"/key.pem"); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server: server error: %v", err)
		}
	}()

	// 6. Wait for shutdown signal via context.
	<-ctx.Done()

	// 7. Graceful shutdown.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("server: shutdown error: %v", err)
	}
	cancel()
	log.Println("Server stopped")
}
