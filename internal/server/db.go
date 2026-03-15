package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

const schema = `
CREATE TABLE IF NOT EXISTS workers (
    id          SERIAL PRIMARY KEY,
    key         TEXT UNIQUE,
    key_hash    TEXT UNIQUE NOT NULL,
    name        TEXT,
    config      JSONB DEFAULT '{}',
    created_at  TIMESTAMPTZ DEFAULT now()
);
ALTER TABLE workers ADD COLUMN IF NOT EXISTS key TEXT;
CREATE UNIQUE INDEX IF NOT EXISTS idx_workers_key ON workers(key);

CREATE TABLE IF NOT EXISTS item_status (
    worker_id   INT NOT NULL REFERENCES workers(id),
    worker_key  TEXT,
    file_id     BIGINT NOT NULL,
    status      SMALLINT NOT NULL,
    sha1        BYTEA,
    crc32       BYTEA,
    updated_at  TIMESTAMPTZ DEFAULT now(),
    PRIMARY KEY (worker_id, file_id)
);
ALTER TABLE item_status ADD COLUMN IF NOT EXISTS worker_key TEXT;
CREATE INDEX IF NOT EXISTS idx_item_status_worker ON item_status(worker_id);
CREATE INDEX IF NOT EXISTS idx_item_status_worker_key ON item_status(worker_key);
CREATE INDEX IF NOT EXISTS idx_item_status_file ON item_status(file_id);

CREATE TABLE IF NOT EXISTS reclaims (
    id          SERIAL PRIMARY KEY,
    worker_id   INT NOT NULL REFERENCES workers(id),
    worker_key  TEXT,
    dir_id      INT NOT NULL,
    is_black    BOOLEAN NOT NULL DEFAULT false,
    created_at  TIMESTAMPTZ DEFAULT now()
);
ALTER TABLE reclaims ADD COLUMN IF NOT EXISTS worker_key TEXT;
-- Legacy reclaim rows may contain multiple active states for the same worker/dir.
-- Keep the newest row by id so startup cleanup and canonical reads agree.
DELETE FROM reclaims older
USING reclaims newer
WHERE older.worker_id = newer.worker_id
  AND older.dir_id = newer.dir_id
  AND older.id < newer.id;
CREATE UNIQUE INDEX IF NOT EXISTS idx_reclaims_worker_dir ON reclaims(worker_id, dir_id);
CREATE INDEX IF NOT EXISTS idx_reclaims_worker ON reclaims(worker_id);
CREATE INDEX IF NOT EXISTS idx_reclaims_worker_key ON reclaims(worker_key);

CREATE TABLE IF NOT EXISTS archive_status (
    id          SERIAL PRIMARY KEY,
    worker_id   INT NOT NULL REFERENCES workers(id),
    worker_key  TEXT,
    file_id     BIGINT NOT NULL,
    archive_id  INT NOT NULL,
    created_at  TIMESTAMPTZ DEFAULT now()
);
ALTER TABLE archive_status ADD COLUMN IF NOT EXISTS worker_key TEXT;
CREATE INDEX IF NOT EXISTS idx_archive_status_worker ON archive_status(worker_id);
CREATE INDEX IF NOT EXISTS idx_archive_status_worker_key ON archive_status(worker_key);

CREATE TABLE IF NOT EXISTS archived (
    id          SERIAL PRIMARY KEY,
    raw_json    JSONB NOT NULL,
    created_at  TIMESTAMPTZ DEFAULT now()
);
`

type Store struct {
	pool *pgxpool.Pool
}

var (
	dbInstance *Store
	dbOnce     sync.Once
)

// DB is the global Store instance (deprecated: use GetDB()).
var DB *Store

// InitDB initializes the singleton Store. Must be called once before GetDB().
// Pattern: sync.Once ensures thread-safe single initialization.
func InitDB(connString string) *Store {
	dbOnce.Do(func() {
		pool, err := pgxpool.New(context.Background(), connString)
		if err != nil {
			log.Fatalf("db: failed to connect: %v", err)
		}
		if err := pool.Ping(context.Background()); err != nil {
			pool.Close()
			log.Fatalf("db: failed to ping: %v", err)
		}
		if _, err := pool.Exec(context.Background(), schema); err != nil {
			pool.Close()
			log.Fatalf("db: failed to migrate schema: %v", err)
		}
		dbInstance = &Store{pool: pool}
		DB = dbInstance // Maintain backward compatibility
		log.Println("db: initialized successfully")
	})
	return dbInstance
}

