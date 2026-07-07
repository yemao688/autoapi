package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"autoapi/internal/model"

	_ "modernc.org/sqlite"
)

// StoreDeps configures the store. For production use, leave DSN empty to
// resolve via the default home directory path ($HOME/.autoapi/autoapi.db).
// For tests, set DSN to ":memory:" or a temp path.
type StoreDeps struct {
	DSN string // empty = default home directory; overridable for tests
}

// Store is the SQLite-backed persistence layer implementing api.StoreService.
// All write operations go through the single-writer goroutine (Writer); reads
// go directly against the shared *sql.DB (safe under WAL mode).
type Store struct {
	db     *sql.DB
	writer *Writer
}

// New opens (or creates) the SQLite database, applies migrations, seeds dev
// fixtures (in !production builds), and starts the write-coordination goroutine.
func New(_ context.Context, deps StoreDeps) (*Store, error) {
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

	s := &Store{db: db}

	// Migrations
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: migrate: %w", err)
	}

	// Writer
	s.writer = NewWriter(db, 1024)
	go s.writer.Run()

	// Dev-only seeding (no-op in production builds)
	initDev(s)

	return s, nil
}

// initDev is replaced by a real implementation in !production builds.
// See fixtures.go for the dev implementation.
var initDev = func(*Store) {}

// Close shuts down the writer and closes the database connection.
func (s *Store) Close() error {
	s.writer.Close()
	return s.db.Close()
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
func makeID() string { return newUUID() }

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...interface{}) error
}

// ---------------------------------------------------------------------------
//  Sentinel errors
// ---------------------------------------------------------------------------

var (
	ErrNotFound  = fmt.Errorf("store: not found")
	ErrQueueFull = fmt.Errorf("store: writer queue full")
)

// ---------------------------------------------------------------------------
//  Convenience: pricing for cost estimation
// ---------------------------------------------------------------------------

// modelCost holds per-token pricing for cost aggregation.
type modelCost struct {
	InputPerToken  float64
	OutputPerToken float64
}

// costTable maps model name → estimated cost per token in USD.
// Extended as new models appear.
var costTable = map[string]modelCost{
	"gpt-4o":            {InputPerToken: 10.0 / 1e6, OutputPerToken: 30.0 / 1e6},
	"gpt-4o-mini":       {InputPerToken: 0.15 / 1e6, OutputPerToken: 0.60 / 1e6},
	"gpt-4":             {InputPerToken: 30.0 / 1e6, OutputPerToken: 60.0 / 1e6},
	"gpt-4-turbo":       {InputPerToken: 10.0 / 1e6, OutputPerToken: 30.0 / 1e6},
	"gpt-3.5-turbo":     {InputPerToken: 0.50 / 1e6, OutputPerToken: 1.50 / 1e6},
	"claude-3.5-sonnet": {InputPerToken: 3.0 / 1e6, OutputPerToken: 15.0 / 1e6},
	"claude-3-opus":     {InputPerToken: 15.0 / 1e6, OutputPerToken: 75.0 / 1e6},
	"claude-3-haiku":    {InputPerToken: 0.25 / 1e6, OutputPerToken: 1.25 / 1e6},
	"deepseek-chat":     {InputPerToken: 0.27 / 1e6, OutputPerToken: 1.10 / 1e6},
	"deepseek-reasoner":  {InputPerToken: 0.55 / 1e6, OutputPerToken: 2.19 / 1e6},
	"moonshot-v1":       {InputPerToken: 0.12 / 1e6, OutputPerToken: 0.12 / 1e6},
	"glm-4":             {InputPerToken: 0.10 / 1e6, OutputPerToken: 0.10 / 1e6},
}

func estimateCost(modelName string, inputTokens, outputTokens int64) float64 {
	// Default fallback pricing (rough average)
	const defaultInput = 2.0 / 1e6
	const defaultOutput = 8.0 / 1e6

	c, ok := costTable[modelName]
	if !ok {
		c = modelCost{InputPerToken: defaultInput, OutputPerToken: defaultOutput}
	}
	return float64(inputTokens)*c.InputPerToken + float64(outputTokens)*c.OutputPerToken
}

// ---------------------------------------------------------------------------
//  Ensure model.ProviderStatus constants are used
// ---------------------------------------------------------------------------

var _ model.ProviderStatus
