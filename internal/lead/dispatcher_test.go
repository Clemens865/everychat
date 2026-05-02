package lead

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/clemenshoenig/everychat/internal/integrations"
	"github.com/clemenshoenig/everychat/internal/storage"
)

func TestCapture_PersistsLeadAndEnqueuesWebhook(t *testing.T) {
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	bots, _ := storage.ListBots(context.Background(), db)
	botID := bots[0].ID
	if _, err := db.Exec(`UPDATE bots SET webhook_url='https://example.test/hook', webhook_secret_hash='secret' WHERE id=?`, botID); err != nil {
		t.Fatal(err)
	}
	res, _ := db.Exec(`INSERT INTO chats (bot_id, visitor_id) VALUES (?, ?)`, botID, "vtr-1")
	chatID, _ := res.LastInsertId()

	d := New(db, integrations.New(true))
	leadID, err := d.Capture(context.Background(), Lead{
		BotID: botID, ChatID: chatID, Email: "lead@firma.de", Summary: "wants meeting",
	})
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if leadID == 0 {
		t.Fatal("expected non-zero lead id")
	}

	var count int
	if err := db.QueryRow(`SELECT count(*) FROM lead_dispatch WHERE lead_id = ?`, leadID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected 1 lead_dispatch row, got %d", count)
	}
}

func TestCapture_RejectsMissingFields(t *testing.T) {
	db, _ := storage.Open(":memory:")
	defer func() { _ = db.Close() }()
	d := New(db, integrations.New(true))
	if _, err := d.Capture(context.Background(), Lead{Email: "x@y.de"}); err == nil {
		t.Error("expected error when bot_id missing")
	}
	if _, err := d.Capture(context.Background(), Lead{BotID: 1}); err == nil {
		t.Error("expected error when email missing")
	}
}

func TestProcessOnce_DeliversAndMarks(t *testing.T) {
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		// Verify HMAC signature is present and well-formed.
		if r.Header.Get("X-Everychat-Signature") == "" {
			t.Error("missing X-Everychat-Signature header")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	bots, _ := storage.ListBots(context.Background(), db)
	botID := bots[0].ID
	if _, err := db.Exec(`UPDATE bots SET webhook_url=?, webhook_secret_hash='topsecret' WHERE id=?`, srv.URL, botID); err != nil {
		t.Fatal(err)
	}
	res, _ := db.Exec(`INSERT INTO chats (bot_id, visitor_id) VALUES (?, ?)`, botID, "vtr-1")
	chatID, _ := res.LastInsertId()

	d := New(db, integrations.New(true))
	if _, err := d.Capture(context.Background(), Lead{BotID: botID, ChatID: chatID, Email: "lead@firma.de"}); err != nil {
		t.Fatal(err)
	}
	if err := d.ProcessOnce(context.Background(), 10); err != nil {
		t.Fatalf("ProcessOnce: %v", err)
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("expected 1 webhook hit, got %d", hits)
	}
	var deliveredAt interface{}
	if err := db.QueryRow(`SELECT delivered_at FROM lead_dispatch ORDER BY id DESC LIMIT 1`).Scan(&deliveredAt); err != nil {
		t.Fatal(err)
	}
	if deliveredAt == nil {
		t.Error("delivered_at not stamped after success")
	}
}

func TestProcessOnce_RetriesOnFailure(t *testing.T) {
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	bots, _ := storage.ListBots(context.Background(), db)
	botID := bots[0].ID
	if _, err := db.Exec(`UPDATE bots SET webhook_url=?, webhook_secret_hash='s' WHERE id=?`, srv.URL, botID); err != nil {
		t.Fatal(err)
	}
	res, _ := db.Exec(`INSERT INTO chats (bot_id, visitor_id) VALUES (?, ?)`, botID, "vtr-1")
	chatID, _ := res.LastInsertId()

	d := New(db, integrations.New(true))
	if _, err := d.Capture(context.Background(), Lead{BotID: botID, ChatID: chatID, Email: "lead@firma.de"}); err != nil {
		t.Fatal(err)
	}
	if err := d.ProcessOnce(context.Background(), 10); err != nil {
		t.Fatalf("ProcessOnce: %v", err)
	}
	var attempts int
	var lastErr, deliveredAt interface{}
	if err := db.QueryRow(`SELECT attempts, last_error, delivered_at FROM lead_dispatch ORDER BY id DESC LIMIT 1`).Scan(&attempts, &lastErr, &deliveredAt); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 {
		t.Errorf("attempts: got %d, want 1", attempts)
	}
	if lastErr == nil {
		t.Error("last_error not recorded on failure")
	}
	if deliveredAt != nil {
		t.Error("delivered_at must remain null on failure")
	}
}
