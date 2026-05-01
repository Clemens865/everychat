package storage

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestOpenInMemoryRunsMigrations(t *testing.T) {
	t.Helper()
	db := openTestDB(t)

	expectedTables := []string{
		"tenants", "bots", "chats", "messages", "kb_chunks",
		"leads", "magic_links", "sessions", "audit_log",
	}
	for _, name := range expectedTables {
		if !hasTable(t, db, name) {
			t.Fatalf("expected table %q to exist after Migrate()", name)
		}
	}

	// _schema_migrations should record 001_init
	var v string
	err := db.QueryRow(`SELECT version FROM _schema_migrations WHERE version='001_init'`).Scan(&v)
	if err != nil {
		t.Fatalf("expected 001_init in _schema_migrations: %v", err)
	}
	if v != "001_init" {
		t.Fatalf("got %q, want 001_init", v)
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	db := openTestDB(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("second Migrate() call: %v", err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("third Migrate() call: %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT count(*) FROM _schema_migrations`).Scan(&n); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 row in _schema_migrations after repeated Migrate(), got %d", n)
	}
}

func TestInsertOneRowPerTable(t *testing.T) {
	db := openTestDB(t)

	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("exec %q: %v", query, err)
		}
	}

	exec(`INSERT INTO tenants (name, domain) VALUES (?, ?)`, "Acme", "acme.test")
	exec(`INSERT INTO bots (tenant_id, name) VALUES ((SELECT id FROM tenants), ?)`, "Helpdesk")
	exec(`INSERT INTO chats (bot_id, visitor_id) VALUES ((SELECT id FROM bots), ?)`, "visitor-1")
	exec(`INSERT INTO messages (chat_id, role, content) VALUES ((SELECT id FROM chats), ?, ?)`, "user", "hi")
	exec(`INSERT INTO kb_chunks (bot_id, source, chunk_index, content) VALUES ((SELECT id FROM bots), ?, ?, ?)`, "manual.md", 0, "chunk")
	exec(`INSERT INTO leads (chat_id, email) VALUES ((SELECT id FROM chats), ?)`, "lead@acme.test")
	exec(`INSERT INTO magic_links (email, token_hash, expires_at) VALUES (?, ?, datetime('now', '+15 minutes'))`, "owner@acme.test", "abc")
	exec(`INSERT INTO sessions (user_email, token_hash, expires_at) VALUES (?, ?, datetime('now', '+30 days'))`, "owner@acme.test", "def")
	exec(`INSERT INTO audit_log (actor, action, target_type) VALUES (?, ?, ?)`, "system", "boot", "process")
}

func TestOpenCreatesParentDirectory(t *testing.T) {
	tmp := t.TempDir()
	dsn := filepath.Join(tmp, "nested", "subdir", "everychat.db")
	db, err := Open(dsn)
	if err != nil {
		t.Fatalf("Open(%q): %v", dsn, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if !hasTable(t, db, "tenants") {
		t.Fatal("expected migrations to run on fresh on-disk DB")
	}
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func hasTable(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var got string
	err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&got)
	if err == sql.ErrNoRows {
		return false
	}
	if err != nil {
		t.Fatalf("check table %q: %v", name, err)
	}
	return got == name
}
