package config

import (
	"os"
	"path/filepath"
	"testing"

	"myrient-horizon/pkg/protocol"
)

func TestLoadExistingWorkerConfigWithKeyStillWorks(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, configFileName)
	data := []byte(`{"key":"mh_existing456","server_url":"https://example.test/api","name":"existing-worker"}`)
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(tempDir)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected config to load")
	}
	if cfg.Key != "mh_existing456" {
		t.Fatalf("expected key %q, got %q", "mh_existing456", cfg.Key)
	}
	if cfg.ServerURL != "https://example.test/api" {
		t.Fatalf("expected server URL %q, got %q", "https://example.test/api", cfg.ServerURL)
	}
	if cfg.Name != "existing-worker" {
		t.Fatalf("expected name %q, got %q", "existing-worker", cfg.Name)
	}
	if cfg.TreeFile == "" {
		t.Fatal("expected defaults to be applied for existing config")
	}
}

func TestRuntimeStateRoundTrip(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	cfg := &WorkerConfig{
		Key:         "mh_existing456",
		ServerURL:   "https://example.test/api",
		Name:        "existing-worker",
		DownloadDir: "downloads",
		RuntimeState: RuntimeState{
			Claims: protocol.ReclaimsSyncMsg{
				{DirID: 1},
				{DirID: 2, IsBlack: true},
			},
			CompletedDirIDs: []int32{3, 5},
		},
	}

	if err := Save(tempDir, cfg); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	loaded, err := Load(tempDir)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected config to load")
	}
	if len(loaded.RuntimeState.Claims) != 2 {
		t.Fatalf("claims len = %d, want 2", len(loaded.RuntimeState.Claims))
	}
	if loaded.RuntimeState.Claims[1].DirID != 2 || !loaded.RuntimeState.Claims[1].IsBlack {
		t.Fatalf("claims[1] = %+v, want dir_id=2 is_black=true", loaded.RuntimeState.Claims[1])
	}
	if len(loaded.RuntimeState.CompletedDirIDs) != 2 || loaded.RuntimeState.CompletedDirIDs[0] != 3 || loaded.RuntimeState.CompletedDirIDs[1] != 5 {
		t.Fatalf("completed_dir_ids = %v, want [3 5]", loaded.RuntimeState.CompletedDirIDs)
	}
}