// GetDB returns the singleton Store instance.
// Must be called after InitDB(). Returns nil if not initialized.
func GetDB() *Store {
	return dbInstance
}

// Close shuts down the database connection pool.
// Pattern: Lifecycle cleanup - call during graceful shutdown.
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
		`INSERT INTO workers (key, key_hash, name) VALUES ($1, $2, $3) RETURNING id`,
		key, string(hash), name,
	).Scan(&id)
	if err != nil {
		return 0, "", fmt.Errorf("insert worker: %w", err)
	}
	return id, key, nil
}

func (s *Store) AuthenticateWorker(ctx context.Context, key string) (int, error) {
	var id int
	err := s.pool.QueryRow(ctx, `SELECT id FROM workers WHERE key = $1`, key).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != pgx.ErrNoRows {
		return 0, err
	}

	rows, err := s.pool.Query(ctx, `SELECT id, key_hash FROM workers WHERE key IS NULL`)
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
			if _, err := s.pool.Exec(ctx,
				`UPDATE workers SET key = $1 WHERE id = $2 AND key IS NULL`,
				key, id,
			); err != nil {
				return 0, err
			}
			return id, nil
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return 0, nil
}

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

func (s *Store) UpdateWorkerConfig(ctx context.Context, workerID int, config []byte) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE workers SET config = $1 WHERE id = $2`, config, workerID)
	return err
}

func (s *Store) UpdateWorkerNameByKey(ctx context.Context, workerKey, name string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE workers SET name = $1 WHERE key = $2`, name, workerKey)
	return err
}

type ItemStatus struct {
	WorkerID  int
	WorkerKey string
	FileID    int32
	Status    int16
	SHA1      []byte
	CRC32     []byte
}

