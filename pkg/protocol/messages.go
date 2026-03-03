package protocol

import "encoding/json"

// Status constants for file processing states.
const (
	StatusNone uint8 = iota
	StatusDownloading
	StatusDownloaded
	StatusVerifying
	StatusVerified
	StatusFailed
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
	DirID   int32  `json:"dir_id"`
	FileIdx int32  `json:"file_idx"`
	Status  uint8  `json:"status"`
	SHA1    string `json:"sha1,omitempty"`
	CRC32   string `json:"crc32,omitempty"`
}

// HeartbeatMsg is sent periodically by the worker.
type HeartbeatMsg struct {
	Type        string  `json:"type"` // "heartbeat"
	DiskFreeGB  float64 `json:"disk_free_gb"`
	Downloading int     `json:"downloading"`
	Verifying   int     `json:"verifying"`
	Aria2Status string  `json:"aria2_status"`
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
