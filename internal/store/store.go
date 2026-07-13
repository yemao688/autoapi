package store

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// StoreDeps configures the store. For production use, leave DSN empty to
// resolve via the default home directory path ($HOME/.autoapi/autoapi.db).
// For tests, set DSN to ":memory:" or a temp path.
type StoreDeps struct {
	DSN          string // empty = default home directory; overridable for tests
	DefaultPort  int    // zero preserves the production default (8344)
	SeedFixtures bool   // fixtures are opt-in and only available in dev builds
}

// Store is the SQLite-backed persistence layer implementing api.StoreService.
// All write operations go through the single-writer goroutine (Writer); reads
// go directly against the shared *sql.DB (safe under WAL mode).
type Store struct {
	db          *sql.DB
	writer      *Writer
	dsnPath     string
	defaultPort int
}

// New opens (or creates) the SQLite database, applies migrations, optionally
// seeds fixtures in a development build, and starts the writer goroutine.
func New(_ context.Context, deps StoreDeps) (*Store, error) {
	defaultPort := deps.DefaultPort
	if defaultPort == 0 {
		defaultPort = 8344
	}
	dsn := deps.DSN
	if dsn == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("store: resolve home dir: %w", err)
		}
		dsn = filepath.Join(home, ".autoapi", "autoapi.db")
		if err := os.MkdirAll(filepath.Dir(dsn), 0700); err != nil {
			return nil, fmt.Errorf("store: mkdir: %w", err)
		}
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	db.SetMaxOpenConns(1)
	slog.Info("store: db opened", "dsn", dsn)

	// Connection pragmas (oracle requirement)
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			db.Close()
			return nil, fmt.Errorf("store: pragma %q: %w", p, err)
		}
	}

	s := &Store{db: db, dsnPath: dsn, defaultPort: defaultPort}

	// Migrations
	slog.Info("store: migrations starting")
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: migrate: %w", err)
	}
	slog.Info("store: migrations complete")

	// Best-effort backfill of cost for pre-migration rows.

	// Writer
	slog.Info("store: writer goroutine starting", "buffer", 1024)
	s.writer = NewWriter(db, 1024)
	go s.writer.Run()

	if deps.SeedFixtures {
		seedFixtures(s)
	}

	return s, nil
}

// Close shuts down the writer and closes the database connection.
func (s *Store) Close() error {
	slog.Info("store: closing")
	s.writer.Close()
	return s.db.Close()
}

// StorageDir returns the directory containing the SQLite database file.
func (s *Store) StorageDir() string {
	if s.dsnPath == "" {
		return ""
	}
	return filepath.Dir(s.dsnPath)
}

// RawDB exposes the underlying *sql.DB for direct queries. Used by the
// service layer for master_password operations that don't fit the
// high-level interface. Do NOT close this handle.
func (s *Store) RawDB() *sql.DB { return s.db }

// ExecRaw runs an arbitrary write function inside a Writer transaction.
// Used by the service layer for low-level writes (master_password).
func (s *Store) ExecRaw(fn func(tx *sql.Tx) error) error {
	return s.execTx(fn)
}

// ---------------------------------------------------------------------------
//  Internal helpers
// ---------------------------------------------------------------------------

// execTx runs fn inside a single-writer transaction. Use for all INSERT /
// UPDATE / DELETE paths.
func (s *Store) execTx(fn func(tx *sql.Tx) error) error {
	return s.writer.Submit(fn)
}

// nowMs returns current Unix milliseconds.
func nowMs() int64 { return time.Now().UnixMilli() }

// makeID returns a new UUID string.
func makeID() string { return NewUUID() }

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...interface{}) error
}

// ---------------------------------------------------------------------------
//  Sentinel errors
// ---------------------------------------------------------------------------

var (
	ErrNotFound  = fmt.Errorf("store: not found")
	ErrConflict  = fmt.Errorf("store: conflict")
	ErrQueueFull = fmt.Errorf("store: writer queue full")
)
