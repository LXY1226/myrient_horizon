package protocol

import "encoding/json"

// Status constants for file processing states.
const (
	StatusNone       uint8 = 0
	StatusDownloaded uint8 = 1
	StatusVerified   uint8 = 2
	StatusArchived   uint8 = 3
	StatusFailed     uint8 = 4
)

// Envelope is a thin wrapper used to peek at the "type" field before full deserialization.
type Envelope struct {
	Type string          `json:"type"`
	Raw  json.RawMessage `json:"-"`
}

func ParseEnvelope(data []byte) (Envelope, error) {
	var env Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return env, err
	}
	env.Raw = data
	return env, nil
}

// --------------- Worker → Server ---------------

// FileReportMsg is a batch of file status reports from a worker.
type FileReportMsg struct {
	Type    string       `json:"type"` // "file_report"
	Reports []FileReport `json:"reports"`
}

type FileReport struct {
	FileID int64  `json:"file_id"` // 全局文件索引
	Status uint8  `json:"status"`
	SHA1   string `json:"sha1,omitempty"`
	CRC32  string `json:"crc32,omitempty"`
}

// HeartbeatMsg is sent periodically by the worker.
type HeartbeatMsg struct {
	Type        string  `json:"type"` // "heartbeat"
	DiskFreeGB  float64 `json:"disk_free_gb"`
	Downloading int     `json:"downloading"`
	Verifying   int     `json:"verifying"`
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

// TaskAssignMsg tells the worker which directories to process.
type TaskAssignMsg struct {
	Type   string  `json:"type"` // "task_assign"
	DirIDs []int32 `json:"dir_ids"`
}

// TaskRevokeMsg tells the worker to stop processing certain directories.
type TaskRevokeMsg struct {
	Type   string  `json:"type"` // "task_revoke"
	DirIDs []int32 `json:"dir_ids"`
}

// ReclaimsSyncMsg is sent by server to sync reclaim list to worker.
type ReclaimsSyncMsg struct {
	Type     string    `json:"type"` // "reclaims_sync"
	Reclaims []Reclaim `json:"reclaims"`
}

type Reclaim struct {
	DirID   int32 `json:"dir_id"`
	IsBlack bool  `json:"is_black"`
}

// UpdateRequiredResponse is the JSON body returned by the server when
// a worker's version does not match the expected version (HTTP 426).
type UpdateRequiredResponse struct {
	Error          string `json:"error"`           // "update_required"
	CurrentVersion string `json:"current_version"` // worker's reported version
	LatestVersion  string `json:"latest_version"`  // server's expected version
	DownloadURL    string `json:"download_url"`
	SHA256         string `json:"sha256"`
}
