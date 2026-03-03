package db

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

const schema = `
CREATE TABLE IF NOT EXISTS workers (
    id          SERIAL PRIMARY KEY,
    key_hash    TEXT UNIQUE NOT NULL,
    name        TEXT,
    config      JSONB DEFAULT '{}',
    created_at  TIMESTAMPTZ DEFAULT now()
);

CREATE TABLE IF NOT EXISTS claims (
    id          SERIAL PRIMARY KEY,
    dir_id      INT NOT NULL,
    worker_id   INT NOT NULL REFERENCES workers(id),
    claimed_at  TIMESTAMPTZ DEFAULT now(),
    released_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_claims_worker ON claims(worker_id);
CREATE INDEX IF NOT EXISTS idx_claims_dir ON claims(dir_id);

CREATE TABLE IF NOT EXISTS file_status (
    dir_id      INT NOT NULL,
    file_idx    INT NOT NULL,
    worker_id   INT NOT NULL REFERENCES workers(id),
    status      SMALLINT NOT NULL,
    sha1        BYTEA,
    crc32       BYTEA,
    updated_at  TIMESTAMPTZ DEFAULT now(),
    PRIMARY KEY (dir_id, file_idx, worker_id)
);
CREATE INDEX IF NOT EXISTS idx_fstatus_dir ON file_status(dir_id);
`

// Store wraps the PostgreSQL connection pool and provides data access methods.
type Store struct {
	pool *pgxpool.Pool
}

