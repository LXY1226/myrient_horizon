// Package protocol defines message types and serialization utilities for
// worker-server communication over WebSocket connections.
package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// TaskStatus represents the processing state of a file.
type TaskStatus uint8

// Status constants for file processing states.
const (
	StatusNone       TaskStatus = 0
	StatusDownloaded TaskStatus = 1
	StatusVerified   TaskStatus = 2
	StatusArchived   TaskStatus = 3
	StatusFailed     TaskStatus = 4
)

const (
	MessagePing = "ping"
	MessagePong = "pong"

	MessageTaskSync           = "task_sync"
	MessageHeartbeat          = "heartbeat"
	MessageStatusSyncRequest  = "status_sync_request"
	MessageStatusSyncResponse = "status_sync_response"

	HeaderWorkerVersion = "X-Worker-Version"
	HeaderTreeSHA1      = "X-Tree-SHA1"
	HeaderReason        = "X-Reason"
	HeaderRetryAfter    = "X-Retry-After"
)

// UnmarshalConnMessage splits a connection message into type and payload.
// The format expects a null byte separator: "type\x00payload".
func UnmarshalConnMessage(data []byte) (string, []byte, error) {
	i := bytes.IndexByte(data, 0x00)
	if i == -1 {
		return "", data, fmt.Errorf("invalid message format: missing null separator")
	}
	return string(data[:i]), data[i+1:], nil
}

// UnmarshalMessage deserializes JSON data into a typed value.
func UnmarshalMessage[T any](data []byte) (*T, error) {
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, fmt.Errorf("unmarshal message: %w", err)
	}
	return &v, nil
}

// MarshalConnMessage serializes a typed value with a message type prefix.
// Output format: "type\x00jsondata".
func MarshalConnMessage[T any](msgType string, data T) []byte {
	wr := bytes.NewBuffer(nil)
	wr.WriteString(msgType)
	wr.WriteByte(0)
	_ = json.NewEncoder(wr).Encode(data)
	return wr.Bytes()
}

// --------------- Worker → Server ---------------

// PingMsg is a batch of file status reports from a worker.
type PingMsg struct {
	Version     int32
	Verified    []VerifyReport
	Downloading []DownloadReport
	Status      WorkerStatus
}

type DownloadReport struct {
	FileID     int32
	Downloaded int64
	Speed      float32
}

type VerifyReport struct {
	FileID int32
	SHA1   []byte
	CRC32  []byte
}

type WorkerStatus struct {
	RemainDownload int
	QueueVerify    int
}

type PongMsg struct {
	Version int32
}

// HeartbeatMsg is sent periodically by the worker.
type HeartbeatMsg struct {
	Type        string  `json:"type"` // "heartbeat"
	DiskFreeGB  float64 `json:"disk_free_gb"`
	Aria2Status string  `json:"aria2_status"`
}

// StatusSyncRequest is sent by worker on startup to request full status sync.
type StatusSyncRequest struct {
	Type string `json:"type"` // "status_sync_request"
}

// StatusSyncResponse is sent by server with all item statuses for this worker.
type StatusSyncResponse struct {
	Type    string       `json:"type"` // "status_sync_response"
	Records []ItemStatus `json:"records"`
}

type ItemStatus struct {
	FileID int64  `json:"file_id"`
	Status uint8  `json:"status"`
	SHA1   string `json:"sha1,omitempty"`
	CRC32  string `json:"crc32,omitempty"`
}

// --------------- Server → Worker ---------------

// ConfigUpdateMsg pushes configuration changes to a worker.
type ConfigUpdateMsg struct {
	Type   string       `json:"type"` // "config_update"
	Config WorkerConfig `json:"config"`
}

type WorkerConfig struct {
	DownloadConcurrency int  `json:"download_concurrency"`
	VerifyConcurrency   int  `json:"verify_concurrency"`
	Simultaneous        bool `json:"simultaneous"`
}

// ReclaimsSyncMsg is sent by server to sync reclaim list to worker.
type ReclaimsSyncMsg []Reclaim

type Reclaim struct {
	DirID   int32
	IsBlack bool `json:",omitempty"`
}

// UpdateRequiredResponse is the JSON body returned by the server when
// a worker's version does not match the expected version (HTTP 426).
type UpdateRequiredResponse struct {
	Error           string `json:"error"` // "update_required"
	Target          string `json:"target,omitempty"`
	CurrentVersion  string `json:"current_version,omitempty"`
	LatestVersion   string `json:"latest_version,omitempty"`
	CurrentTreeSHA1 string `json:"current_tree_sha1,omitempty"`
	LatestTreeSHA1  string `json:"latest_tree_sha1,omitempty"`
	DownloadURL     string `json:"download_url,omitempty"`
	SHA256          string `json:"sha256,omitempty"`
}
