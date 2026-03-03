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

	"myrient-horizon/internal/server/db"
	"myrient-horizon/internal/server/handler"
	stree "myrient-horizon/internal/server/tree"
	"myrient-horizon/internal/server/wsrpc"
	"myrient-horizon/pkg/myrienttree"
)

func main() {
	addr := flag.String("addr", ":8080", "HTTP listen address")
	dbURL := flag.String("db", "postgres://myrient:myrient_dev_password@localhost:5432/myrient?sslmode=disable", "PostgreSQL connection string")
	treeFile := flag.String("tree", "data/full_tree.fbd", "Path to flatbuffer tree file")
	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 1. Load flatbuffer tree.
	log.Printf("Loading tree from %s...", *treeFile)
	baseTree, err := myrienttree.LoadFromFile(*treeFile)
	if err != nil {
		log.Fatalf("Failed to load tree: %v", err)
	}
	log.Printf("Tree loaded: %d dirs, %d files", len(baseTree.Dirs), len(baseTree.Files))

	// 2. Connect to PostgreSQL.
	log.Printf("Connecting to database...")
	store, err := db.New(ctx, *dbURL)
	if err != nil {
		log.Fatalf("Failed to connect to DB: %v", err)
	}
	defer store.Close()
	log.Printf("Database connected, schema migrated")

	// 3. Build server tree and recover state from DB.
	serverTree := stree.New(baseTree)
	log.Printf("Recovering state from database...")
	if err := serverTree.RecoverFromDB(ctx, store); err != nil {
		log.Fatalf("Failed to recover state: %v", err)
	}
	rootStats := serverTree.GetDirStats(0)
	log.Printf("State recovered: %d total, %d verified, %d downloaded, %d failed, %d conflicts",
		rootStats.Total, rootStats.Verified, rootStats.Downloaded, rootStats.Failed, rootStats.Conflict)

	// 4. Set up WebSocket hub and HTTP handlers.
	hub := wsrpc.NewHub(store, serverTree)
	h := handler.New(store, serverTree, hub)

	mux := http.NewServeMux()
	h.Register(mux)

	server := &http.Server{
		Addr:         *addr,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 0, // SSE needs no write timeout.
		IdleTimeout:  60 * time.Second,
	}

	// 5. Start server.
	go func() {
		log.Printf("Server listening on %s", *addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server error: %v", err)
		}
	}()

	// 6. Graceful shutdown.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	log.Println("Shutting down...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("Shutdown error: %v", err)
	}
	cancel()
	log.Println("Server stopped")
}
