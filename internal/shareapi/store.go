package shareapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNotFound is returned by Store.Get when the code does not exist.
var ErrNotFound = errors.New("share code not found")

// ErrCodeExists is returned by Store.Put when the chosen code already
// exists. Handlers retry with a fresh code on this error.
var ErrCodeExists = errors.New("share code already exists")

// Store wraps a SQLite database holding gzip-compressed share payloads
// keyed by short code. The schema is intentionally minimal — bumping
// the layout is a manual migration.
type Store struct {
	db *sql.DB
}

// Open initialises (or upgrades) the SQLite database at path. The file
// is created on first use. WAL mode is enabled so reads do not block
// the single writer.
func Open(path string) (*Store, error) {
	// _txlock=immediate makes writes acquire the write lock at BEGIN
	// rather than first write — avoids "database is locked" surprises
	// under contention with the single-writer share-api workload.
	//
	// The path goes through file: URI form to survive characters that
	// would otherwise be parsed as DSN delimiters (?, #, &).
	dsn := "file:" + url.PathEscape(path) +
		"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(on)&_pragma=busy_timeout(5000)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	db.SetMaxOpenConns(1) // SQLite is single-writer; serialise at the pool.

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS reports (
			code       TEXT    PRIMARY KEY,
			payload    BLOB    NOT NULL,
			created_at INTEGER NOT NULL
		) STRICT
	`); err != nil {
		db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}

	return &Store{db: db}, nil
}

// Close releases the database file.
func (s *Store) Close() error {
	return s.db.Close()
}

// Put inserts a payload under the given code. Returns ErrCodeExists if
// the code collides with an existing row — callers should generate a
// new code and retry.
func (s *Store) Put(ctx context.Context, code string, payload []byte) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO reports (code, payload, created_at) VALUES (?, ?, ?)`,
		code, payload, time.Now().Unix())
	if err == nil {
		return nil
	}
	// modernc.org/sqlite returns a generic error containing "UNIQUE
	// constraint failed" — match by substring since the typed sentinel
	// is buried under driver-specific types.
	if isUniqueViolation(err) {
		return ErrCodeExists
	}
	return fmt.Errorf("insert report: %w", err)
}

// Get returns the stored payload for the given code, or ErrNotFound.
func (s *Store) Get(ctx context.Context, code string) ([]byte, error) {
	var payload []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT payload FROM reports WHERE code = ?`, code).
		Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("select report: %w", err)
	}
	return payload, nil
}

// Count returns the number of stored reports. Used by /healthz to
// surface basic operability without exposing internals.
func (s *Store) Count(ctx context.Context) (int64, error) {
	var n int64
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM reports`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count: %w", err)
	}
	return n, nil
}

// DeleteOlderThan removes rows whose created_at is older than cutoff.
// Returns the number of rows deleted. The receiver runs this on a
// timer to keep the PVC from filling and bricking the deployment.
//
// A separate VACUUM is intentionally NOT issued — SQLite reuses freed
// pages within the existing file, which is exactly what we want under
// a fixed-size PVC.
func (s *Store) DeleteOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM reports WHERE created_at < ?`, cutoff.Unix())
	if err != nil {
		return 0, fmt.Errorf("delete old reports: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func isUniqueViolation(err error) bool {
	// Substring check keeps the driver dependency loose — modernc/sqlite
	// reports unique violations as a wrapped sqlite3.Error whose string
	// form contains "UNIQUE constraint failed".
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
