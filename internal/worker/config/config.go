package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"myrient-horizon/pkg/protocol"
)

// WorkerConfig is the persistent local configuration for a worker.
type WorkerConfig struct {
	Key           string       `json:"key"`
	ServerURL     string       `json:"server_url"`
	Name          string       `json:"name"`
	TreeFile      string       `json:"tree_file"`
	DownloadDir   string       `json:"download_dir"`
	Aria2cPath    string       `json:"aria2c_path"`
	Aria2Port     int          `json:"aria2_port"`
	Aria2Conf     string       `json:"aria2_conf"`
	Aria2Args     []string     `json:"aria2_args"`
	Aria2RPCURL   string       `json:"aria2_rpc_url"`
	DiskMinGB     float64      `json:"disk_min_gb"`
	HeartBeatIntv int          `json:"heart_beat_interval"`
	RuntimeState  RuntimeState `json:"runtime_state,omitempty"`
}

type RuntimeState struct {
	Claims          protocol.ReclaimsSyncMsg `json:"claims,omitempty"`
	CompletedDirIDs []int32                  `json:"completed_dir_ids,omitempty"`
}

// Global is the worker configuration singleton (Pattern 1: Direct global).
// Set during bootstrap by EnsureConfig() and accessed throughout the worker.
var Global WorkerConfig

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
	if c.HeartBeatIntv == 0 {
		c.HeartBeatIntv = 3
	}
}

const configFileName = "worker.json"

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

func Save(dir string, cfg *WorkerConfig) error {
	cfg.applyDefaults()
	path := filepath.Join(dir, configFileName)
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, configFileName+".*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = os.Remove(tmpPath)
	}()

	if _, err := io.Copy(tmp, bytes.NewReader(data)); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Rename(tmpPath, path)
}

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

func SyncName(dir string, cfg *WorkerConfig) error {
	req, err := http.NewRequest(http.MethodGet, cfg.ServerURL+"/stats/me", nil)
	if err != nil {
		return fmt.Errorf("build stats request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Key)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("fetch worker stats: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch worker stats: status %d", resp.StatusCode)
	}

	var result struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode worker stats: %w", err)
	}

	name := strings.TrimSpace(result.Name)
	if name == "" || name == cfg.Name {
		return nil
	}
	cfg.Name = name
	if err := Save(dir, cfg); err != nil {
		return fmt.Errorf("save synced config: %w", err)
	}
	return nil
}
