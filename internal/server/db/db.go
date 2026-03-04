package db

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

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

CREATE TABLE IF NOT EXISTS item_status (
    worker_id   INT NOT NULL REFERENCES workers(id),
    file_id     BIGINT NOT NULL,
    status      SMALLINT NOT NULL,
    sha1        BYTEA,
    crc32       BYTEA,
    updated_at  TIMESTAMPTZ DEFAULT now(),
    PRIMARY KEY (worker_id, file_id)
);
CREATE INDEX IF NOT EXISTS idx_item_status_worker ON item_status(worker_id);
CREATE INDEX IF NOT EXISTS idx_item_status_file ON item_status(file_id);

CREATE TABLE IF NOT EXISTS reclaims (
    id          SERIAL PRIMARY KEY,
    worker_id   INT NOT NULL REFERENCES workers(id),
    dir_id      INT NOT NULL,
    is_black    BOOLEAN NOT NULL DEFAULT false,
    created_at  TIMESTAMPTZ DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_reclaims_worker ON reclaims(worker_id);

CREATE TABLE IF NOT EXISTS archive_status (
    id          SERIAL PRIMARY KEY,
    worker_id   INT NOT NULL REFERENCES workers(id),
    file_id     BIGINT NOT NULL,
    archive_id  INT NOT NULL,
    created_at  TIMESTAMPTZ DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_archive_status_worker ON archive_status(worker_id);

CREATE TABLE IF NOT EXISTS archived (
    id          SERIAL PRIMARY KEY,
    raw_json    JSONB NOT NULL,
    created_at  TIMESTAMPTZ DEFAULT now()
);
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

// ---- Item Status ----

type ItemStatus struct {
	WorkerID int
	FileID   int64
	Status   int16
	SHA1     []byte
	CRC32    []byte
}

// UpsertItemStatus inserts or updates an item status record.
func (s *Store) UpsertItemStatus(ctx context.Context, item *ItemStatus) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO item_status (worker_id, file_id, status, sha1, crc32)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (worker_id, file_id)
		 DO UPDATE SET status=EXCLUDED.status, sha1=EXCLUDED.sha1, crc32=EXCLUDED.crc32, updated_at=now()`,
		item.WorkerID, item.FileID, item.Status, item.SHA1, item.CRC32)
	return err
}

// ScanAllItemStatusByWorker streams all item_status rows for a specific worker.
func (s *Store) ScanAllItemStatusByWorker(ctx context.Context, workerID int, fn func(*ItemStatus) error) error {
	rows, err := s.pool.Query(ctx,
		`SELECT worker_id, file_id, status, sha1, crc32 FROM item_status WHERE worker_id = $1`, workerID)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var item ItemStatus
		if err := rows.Scan(&item.WorkerID, &item.FileID, &item.Status, &item.SHA1, &item.CRC32); err != nil {
			return err
		}
		if err := fn(&item); err != nil {
			return err
		}
	}
	return rows.Err()
}

// ---- Reclaims ----

type Reclaim struct {
	ID        int       `json:"id"`
	WorkerID  int       `json:"worker_id"`
	DirID     int       `json:"dir_id"`
	IsBlack   bool      `json:"is_black"`
	CreatedAt time.Time `json:"created_at"`
}

// GetReclaimsByWorker returns all reclaims for a specific worker.
func (s *Store) GetReclaimsByWorker(ctx context.Context, workerID int) ([]Reclaim, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, worker_id, dir_id, is_black, created_at FROM reclaims WHERE worker_id = $1 ORDER BY id`,
		workerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var reclaims []Reclaim
	for rows.Next() {
		var r Reclaim
		if err := rows.Scan(&r.ID, &r.WorkerID, &r.DirID, &r.IsBlack, &r.CreatedAt); err != nil {
			return nil, err
		}
		reclaims = append(reclaims, r)
	}
	return reclaims, nil
}

// InsertReclaim creates a new reclaim record.
func (s *Store) InsertReclaim(ctx context.Context, workerID, dirID int, isBlack bool) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO reclaims (worker_id, dir_id, is_black) VALUES ($1, $2, $3)`,
		workerID, dirID, isBlack)
	return err
}

// DeleteReclaim removes a reclaim record by ID.
func (s *Store) DeleteReclaim(ctx context.Context, reclaimID int) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM reclaims WHERE id = $1`, reclaimID)
	return err
}

// ---- Archive Status ----

type ArchiveStatus struct {
	ID        int       `json:"id"`
	WorkerID  int       `json:"worker_id"`
	FileID    int64     `json:"file_id"`
	ArchiveID int       `json:"archive_id"`
	CreatedAt time.Time `json:"created_at"`
}

// GetArchiveStatusByWorker returns all archive status for a specific worker.
func (s *Store) GetArchiveStatusByWorker(ctx context.Context, workerID int) ([]ArchiveStatus, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, worker_id, file_id, archive_id, created_at FROM archive_status WHERE worker_id = $1 ORDER BY id`,
		workerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var statuses []ArchiveStatus
	for rows.Next() {
		var a ArchiveStatus
		if err := rows.Scan(&a.ID, &a.WorkerID, &a.FileID, &a.ArchiveID, &a.CreatedAt); err != nil {
			return nil, err
		}
		statuses = append(statuses, a)
	}
	return statuses, nil
}

// ---- Archived ----

type Archived struct {
	ID        int       `json:"id"`
	RawJSON   []byte    `json:"raw_json"`
	CreatedAt time.Time `json:"created_at"`
}

// InsertArchived creates a new archived record.
func (s *Store) InsertArchived(ctx context.Context, rawJSON []byte) (int, error) {
	var id int
	err := s.pool.QueryRow(ctx,
		`INSERT INTO archived (raw_json) VALUES ($1) RETURNING id`,
		rawJSON).Scan(&id)
	return id, err
}

// GetArchivedByID returns an archived record by ID.
func (s *Store) GetArchivedByID(ctx context.Context, id int) (*Archived, error) {
	a := &Archived{}
	err := s.pool.QueryRow(ctx,
		`SELECT id, raw_json, created_at FROM archived WHERE id = $1`, id,
	).Scan(&a.ID, &a.RawJSON, &a.CreatedAt)
	if err != nil {
		return nil, err
	}
	return a, nil
}
