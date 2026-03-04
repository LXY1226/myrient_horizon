package worker

import (
	"log"
	"myrient-horizon/internal/worker/config"
	"os"
)

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
		log.Fatalf("No config found. A default worker.json has been created in %s — please edit it and restart.", workDir)
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
	return cfg
}
