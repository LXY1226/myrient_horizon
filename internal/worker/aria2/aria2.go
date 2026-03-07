package aria2

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
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
	wmu       sync.Mutex // protects writes to conn
	idCounter atomic.Int64
	cfg       Config // stored for reconnection

	// RPC call-response matching.
	pendingMu sync.Mutex
	pending   map[int64]chan rpcResponse

	// Events emits download completion/failure notifications.
	// This channel persists across reconnections.
	Events chan Event

	done chan struct{} // closed when readLoop exits
}

// Event is emitted when an aria2 download completes or fails.
type Event struct {
	GID     string
	Success bool
}

// Config for the aria2c subprocess.
type Config struct {
	Aria2cPath     string // path to aria2c binary
	DownloadDir    string
	RPCPort        int
	ConfPath       string
	ExtraArgs      []string // additional command-line arguments
	ExternalRPCURL string   // non-empty = external mode (no subprocess)
}

// NewClient creates and starts an aria2c subprocess, or connects to an external one.
func NewClient(cfg Config) (*Client, error) {
	c := &Client{
		cfg:     cfg,
		pending: make(map[int64]chan rpcResponse),
		Events:  make(chan Event, 256),
		done:    make(chan struct{}),
	}

	if cfg.ExternalRPCURL != "" {
		// External mode: connect to existing aria2 RPC.
		c.rpcURL = cfg.ExternalRPCURL
		if err := c.connect(); err != nil {
			return nil, fmt.Errorf("connect to external aria2: %w", err)
		}
	} else {
		// Managed mode: start subprocess + connect.
		c.rpcURL = fmt.Sprintf("ws://localhost:%d/jsonrpc", cfg.RPCPort)
		if err := c.start(cfg); err != nil {
			return nil, err
		}
		time.Sleep(500 * time.Millisecond)
		if err := c.connect(); err != nil {
			return nil, fmt.Errorf("connect to aria2c: %w", err)
		}
	}

	go c.readLoop()
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
		fmt.Sprintf("--conf=%s", cfg.ConfPath),
		"--rpc-listen-all=false",
		"--auto-file-renaming=false",
		"--allow-overwrite=false",
		"--continue=true",
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

// Done returns a channel that is closed when the current connection's readLoop exits.
func (c *Client) Done() <-chan struct{} {
	return c.done
}

// Managed returns true if this client manages its own aria2c subprocess.
func (c *Client) Managed() bool {
	return c.cfg.ExternalRPCURL == ""
}

// cleanupForReconnect closes the old connection, waits for readLoop to exit,
// and fails all pending RPC calls.
func (c *Client) cleanupForReconnect() {
	if c.conn != nil {
		c.conn.Close()
	}
	<-c.done

	c.pendingMu.Lock()
	for id, ch := range c.pending {
		ch <- rpcResponse{Error: "reconnecting"}
		delete(c.pending, id)
	}
	c.pendingMu.Unlock()

	c.done = make(chan struct{})
}

// ReconnectOnly attempts to reconnect to the existing aria2 process (no restart).
func (c *Client) ReconnectOnly() error {
	c.cleanupForReconnect()

	if err := c.connect(); err != nil {
		return err
	}
	go c.readLoop()
	return nil
}

// RestartProcess kills the managed aria2c process and starts a new one.
// Must only be called in managed mode.
func (c *Client) RestartProcess() error {
	if !c.Managed() {
		return fmt.Errorf("RestartProcess called in external mode")
	}

	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
		_ = c.cmd.Wait()
	}

	if err := c.start(c.cfg); err != nil {
		return err
	}
	time.Sleep(500 * time.Millisecond)
	if err := c.connect(); err != nil {
		return err
	}
	go c.readLoop()
	return nil
}

// AddURI submits a download task to aria2c. Returns the GID.
func (c *Client) AddURI(ctx context.Context, uri string, dir string, filename string) (string, error) {
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
	return c.callRPC(ctx, req)
}

// readLoop is the background goroutine that reads all messages from the aria2
// WebSocket connection and dispatches them.
func (c *Client) readLoop() {
	defer close(c.done)
	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			log.Printf("aria2: readLoop error: %v", err)
			// Fail all pending RPC calls.
			c.pendingMu.Lock()
			for id, ch := range c.pending {
				ch <- rpcResponse{Error: fmt.Sprintf("connection closed: %v", err)}
				delete(c.pending, id)
			}
			c.pendingMu.Unlock()
			return
		}

		// Peek at the message to determine if it's an RPC response or a notification.
		var peek struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(data, &peek); err != nil {
			log.Printf("aria2: bad message: %v", err)
			continue
		}

		if peek.ID != nil {
			// RPC response — route to pending caller.
			var resp rpcResponse
			if err := json.Unmarshal(data, &resp); err != nil {
				log.Printf("aria2: bad rpc response: %v", err)
				continue
			}
			c.pendingMu.Lock()
			ch, ok := c.pending[resp.ID]
			if ok {
				delete(c.pending, resp.ID)
			}
			c.pendingMu.Unlock()
			if ok {
				ch <- resp
			}
		} else if peek.Method != "" {
			// Notification from aria2.
			c.handleNotification(peek.Method, peek.Params)
		}
	}
}

