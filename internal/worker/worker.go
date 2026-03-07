package worker

import (
	"log"
	"os"

	"myrient-horizon/internal/worker/aria2"
	"myrient-horizon/internal/worker/config"
)

const defaultDownloaderTaskBuffer = 1024

// EnsureConfig loads or initializes worker configuration following the bootstrap pattern:
// 1. Load from disk, fail if corrupted
// 2. Auto-create default if missing (then exit with instructions)
// 3. Auto-register with server if no key (one-time setup)
// 4. Set config.Global singleton for package-wide access
// Returns: *config.WorkerConfig - always valid or fatal
func EnsureConfig() *config.WorkerConfig {
	workDir, _ := os.Getwd()

	cfg, err := config.Load(workDir)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}
	if cfg == nil {
		hostname, _ := os.Hostname()
		cfg = &config.WorkerConfig{Name: "worker-" + hostname}
		if err := config.Save(workDir, cfg); err != nil {
			log.Fatalf("Failed to save default config: %v", err)
		}
		log.Fatalf("No config found. A default worker.json has been created in %s; please edit it and restart.", workDir)
	}
	if cfg.Key == "" {
		log.Printf("Registering with server as %q...", cfg.Name)
		cfg, err = config.Register(workDir, cfg.ServerURL, cfg.Name)
		if err != nil {
			log.Fatalf("Failed to register: %v", err)
		}
		log.Println("Registered as worker", cfg.Name)
		log.Println("===================")
		log.Println("Worker Key:", cfg.Key)
		log.Println("===================")
	} else {
		log.Println("Loaded config: worker", cfg.Name)
	}
	config.Global = *cfg
	return cfg
}

// InitWorker completes worker bootstrap after EnsureConfig() and LoadTree().
// Initialization order is fixed so later runtimes can depend on earlier ones:
// 1. Validate config/tree/bootstrap inputs.
// 2. Initialize Reporter from config.Global.
// 3. Initialize Verifier, which requires Reporter.
// 4. Initialize Downloader, which requires the aria2 client.
// Failures are fatal because a partially initialized worker is unusable.
func InitWorker(aria2Client *aria2.Client) {
	if config.Global.Key == "" {
		log.Fatal("InitWorker: config.Global not loaded (Key is empty)")
	}
	if config.Global.ServerURL == "" {
		log.Fatal("InitWorker: config.Global not loaded (ServerURL is empty)")
	}
	if config.Global.TreeFile == "" {
		log.Fatal("InitWorker: config.Global not loaded (TreeFile is empty)")
	}
	if config.Global.DownloadDir == "" {
		log.Fatal("InitWorker: config.Global not loaded (DownloadDir is empty)")
	}
	if config.Global.HeartBeatIntv <= 0 {
		log.Fatal("InitWorker: config.Global invalid (HeartBeatIntv must be > 0)")
	}
	if iTree == nil {
		log.Fatal("InitWorker: task tree not loaded")
	}
	if aria2Client == nil {
		log.Fatal("InitWorker: aria2 client is required")
	}

	initReporter()
	if Reporter == nil {
		log.Fatal("InitWorker: reporter initialization failed")
	}

	InitVerifier()
	if Verifier == nil {
		log.Fatal("InitWorker: verifier initialization failed")
	}

	InitDownloader(aria2Client, defaultDownloaderTaskBuffer)
	if Downloader == nil {
		log.Fatal("InitWorker: downloader initialization failed")
	}
}