func (s *Store) UpsertItemStatus(ctx context.Context, item *ItemStatus) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO item_status (worker_id, file_id, status, sha1, crc32)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (worker_id, file_id)
		 DO UPDATE SET status=EXCLUDED.status, sha1=EXCLUDED.sha1, crc32=EXCLUDED.crc32, updated_at=now()`,
		item.WorkerID, item.FileID, item.Status, item.SHA1, item.CRC32)
	return err
}

func (s *Store) UpsertItemStatusByKey(ctx context.Context, key string, fileID int32, status int16, sha1, crc32 []byte) error {
	cmdTag, err := s.pool.Exec(ctx,
		`INSERT INTO item_status (worker_id, worker_key, file_id, status, sha1, crc32)
		 SELECT id, $1, $2, $3, $4, $5
		 FROM workers
		 WHERE key = $1
		 ON CONFLICT (worker_id, file_id)
		 DO UPDATE SET worker_key=EXCLUDED.worker_key, status=EXCLUDED.status, sha1=EXCLUDED.sha1, crc32=EXCLUDED.crc32, updated_at=now()`,
		key, fileID, status, sha1, crc32,
	)
	if err != nil {
		return err
	}
	if cmdTag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (s *Store) ScanAllItemStatusByWorker(ctx context.Context, workerID int, fn func(*ItemStatus) error) error {
	rows, err := s.pool.Query(ctx,
		`SELECT worker_id, COALESCE(worker_key, ''), file_id, status, sha1, crc32 FROM item_status WHERE worker_id = $1`, workerID)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var item ItemStatus
		if err := rows.Scan(&item.WorkerID, &item.WorkerKey, &item.FileID, &item.Status, &item.SHA1, &item.CRC32); err != nil {
			return err
		}
		if err := fn(&item); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (s *Store) ScanAllItemStatus(ctx context.Context, fn func(*ItemStatus) error) error {
	rows, err := s.pool.Query(ctx,
		`SELECT worker_id, COALESCE(worker_key, ''), file_id, status, sha1, crc32 FROM item_status`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var item ItemStatus
		if err := rows.Scan(&item.WorkerID, &item.WorkerKey, &item.FileID, &item.Status, &item.SHA1, &item.CRC32); err != nil {
			return err
		}
		if err := fn(&item); err != nil {
			return err
		}
	}
	return rows.Err()
}

type Reclaim struct {
	ID        int       `json:"id"`
	WorkerID  int       `json:"worker_id"`
	WorkerKey string    `json:"worker_key"`
	DirID     int       `json:"dir_id"`
	IsBlack   bool      `json:"is_black"`
	CreatedAt time.Time `json:"created_at"`
}

const reclaimSelectColumns = `id, worker_id, COALESCE(worker_key, ''), dir_id, is_black, created_at`

const canonicalReclaimsQuery = `
	SELECT id, worker_id, COALESCE(worker_key, ''), dir_id, is_black, created_at
	FROM (
		SELECT DISTINCT ON (worker_id, dir_id)
			id, worker_id, worker_key, dir_id, is_black, created_at
		FROM reclaims
		WHERE %s
		-- Match schema cleanup: highest id is the canonical effective reclaim.
		ORDER BY worker_id, dir_id, id DESC
	) effective
	ORDER BY id`

func (s *Store) GetReclaimsByWorker(ctx context.Context, workerID int) ([]Reclaim, error) {
	rows, err := s.pool.Query(ctx,
		fmt.Sprintf(canonicalReclaimsQuery, `worker_id = $1`),
		workerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var reclaims []Reclaim
	for rows.Next() {
		var r Reclaim
		if err := rows.Scan(&r.ID, &r.WorkerID, &r.WorkerKey, &r.DirID, &r.IsBlack, &r.CreatedAt); err != nil {
			return nil, err
		}
		reclaims = append(reclaims, r)
	}
	return reclaims, nil
}

func (s *Store) GetReclaimsByKey(ctx context.Context, key string) ([]Reclaim, error) {
	rows, err := s.pool.Query(ctx,
		fmt.Sprintf(canonicalReclaimsQuery, `worker_key = $1
		   OR (worker_key IS NULL AND worker_id = (SELECT id FROM workers WHERE key = $1))`),
		key,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var reclaims []Reclaim
	for rows.Next() {
		var r Reclaim
		if err := rows.Scan(&r.ID, &r.WorkerID, &r.WorkerKey, &r.DirID, &r.IsBlack, &r.CreatedAt); err != nil {
			return nil, err
		}
		reclaims = append(reclaims, r)
	}
	return reclaims, rows.Err()
}

func (s *Store) GetEffectiveReclaim(ctx context.Context, workerID, dirID int) (*Reclaim, error) {
	reclaim := &Reclaim{}
	err := s.pool.QueryRow(ctx,
		`SELECT `+reclaimSelectColumns+`
		 FROM (
			SELECT DISTINCT ON (worker_id, dir_id)
				id, worker_id, worker_key, dir_id, is_black, created_at
			FROM reclaims
			WHERE worker_id = $1 AND dir_id = $2
			ORDER BY worker_id, dir_id, id DESC
		 ) effective`,
		workerID, dirID,
	).Scan(&reclaim.ID, &reclaim.WorkerID, &reclaim.WorkerKey, &reclaim.DirID, &reclaim.IsBlack, &reclaim.CreatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return reclaim, nil
}

func (s *Store) ScanAllReclaims(ctx context.Context, fn func(*Reclaim) error) error {
	rows, err := s.pool.Query(ctx,
		fmt.Sprintf(canonicalReclaimsQuery, `TRUE`))
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var reclaim Reclaim
		if err := rows.Scan(&reclaim.ID, &reclaim.WorkerID, &reclaim.WorkerKey, &reclaim.DirID, &reclaim.IsBlack, &reclaim.CreatedAt); err != nil {
			return err
		}
		if err := fn(&reclaim); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (s *Store) InsertReclaim(ctx context.Context, workerID, dirID int, isBlack bool) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO reclaims (worker_id, dir_id, is_black)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (worker_id, dir_id) DO UPDATE
		 SET is_black = EXCLUDED.is_black,
		     worker_key = COALESCE(reclaims.worker_key, EXCLUDED.worker_key),
		     created_at = now()`,
		workerID, dirID, isBlack)
	return err
}

