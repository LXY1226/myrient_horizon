package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
)

// WorkerConfig is the persistent local configuration for a worker.
type WorkerConfig struct {
	Key           string   `json:"key"`
	ServerURL     string   `json:"server_url"`
	Name          string   `json:"name"`
	TreeFile      string   `json:"tree_file"`
	DownloadDir   string   `json:"download_dir"`
	Aria2cPath    string   `json:"aria2c_path"`
	Aria2Port     int      `json:"aria2_port"`
	Aria2Conf     string   `json:"aria2_conf"`
	Aria2Args     []string `json:"aria2_args"`
	Aria2RPCURL   string   `json:"aria2_rpc_url"` // non-empty = external aria2 mode
	DiskMinGB     float64  `json:"disk_min_gb"`
	HeartBeatIntv int      `json:"heart_beat_interval"`
}

// Global is the worker configuration singleton (Pattern 1: Direct global).
// Set during bootstrap by EnsureConfig() and accessed throughout the worker.
// Server alignment note: Server-side code uses Pattern 2 (Init/Get), but
// workers use this direct global pattern for simplicity in single-process setup.
var Global WorkerConfig

// applyDefaults fills zero-value fields with sensible defaults.
func (c *WorkerConfig) applyDefaults() {
	if c.ServerURL == "" {
		c.ServerURL = "https://myrient.imlxy.net/api"
	}
	if c.TreeFile == "" {
		c.TreeFile = "full_tree.fbd"
	}
	if c.DownloadDir == "" {
		c.DownloadDir = "downloads"
	}
	if c.Aria2cPath == "" {
		c.Aria2cPath = "./aria2c.exe"
	}
	if c.Aria2Port == 0 {
		c.Aria2Port = 6800
	}
	if c.Aria2Conf == "" {
		c.Aria2Conf = "aria2.conf"
	}
	if c.DiskMinGB == 0 {
		c.DiskMinGB = 10.0
	}
}

const configFileName = "worker.json"

// Load reads the worker config from the current directory.
// Returns nil if the file does not exist.
func Load(dir string) (*WorkerConfig, error) {
	path := filepath.Join(dir, configFileName)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var cfg WorkerConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	cfg.applyDefaults()
	return &cfg, nil
}

// Save writes the worker config to the current directory.
func Save(dir string, cfg *WorkerConfig) error {
	cfg.applyDefaults()
	path := filepath.Join(dir, configFileName)
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// Register calls the server's registration API and saves the config locally.
func Register(dir, serverURL, name string) (*WorkerConfig, error) {
	body, _ := json.Marshal(map[string]string{"name": name})
	resp, err := http.Post(serverURL+"/register", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("register request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("register failed: status %d", resp.StatusCode)
	}

	var result struct {
		ID  int    `json:"id"`
		Key string `json:"key"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	cfg := &WorkerConfig{
		Key:       result.Key,
		ServerURL: serverURL,
		Name:      name,
	}
	cfg.applyDefaults()
	if err := Save(dir, cfg); err != nil {
		return nil, fmt.Errorf("save config: %w", err)
	}
	return cfg, nil
}
