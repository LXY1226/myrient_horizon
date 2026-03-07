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
		log.Println("server: shutting down...")
		cancel()
	}()

	log.Printf("server: loading tree from %s...", *dataDir+"/full_tree.fbd")
	baseTree, err := myrienttree.LoadFromFile[stree.DirExt, stree.FileExt](*dataDir + "/full_tree.fbd")
	if err != nil {
		log.Fatalf("server: failed to load tree: %v", err)
	}
	log.Printf("server: tree loaded: %d dirs, %d files", len(baseTree.Dirs), len(baseTree.Files))

	log.Printf("server: connecting to database...")
	db, err := stree.InitDB(ctx, *dbURL)
	if err != nil {
		log.Fatalf("server: failed to connect to DB: %v", err)
	}
	defer db.Close()
	log.Printf("server: database connected, schema migrated")

	tree := stree.InitTree(baseTree)
	rootStats := tree.GetDirStats(0)
	log.Printf("server: state initialized: %d total, %d downloaded, %d verified, %d archived, %d failed, %d conflicts",
		rootStats.Total, rootStats.Downloaded, rootStats.Verified, rootStats.Archived, rootStats.Failed, rootStats.Conflict)

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

	<-ctx.Done()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("server: shutdown error: %v", err)
	}
	log.Println("server: stopped")
}