// handleNotification processes aria2 event notifications.
func (c *Client) handleNotification(method string, params json.RawMessage) {
	var events []struct {
		GID string `json:"gid"`
	}
	if err := json.Unmarshal(params, &events); err != nil || len(events) == 0 {
		log.Printf("aria2: bad notification params for %s: %v", method, err)
		return
	}

	gid := events[0].GID
	switch method {
	case "aria2.onDownloadComplete":
		c.Events <- Event{GID: gid, Success: true}
	case "aria2.onDownloadError":
		c.Events <- Event{GID: gid, Success: false}
	}
}

// callRPCRaw sends a JSON-RPC request and returns the raw result.
func (c *Client) callRPCRaw(ctx context.Context, req rpcRequest) (json.RawMessage, error) {
	ch := make(chan rpcResponse, 1)
	c.pendingMu.Lock()
	c.pending[req.ID] = ch
	c.pendingMu.Unlock()

	data, err := json.Marshal(req)
	if err != nil {
		c.pendingMu.Lock()
		delete(c.pending, req.ID)
		c.pendingMu.Unlock()
		return nil, err
	}

	c.wmu.Lock()
	err = c.conn.WriteMessage(websocket.TextMessage, data)
	c.wmu.Unlock()
	if err != nil {
		c.pendingMu.Lock()
		delete(c.pending, req.ID)
		c.pendingMu.Unlock()
		return nil, err
	}

	select {
	case resp := <-ch:
		if resp.Error != nil {
			return nil, fmt.Errorf("aria2 error: %v", resp.Error)
		}
		return resp.Result, nil
	case <-ctx.Done():
		c.pendingMu.Lock()
		delete(c.pending, req.ID)
		c.pendingMu.Unlock()
		return nil, ctx.Err()
	case <-c.done:
		return nil, fmt.Errorf("aria2 connection closed")
	}
}

// callRPC sends a JSON-RPC request and returns the result as a string (e.g. GID).
func (c *Client) callRPC(ctx context.Context, req rpcRequest) (string, error) {
	raw, err := c.callRPCRaw(ctx, req)
	if err != nil {
		return "", err
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", fmt.Errorf("unexpected result type: %s", string(raw))
	}
	return s, nil
}

// Close shuts down the aria2 connection and, in managed mode, the subprocess.
func (c *Client) Close() {
	if c.conn != nil {
		c.conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, "shutdown"))
		c.conn.Close()
	}
	if c.Managed() && c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
		_ = c.cmd.Wait()
	}
}

// IsAlive checks if the aria2c connection/process is still running.
func (c *Client) IsAlive() bool {
	if !c.Managed() {
		// In external mode, check if the done channel is still open.
		select {
		case <-c.done:
			return false
		default:
			return true
		}
	}
	if c.cmd == nil || c.cmd.Process == nil {
		return false
	}
	return c.cmd.ProcessState == nil
}

// DownloadInfo holds the subset of fields returned by tellActive / tellWaiting.
type DownloadInfo struct {
	GID   string     `json:"gid"`
	Dir   string     `json:"dir"`
	Files []FileInfo `json:"files"`
}

// FileInfo is one entry from the "files" array in an aria2 status object.
type FileInfo struct {
	Path string `json:"path"`
}

// TellActive returns the list of active downloads.
func (c *Client) TellActive(ctx context.Context) ([]DownloadInfo, error) {
	id := c.idCounter.Add(1)
	req := rpcRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  "aria2.tellActive",
		Params:  []any{[]string{"gid", "dir", "files"}},
	}
	raw, err := c.callRPCRaw(ctx, req)
	if err != nil {
		return nil, err
	}
	var result []DownloadInfo
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("unmarshal tellActive: %w", err)
	}
	return result, nil
}

// TellWaiting returns waiting downloads starting at offset, up to num entries.
func (c *Client) TellWaiting(ctx context.Context, offset, num int) ([]DownloadInfo, error) {
	id := c.idCounter.Add(1)
	req := rpcRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  "aria2.tellWaiting",
		Params:  []any{offset, num, []string{"gid", "dir", "files"}},
	}
	raw, err := c.callRPCRaw(ctx, req)
	if err != nil {
		return nil, err
	}
	var result []DownloadInfo
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("unmarshal tellWaiting: %w", err)
	}
	return result, nil
}

// TellStatus returns the status for a specific GID.
func (c *Client) TellStatus(ctx context.Context, gid string) (DownloadInfo, error) {
	id := c.idCounter.Add(1)
	req := rpcRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  "aria2.tellStatus",
		Params:  []any{gid, []string{"gid", "dir", "files"}},
	}
	raw, err := c.callRPCRaw(ctx, req)
	if err != nil {
		return DownloadInfo{}, err
	}
	var result DownloadInfo
	if err := json.Unmarshal(raw, &result); err != nil {
		return DownloadInfo{}, fmt.Errorf("unmarshal tellStatus: %w", err)
	}
	return result, nil
}

// JSON-RPC types.
type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  []any  `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   any             `json:"error,omitempty"`
}