// New creates a new Store and runs schema migration.
func New(ctx context.Context, connString string) (*Store, error) {
	pool, err := pgxpool.New(ctx, connString)
	if err != nil {
		return nil, fmt.Errorf("connect to db: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping db: %w", err)
	}
	if _, err := pool.Exec(ctx, schema); err != nil {
		pool.Close()
		return nil, fmt.Errorf("migrate schema: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Close shuts down the connection pool.
func (s *Store) Close() {
	s.pool.Close()
}

// ---- Workers ----

type Worker struct {
	ID        int       `json:"id"`
	Name      string    `json:"name"`
	Config    []byte    `json:"config"` // raw JSONB
	CreatedAt time.Time `json:"created_at"`
}

// RegisterWorker creates a new worker, returning the ID and plaintext key.
func (s *Store) RegisterWorker(ctx context.Context, name string) (id int, key string, err error) {
	// Generate a random key: mh_ + 32 hex chars.
	raw := make([]byte, 16)
	if _, err = rand.Read(raw); err != nil {
		return 0, "", fmt.Errorf("generate key: %w", err)
	}
	key = "mh_" + hex.EncodeToString(raw)

	hash, err := bcrypt.GenerateFromPassword([]byte(key), bcrypt.DefaultCost)
	if err != nil {
		return 0, "", fmt.Errorf("hash key: %w", err)
	}

	err = s.pool.QueryRow(ctx,
		`INSERT INTO workers (key_hash, name) VALUES ($1, $2) RETURNING id`,
		string(hash), name,
	).Scan(&id)
	if err != nil {
		return 0, "", fmt.Errorf("insert worker: %w", err)
	}
	return id, key, nil
}

// AuthenticateWorker finds a worker by verifying the plaintext key against stored hashes.
// Returns the worker ID or 0 if not found.
func (s *Store) AuthenticateWorker(ctx context.Context, key string) (int, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, key_hash FROM workers`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int
		var hash string
		if err := rows.Scan(&id, &hash); err != nil {
			return 0, err
		}
		if bcrypt.CompareHashAndPassword([]byte(hash), []byte(key)) == nil {
			return id, nil
		}
	}
	return 0, nil
}

// GetWorker returns a single worker by ID.
func (s *Store) GetWorker(ctx context.Context, id int) (*Worker, error) {
	w := &Worker{}
	err := s.pool.QueryRow(ctx,
		`SELECT id, name, config, created_at FROM workers WHERE id = $1`, id,
	).Scan(&w.ID, &w.Name, &w.Config, &w.CreatedAt)
	if err != nil {
		return nil, err
	}
	return w, nil
}

// ListWorkers returns all registered workers.
func (s *Store) ListWorkers(ctx context.Context) ([]Worker, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, config, created_at FROM workers ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var workers []Worker
	for rows.Next() {
		var w Worker
		if err := rows.Scan(&w.ID, &w.Name, &w.Config, &w.CreatedAt); err != nil {
			return nil, err
		}
		workers = append(workers, w)
	}
	return workers, nil
}

// UpdateWorkerConfig sets the JSONB config for a worker.
func (s *Store) UpdateWorkerConfig(ctx context.Context, workerID int, config []byte) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE workers SET config = $1 WHERE id = $2`, config, workerID)
	return err
}

// ---- Claims ----

type Claim struct {
	ID         int        `json:"id"`
	DirID      int        `json:"dir_id"`
	WorkerID   int        `json:"worker_id"`
	ClaimedAt  time.Time  `json:"claimed_at"`
	ReleasedAt *time.Time `json:"released_at,omitempty"`
}

// ClaimDir records a directory claim for a worker.
func (s *Store) ClaimDir(ctx context.Context, workerID int, dirID int32) (int, error) {
	var id int
	err := s.pool.QueryRow(ctx,
		`INSERT INTO claims (dir_id, worker_id) VALUES ($1, $2) RETURNING id`,
		dirID, workerID,
	).Scan(&id)
	return id, err
}

// ReleaseDir marks a claim as released.
func (s *Store) ReleaseDir(ctx context.Context, workerID int, dirID int32) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE claims SET released_at = now()
		 WHERE worker_id = $1 AND dir_id = $2 AND released_at IS NULL`,
		workerID, dirID)
	return err
}

// ActiveClaimsForWorker returns all unreleased claims for a worker.
func (s *Store) ActiveClaimsForWorker(ctx context.Context, workerID int) ([]Claim, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, dir_id, worker_id, claimed_at FROM claims
		 WHERE worker_id = $1 AND released_at IS NULL ORDER BY dir_id`,
		workerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var claims []Claim
	for rows.Next() {
		var c Claim
		if err := rows.Scan(&c.ID, &c.DirID, &c.WorkerID, &c.ClaimedAt); err != nil {
			return nil, err
		}
		claims = append(claims, c)
	}
	return claims, nil
}

// ---- File Status ----

type FileStatus struct {
	DirID    int32  `json:"dir_id"`
	FileIdx  int32  `json:"file_idx"`
	WorkerID int    `json:"worker_id"`
	Status   int16  `json:"status"`
	SHA1     []byte `json:"sha1,omitempty"`
	CRC32    []byte `json:"crc32,omitempty"`
}

// UpsertFileStatus inserts or updates a file status record.
func (s *Store) UpsertFileStatus(ctx context.Context, fs *FileStatus) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO file_status (dir_id, file_idx, worker_id, status, sha1, crc32)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (dir_id, file_idx, worker_id)
		 DO UPDATE SET status=EXCLUDED.status, sha1=EXCLUDED.sha1, crc32=EXCLUDED.crc32, updated_at=now()`,
		fs.DirID, fs.FileIdx, fs.WorkerID, fs.Status, fs.SHA1, fs.CRC32)
	return err
}

// UpsertFileStatusBatch performs a batch upsert of file status records.
func (s *Store) UpsertFileStatusBatch(ctx context.Context, records []FileStatus) error {
	batch := &pgx.Batch{}
	for i := range records {
		r := &records[i]
		batch.Queue(
			`INSERT INTO file_status (dir_id, file_idx, worker_id, status, sha1, crc32)
			 VALUES ($1, $2, $3, $4, $5, $6)
			 ON CONFLICT (dir_id, file_idx, worker_id)
			 DO UPDATE SET status=EXCLUDED.status, sha1=EXCLUDED.sha1, crc32=EXCLUDED.crc32, updated_at=now()`,
			r.DirID, r.FileIdx, r.WorkerID, r.Status, r.SHA1, r.CRC32,
		)
	}
	br := s.pool.SendBatch(ctx, batch)
	defer br.Close()
	for range records {
		if _, err := br.Exec(); err != nil {
			return err
		}
	}
	return nil
}

// ScanAllFileStatus streams every row from file_status for startup recovery,
// invoking fn for each row without buffering the entire result set in memory.
func (s *Store) ScanAllFileStatus(ctx context.Context, fn func(*FileStatus) error) error {
	rows, err := s.pool.Query(ctx,
		`SELECT dir_id, file_idx, worker_id, status, sha1, crc32 FROM file_status`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var fs FileStatus
		if err := rows.Scan(&fs.DirID, &fs.FileIdx, &fs.WorkerID, &fs.Status, &fs.SHA1, &fs.CRC32); err != nil {
			return err
		}
		if err := fn(&fs); err != nil {
			return err
		}
	}
	return rows.Err()
}

// FileStatusByDir returns all file_status rows for a specific directory.
func (s *Store) FileStatusByDir(ctx context.Context, dirID int32) ([]FileStatus, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT dir_id, file_idx, worker_id, status, sha1, crc32
		 FROM file_status WHERE dir_id = $1 ORDER BY file_idx, worker_id`, dirID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []FileStatus
	for rows.Next() {
		var fs FileStatus
		if err := rows.Scan(&fs.DirID, &fs.FileIdx, &fs.WorkerID, &fs.Status, &fs.SHA1, &fs.CRC32); err != nil {
			return nil, err
		}
		result = append(result, fs)
	}
	return result, nil
}

// ConflictFiles returns file indices that have conflicting SHA1 hashes in a directory.
func (s *Store) ConflictFiles(ctx context.Context, dirID int32) ([]int32, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT file_idx FROM file_status WHERE dir_id = $1
		 GROUP BY file_idx HAVING COUNT(DISTINCT sha1) FILTER (WHERE sha1 IS NOT NULL) > 1`,
		dirID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var idxs []int32
	for rows.Next() {
		var idx int32
		if err := rows.Scan(&idx); err != nil {
			return nil, err
		}
		idxs = append(idxs, idx)
	}
	return idxs, nil
}
