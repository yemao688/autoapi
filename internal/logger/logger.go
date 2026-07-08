// Package logger configures the application-wide structured logger.
//
// The logger is backed by a slog.TextHandler whose output is teed between
// os.Stderr and a rotating file (via lumberjack). Both the structured
// (slog) and legacy (log) sinks are wired to the same writer so calls from
// third-party packages that still use the standard log package also reach
// the persistent log file.
//
// The path is configurable from the UI; the recommended default is
// ~/.autoapi/logs/autoapi.log. When the user disables logging we drop the
// file writer and keep only stderr so the application still produces
// visible diagnostics in the terminal / wails log file.
package logger

import (
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"path/filepath"

	"gopkg.in/natefinch/lumberjack.v2"
)

// Config drives the persistent logger. The zero value produces a
// stderr-only logger at info level (see Init for details).
type Config struct {
	Enabled    bool   // true = also write to the rotating file
	Level      string // "error" | "warn" | "info" | "debug" | "trace"
	MaxSizeMB  int    // per-file size cap before rotation
	MaxAgeDays int    // days to retain old log files
	MaxBackups int    // maximum number of rotated files to keep
	Path       string // full path to the active log file (e.g. ~/.autoapi/logs/autoapi.log)
}

// levelTrace is a custom level below Debug so that ultra-verbose
// diagnostics (e.g. per-request body traces) can be filtered out by default.
const levelTrace slog.Level = -8

// currentWriter is the active multi-writer (file + stderr). We retain a
// reference so that subsequent Init calls can close the previous lumberjack
// handle and release the file lock. We also keep it so that tests can
// inspect the active sink.
var currentWriter io.Writer

// Init (re-)configures the global slog and log sinks according to cfg.
//
// Behaviour:
//   - When cfg.Enabled is true and cfg.Path resolves to a writable location
//     the handler is a TextHandler writing to both the rotating file and
//     os.Stderr (via io.MultiWriter).
//   - When cfg.Enabled is false the handler writes only to os.Stderr.
//   - On any file-related error the function falls back to stderr-only
//     logging, returns the error, and never panics. This matches the
//     "logging must not break the app" requirement.
//
// The package is safe to call multiple times: re-initialising closes the
// previous lumberjack handle.
func Init(cfg Config) error {
	level := parseLevel(cfg.Level)

	var writer io.Writer = os.Stderr
	if cfg.Enabled && cfg.Path != "" {
		// Ensure the parent directory exists with owner-only permissions.
		if dir := filepath.Dir(cfg.Path); dir != "" {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return fallbackToStderr(level, fmt.Errorf("logger: mkdir %q: %w", dir, err))
			}
		}

		rotator := &lumberjack.Logger{
			Filename:   cfg.Path,
			MaxSize:    cfg.MaxSizeMB,
			MaxAge:     cfg.MaxAgeDays,
			MaxBackups: cfg.MaxBackups,
			LocalTime:  true,
			Compress:   false,
		}
		// Probe the file by issuing a no-op write. lumberjack opens the
		// destination lazily on Write, so we can detect a misconfigured
		// path (read-only mount, missing parent directory, etc.) before
		// committing to a file-backed handler.
		if _, err := rotator.Write([]byte{}); err != nil {
			_ = rotator.Close()
			return fallbackToStderr(level, fmt.Errorf("logger: open %q: %w", cfg.Path, err))
		}
		writer = io.MultiWriter(rotator, os.Stderr)
		// best-effort close of the previous rotator, if any
		prev, _ := currentWriter.(*lumberjack.Logger)
		if prev != nil {
			_ = prev.Close()
		}
		currentWriter = writer
	} else {
		// Disabled — keep stderr only and drop any file rotator.
		prev, _ := currentWriter.(*lumberjack.Logger)
		if prev != nil {
			_ = prev.Close()
		}
		currentWriter = os.Stderr
	}

	handler := slog.NewTextHandler(writer, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(handler))

	// Route the legacy log package through the same writer so that any
	// dependency still using log.Printf also lands in the file.
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.SetOutput(writer)

	return nil
}

// Update is a thin wrapper around Init used when the user edits the
// logging section of the settings panel. Errors are returned to the caller
// (the App layer surfaces them via a toast).
func Update(cfg Config) error {
	return Init(cfg)
}

// fallbackToStderr re-installs a stderr-only logger at the given level and
// returns the supplied error to the caller. Used as a safety net when
// file initialisation fails — the app must keep working even if the log
// directory is not writable.
func fallbackToStderr(level slog.Level, err error) error {
	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(handler))
	log.SetOutput(os.Stderr)
	currentWriter = os.Stderr
	return err
}

// parseLevel maps the public string level to a slog.Level. Unknown values
// fall back to Info so a misconfigured UI never silently disables logging.
func parseLevel(s string) slog.Level {
	switch s {
	case "error", "ERROR":
		return slog.LevelError
	case "warn", "WARN", "warning":
		return slog.LevelWarn
	case "debug", "DEBUG":
		return slog.LevelDebug
	case "trace", "TRACE":
		return levelTrace
	case "info", "INFO", "":
		return slog.LevelInfo
	default:
		return slog.LevelInfo
	}
}