func (s *Store) InsertReclaimByKey(ctx context.Context, key string, dirID int, isBlack bool) error {
	cmdTag, err := s.pool.Exec(ctx,
		`INSERT INTO reclaims (worker_id, worker_key, dir_id, is_black)
		 SELECT id, $1, $2, $3
		 FROM workers
		 WHERE key = $1
		 ON CONFLICT (worker_id, dir_id) DO UPDATE
		 SET worker_key = EXCLUDED.worker_key,
		     is_black = EXCLUDED.is_black,
		     created_at = now()`,
		key, dirID, isBlack,
	)
	if err != nil {
		return err
	}
	if cmdTag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (s *Store) SetEffectiveReclaimByKey(ctx context.Context, key string, dirID int, isBlack bool) (*Reclaim, error) {
	reclaim := &Reclaim{}
	err := s.pool.QueryRow(ctx,
		`INSERT INTO reclaims (worker_id, worker_key, dir_id, is_black)
		 SELECT id, $1, $2, $3
		 FROM workers
		 WHERE key = $1
		 ON CONFLICT (worker_id, dir_id) DO UPDATE
		 SET worker_key = EXCLUDED.worker_key,
		     is_black = EXCLUDED.is_black,
		     created_at = now()
		 RETURNING id, worker_id, COALESCE(worker_key, ''), dir_id, is_black, created_at`,
		key, dirID, isBlack,
	).Scan(&reclaim.ID, &reclaim.WorkerID, &reclaim.WorkerKey, &reclaim.DirID, &reclaim.IsBlack, &reclaim.CreatedAt)
	if err != nil {
		return nil, err
	}
	return reclaim, nil
}

func (s *Store) CreateReclaimByKey(ctx context.Context, key string, dirID int, isBlack bool) (*Reclaim, error) {
	return s.SetEffectiveReclaimByKey(ctx, key, dirID, isBlack)
}

func (s *Store) DeleteReclaim(ctx context.Context, reclaimID int) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM reclaims WHERE id = $1`, reclaimID)
	return err
}

func (s *Store) DeleteReclaimByKey(ctx context.Context, key string, reclaimID int) (bool, error) {
	var deleted bool
	err := s.pool.QueryRow(ctx,
		`WITH target AS (
			SELECT worker_id, dir_id
			FROM reclaims
			WHERE id = $1
			  AND (
				worker_key = $2
				OR (worker_key IS NULL AND worker_id = (SELECT id FROM workers WHERE key = $2))
			  )
		), deleted AS (
			DELETE FROM reclaims r
			USING target t
			WHERE r.worker_id = t.worker_id
			  AND r.dir_id = t.dir_id
			RETURNING 1
		)
		SELECT EXISTS(SELECT 1 FROM deleted)`,
		reclaimID, key,
	).Scan(&deleted)
	if err != nil {
		return false, err
	}
	return deleted, nil
}

type ArchiveStatus struct {
	ID        int       `json:"id"`
	WorkerID  int       `json:"worker_id"`
	WorkerKey string    `json:"worker_key"`
	FileID    int64     `json:"file_id"`
	ArchiveID int       `json:"archive_id"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Store) GetArchiveStatusByWorker(ctx context.Context, workerID int) ([]ArchiveStatus, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, worker_id, COALESCE(worker_key, ''), file_id, archive_id, created_at FROM archive_status WHERE worker_id = $1 ORDER BY id`,
		workerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var statuses []ArchiveStatus
	for rows.Next() {
		var a ArchiveStatus
		if err := rows.Scan(&a.ID, &a.WorkerID, &a.WorkerKey, &a.FileID, &a.ArchiveID, &a.CreatedAt); err != nil {
			return nil, err
		}
		statuses = append(statuses, a)
	}
	return statuses, nil
}

func (s *Store) GetArchiveStatusByKey(ctx context.Context, key string) ([]ArchiveStatus, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, worker_id, COALESCE(worker_key, ''), file_id, archive_id, created_at
		 FROM archive_status
		 WHERE worker_key = $1
		    OR (worker_key IS NULL AND worker_id = (SELECT id FROM workers WHERE key = $1))
		 ORDER BY id`,
		key,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var statuses []ArchiveStatus
	for rows.Next() {
		var a ArchiveStatus
		if err := rows.Scan(&a.ID, &a.WorkerID, &a.WorkerKey, &a.FileID, &a.ArchiveID, &a.CreatedAt); err != nil {
			return nil, err
		}
		statuses = append(statuses, a)
	}
	return statuses, rows.Err()
}

func (s *Store) InsertArchiveStatusByKey(ctx context.Context, key string, fileID int64, archiveID int) error {
	cmdTag, err := s.pool.Exec(ctx,
		`INSERT INTO archive_status (worker_id, worker_key, file_id, archive_id)
		 SELECT id, $1, $2, $3
		 FROM workers
		 WHERE key = $1`,
		key, fileID, archiveID,
	)
	if err != nil {
		return err
	}
	if cmdTag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

type Archived struct {
	ID        int       `json:"id"`
	RawJSON   []byte    `json:"raw_json"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Store) InsertArchived(ctx context.Context, rawJSON []byte) (int, error) {
	var id int
	err := s.pool.QueryRow(ctx,
		`INSERT INTO archived (raw_json) VALUES ($1) RETURNING id`,
		rawJSON).Scan(&id)
	return id, err
}

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
