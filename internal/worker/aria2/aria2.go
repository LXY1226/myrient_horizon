package aria2

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// Client manages an aria2c subprocess and communicates via JSON-RPC over WebSocket.
type Client struct {
	cmd       *exec.Cmd
	rpcURL    string
	conn      *websocket.Conn
	mu        sync.Mutex
	idCounter atomic.Int64

	downloadDir string
	sessionFile string
	rpcPort     int

	// Callbacks.
	OnComplete func(gid string)
	OnError    func(gid string)
}

// Config for the aria2c subprocess.
type Config struct {
	Aria2cPath  string // path to aria2c binary
	DownloadDir string
	RPCPort     int
	MaxConcur   int
	SessionFile string
	ExtraArgs   []string // additional command-line arguments
}

// NewClient creates and starts an aria2c subprocess.
func NewClient(cfg Config) (*Client, error) {
	c := &Client{
		downloadDir: cfg.DownloadDir,
		sessionFile: cfg.SessionFile,
		rpcPort:     cfg.RPCPort,
		rpcURL:      fmt.Sprintf("ws://localhost:%d/jsonrpc", cfg.RPCPort),
	}

	if err := c.start(cfg); err != nil {
		return nil, err
	}

	// Wait a bit for aria2c to start up, then connect.
	time.Sleep(500 * time.Millisecond)
	if err := c.connect(); err != nil {
		return nil, fmt.Errorf("connect to aria2c: %w", err)
	}

	return c, nil
}

func (c *Client) start(cfg Config) error {
	aria2cPath := cfg.Aria2cPath
	if aria2cPath == "" {
		aria2cPath = "aria2c"
	}
	downloadDir, err := filepath.Abs(cfg.DownloadDir)
	if err != nil {
		return err
	}

	args := []string{
		"--enable-rpc",
		fmt.Sprintf("--rpc-listen-port=%d", cfg.RPCPort),
		fmt.Sprintf("--dir=%s", downloadDir),
		fmt.Sprintf("--max-concurrent-downloads=%d", cfg.MaxConcur),
		"--rpc-listen-all=false",
		"--auto-file-renaming=false",
		"--allow-overwrite=false",
		"--continue=true",
	}
	if cfg.SessionFile != "" {
		f, err := os.OpenFile(cfg.SessionFile, os.O_RDONLY|os.O_CREATE, 0666)
		if err != nil {
			return err
		}
		f.Close()
		args = append(args,
			fmt.Sprintf("--save-session=%s", cfg.SessionFile),
			fmt.Sprintf("--input-file=%s", cfg.SessionFile),
		)
	}

	args = append(args, cfg.ExtraArgs...)
	log.Printf("Starting aria2c...")
	log.Println("aria2c", args)
	c.cmd = exec.Command(aria2cPath, args...)
	setPlatformAttrs(c.cmd)
	if err := c.cmd.Start(); err != nil {
		return fmt.Errorf("start aria2c: %w", err)
	}
	log.Printf("aria2c started (pid %d) on port %d", c.cmd.Process.Pid, cfg.RPCPort)
	return nil
}

func (c *Client) connect() error {
	var err error
	for i := 0; i < 20; i++ {
		//if c.cmd != nil {
		//	if c.cmd.ProcessState != nil && c.cmd.ProcessState.Exited() {
		//		log.Printf("aria2c exited unexpectedly")
		//		return nil
		//	}
		//}
		c.conn, _, err = websocket.DefaultDialer.Dial(c.rpcURL, http.Header{})
		if err == nil {
			log.Println("Connected to aria2")
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("aria2c not ready after 10s: %w", err)
}

// Restart kills and restarts the aria2c process.
func (c *Client) Restart(cfg Config) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil {
		c.conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, "restarting"))
		c.conn.Close()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
		_ = c.cmd.Wait()
	}

	if err := c.start(cfg); err != nil {
		return err
	}
	time.Sleep(500 * time.Millisecond)
	return c.connect()
}

// AddURI submits a download task to aria2c. Returns the GID.
func (c *Client) AddURI(ctx context.Context, uri string, dir string, filename string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	id := c.idCounter.Add(1)
	req := rpcRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  "aria2.addUri",
		Params: []any{
			[]string{uri},
			map[string]string{
				"dir": dir,
				"out": filename,
			},
		},
	}
	return c.call(ctx, req)
}

// GetStatus retrieves the status of a download by GID.
func (c *Client) GetStatus(ctx context.Context, gid string) (map[string]any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	id := c.idCounter.Add(1)
	req := rpcRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  "aria2.tellStatus",
		Params:  []any{gid},
	}

	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if err := c.conn.WriteMessage(websocket.TextMessage, data); err != nil {
		return nil, err
	}
	_, respData, err := c.conn.ReadMessage()
	if err != nil {
		return nil, err
	}
	var resp rpcResponse
	if err := json.Unmarshal(respData, &resp); err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("aria2 error: %v", resp.Error)
	}
	result, ok := resp.Result.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("unexpected result type")
	}
	return result, nil
}

func (c *Client) call(ctx context.Context, req rpcRequest) (string, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	if err := c.conn.WriteMessage(websocket.TextMessage, data); err != nil {
		return "", err
	}
	_, respData, err := c.conn.ReadMessage()
	if err != nil {
		return "", err
	}
	var resp rpcResponse
	if err := json.Unmarshal(respData, &resp); err != nil {
		return "", err
	}
	if resp.Error != nil {
		return "", fmt.Errorf("aria2 error: %v", resp.Error)
	}
	gid, ok := resp.Result.(string)
	if !ok {
		return "", fmt.Errorf("unexpected result type: %T", resp.Result)
	}
	return gid, nil
}

// Close shuts down the aria2c process.
func (c *Client) Close() {
	if c.conn != nil {
		c.conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, "shutdown"))
		c.conn.Close()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
		_ = c.cmd.Wait()
	}
}

// IsAlive checks if the aria2c process is still running.
func (c *Client) IsAlive() bool {
	if c.cmd == nil || c.cmd.Process == nil {
		return false
	}
	// On Windows, Process.Signal(0) isn't supported the same way.
	// Check if Wait() has already returned.
	return c.cmd.ProcessState == nil
}

// JSON-RPC types.
type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  []any  `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Result  any    `json:"result,omitempty"`
	Error   any    `json:"error,omitempty"`
}
