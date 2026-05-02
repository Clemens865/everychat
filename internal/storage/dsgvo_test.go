package storage

import (
	"context"
	"database/sql"
	"testing"
)

// seedVisitor creates one chat (with two messages) + one lead under
// visitor_id. Returns the chat id so callers can plant additional state.
func seedVisitor(t *testing.T, db *sql.DB, visitorID, email string) (botID, chatID int64) {
	t.Helper()
	bots, _ := ListBots(context.Background(), db)
	botID = bots[0].ID
	res, err := db.Exec(`INSERT INTO chats (bot_id, visitor_id, visitor_email) VALUES (?, ?, ?)`, botID, visitorID, email)
	if err != nil {
		t.Fatal(err)
	}
	chatID, _ = res.LastInsertId()
	if _, err := db.Exec(`INSERT INTO messages (chat_id, role, content) VALUES (?, 'user', ?)`, chatID, "Hallo"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO messages (chat_id, role, content) VALUES (?, 'assistant', ?)`, chatID, "Guten Tag"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO leads (chat_id, email, summary) VALUES (?, ?, ?)`, chatID, email, "wants meeting"); err != nil {
		t.Fatal(err)
	}
	return
}

func TestLoadVisitor_AggregatesChatsAndLeads(t *testing.T) {
	db := openTestDB(t)
	seedVisitor(t, db, "vis-1", "max@firma.de")
	seedVisitor(t, db, "vis-1", "max@firma.de") // second chat for same visitor

	rec, err := LoadVisitor(context.Background(), db, "vis-1")
	if err != nil {
		t.Fatalf("LoadVisitor: %v", err)
	}
	if len(rec.Chats) != 2 {
		t.Errorf("expected 2 chats, got %d", len(rec.Chats))
	}
	if len(rec.Leads) != 2 {
		t.Errorf("expected 2 leads, got %d", len(rec.Leads))
	}
	if rec.BotMessages != 4 {
		t.Errorf("expected 4 messages, got %d", rec.BotMessages)
	}
}

func TestEraseVisitor_CascadesAndAudits(t *testing.T) {
	db := openTestDB(t)
	seedVisitor(t, db, "vis-1", "max@firma.de")
	seedVisitor(t, db, "vis-2", "anna@firma.de") // unrelated visitor; must NOT be erased

	summary, err := EraseVisitor(context.Background(), db, "vis-1", "admin@everychat.local")
	if err != nil {
		t.Fatalf("EraseVisitor: %v", err)
	}
	if summary.ChatsDeleted != 1 || summary.MessagesDeleted != 2 || summary.LeadsDeleted != 1 {
		t.Errorf("counts: %+v", summary)
	}

	// vis-1 traces gone everywhere except audit_log.
	for _, q := range []string{
		`SELECT count(*) FROM chats WHERE visitor_id = 'vis-1'`,
		`SELECT count(*) FROM messages WHERE chat_id IN (SELECT id FROM chats WHERE visitor_id = 'vis-1')`,
		`SELECT count(*) FROM leads WHERE chat_id IN (SELECT id FROM chats WHERE visitor_id = 'vis-1')`,
	} {
		var n int
		if err := db.QueryRow(q).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		if n != 0 {
			t.Errorf("%s -> %d, want 0", q, n)
		}
	}
	// vis-2 untouched.
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM chats WHERE visitor_id = 'vis-2'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("vis-2 collateral damage: %d chats", n)
	}
	// audit_log row exists.
	if err := db.QueryRow(`SELECT count(*) FROM audit_log WHERE action = 'dsgvo.erase' AND target_id = 'vis-1'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("audit_log: %d rows for erasure, want 1", n)
	}
}

func TestEraseVisitor_IdempotentOnSecondCall(t *testing.T) {
	db := openTestDB(t)
	seedVisitor(t, db, "vis-1", "x@y.de")

	if _, err := EraseVisitor(context.Background(), db, "vis-1", "admin"); err != nil {
		t.Fatal(err)
	}
	summary, err := EraseVisitor(context.Background(), db, "vis-1", "admin")
	if err != nil {
		t.Fatalf("second erase: %v", err)
	}
	if summary.ChatsDeleted != 0 || summary.MessagesDeleted != 0 || summary.LeadsDeleted != 0 {
		t.Errorf("second call should report zero counts: %+v", summary)
	}
}

func TestSetDSGVODocURL_RoundTrip(t *testing.T) {
	db := openTestDB(t)
	bots, _ := ListBots(context.Background(), db)
	id := bots[0].ID

	if err := SetDSGVODocURL(context.Background(), db, id, "https://demo.de/dsgvo"); err != nil {
		t.Fatal(err)
	}
	got, err := LoadDSGVODocURL(context.Background(), db, id)
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://demo.de/dsgvo" {
		t.Errorf("got %q", got)
	}
	if err := SetDSGVODocURL(context.Background(), db, id, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := LoadDSGVODocURL(context.Background(), db, id); got != "" {
		t.Errorf("expected empty after clear, got %q", got)
	}
}

func TestSweepRetention_DeletesPastWindow(t *testing.T) {
	db := openTestDB(t)
	bots, _ := ListBots(context.Background(), db)
	id := bots[0].ID

	// Retention 1 day, then plant an old + a fresh chat.
	if _, err := db.Exec(`UPDATE bots SET retention_days = 1 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO chats (bot_id, visitor_id, started_at) VALUES (?, 'old-vis', datetime('now', '-3 days'))`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO chats (bot_id, visitor_id, started_at) VALUES (?, 'fresh-vis', datetime('now'))`, id); err != nil {
		t.Fatal(err)
	}

	stats, err := SweepRetention(context.Background(), db)
	if err != nil {
		t.Fatalf("SweepRetention: %v", err)
	}
	if stats.ChatsDeleted != 1 {
		t.Errorf("expected 1 chat deleted, got %d", stats.ChatsDeleted)
	}

	var n int
	if err := db.QueryRow(`SELECT count(*) FROM chats WHERE visitor_id = 'old-vis'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("old-vis chat not deleted")
	}
	if err := db.QueryRow(`SELECT count(*) FROM chats WHERE visitor_id = 'fresh-vis'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("fresh-vis chat unexpectedly deleted")
	}

	if err := db.QueryRow(`SELECT count(*) FROM _retention_runs WHERE finished_at IS NOT NULL`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("expected 1 finished retention_runs row, got %d", n)
	}
}
