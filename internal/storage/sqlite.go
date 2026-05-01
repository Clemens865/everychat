// Package storage owns the SQLite connection lifecycle, schema migrations,
// and any shared persistence helpers used by the rest of the everychat
// binary. Phase 1 covers schema bootstrap only — query builders for each
// table arrive in subsequent sprints/phases.
package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "github.com/mattn/go-sqlite3" // CGo driver, registered as "sqlite3"
)

// DefaultDSN is used when EVERYCHAT_DB_PATH is unset. The file is created
// (along with its parent directory) on first open.
const DefaultDSN = "./data/everychat.db"

// Open opens (or creates) the SQLite database at dsn, ensures sane PRAGMAs,
// and runs any pending embedded migrations. The returned *sql.DB is safe for
// concurrent use; callers own its lifecycle (Close).
//
// dsn is treated as a filesystem path unless it already contains DSN-style
// query parameters (e.g. ":memory:" or "file::memory:?cache=shared").
func Open(dsn string) (*sql.DB, error) {
	if dsn == "" {
		return nil, errors.New("storage.Open: empty dsn")
	}

	if needsParentDir(dsn) {
		if err := os.MkdirAll(filepath.Dir(dsn), 0o750); err != nil {
			return nil, fmt.Errorf("storage.Open: create data dir: %w", err)
		}
	}

	db, err := sql.Open("sqlite3", dsn+pragmaSuffix(dsn))
	if err != nil {
		return nil, fmt.Errorf("storage.Open: %w", err)
	}

	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("storage.Open: ping: %w", err)
	}

	// SQLite has a single writer; one connection avoids "database is locked"
	// noise during the small ops/admin workload Phase 1 targets.
	db.SetMaxOpenConns(1)

	if err := Migrate(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("storage.Open: migrate: %w", err)
	}
	return db, nil
}

// needsParentDir reports whether the dsn refers to a file path whose
// containing directory we should create. ":memory:" and "file:..." DSNs
// don't need it.
func needsParentDir(dsn string) bool {
	if dsn == ":memory:" {
		return false
	}
	if len(dsn) >= 5 && dsn[:5] == "file:" {
		return false
	}
	return true
}

// pragmaSuffix appends the PRAGMA query-string segment we always want.
// We avoid clobbering an existing query string the caller provided.
func pragmaSuffix(dsn string) string {
	pragma := "?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000"
	for _, c := range dsn {
		if c == '?' {
			// caller already supplied params — append with & instead
			return "&_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000"
		}
	}
	return pragma
}
