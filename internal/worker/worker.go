package worker

import (
	"log"
	"os"

	"myrient-horizon/internal/worker/config"
)

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
