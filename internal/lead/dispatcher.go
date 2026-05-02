package lead

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/clemenshoenig/everychat/internal/integrations"
	"github.com/clemenshoenig/everychat/internal/storage"
)

// Lead is the captured contact-handoff record handed to Capture.
type Lead struct {
	BotID     int64
	ChatID    int64
	VisitorID string
	Email     string
	Name      string
	Summary   string
}

// Dispatcher orchestrates lead capture: persists the row, enqueues
// outbound delivery, and processes the queue with retry/backoff.
type Dispatcher struct {
	db      *sql.DB
	webhook *integrations.WebhookClient
	now     func() time.Time

	// MaxAttempts caps retries. After this many failed dispatches the
	// row stays in lead_dispatch with a non-null last_error for the
	// admin UI to surface.
	MaxAttempts int
}

// New constructs a Dispatcher.
func New(db *sql.DB, webhook *integrations.WebhookClient) *Dispatcher {
	return &Dispatcher{
		db:          db,
		webhook:     webhook,
		now:         time.Now,
		MaxAttempts: 5,
	}
}

// SetClock overrides time.Now for tests.
func (d *Dispatcher) SetClock(now func() time.Time) { d.now = now }

// Capture persists a Lead and enqueues a webhook dispatch if the bot
// has webhook_url configured. Synchronous — the visitor sees the
// artifact's success state once this returns.
func (d *Dispatcher) Capture(ctx context.Context, l Lead) (int64, error) {
	if l.Email == "" || l.BotID == 0 {
		return 0, errors.New("Capture: bot_id and email required")
	}

	// leads.chat_id is NOT NULL with a FK to chats — when the widget
	// hasn't sent a chat_id yet (lead artifact submitted on first
	// turn), synthesize a chats row so the FK resolves. The visitor_id
	// is preserved for the DSGVO erasure cascade in Sprint 6.
	chatID := l.ChatID
	if chatID == 0 {
		visitor := l.VisitorID
		if visitor == "" {
			visitor = "lead-only"
		}
		res, err := d.db.ExecContext(ctx,
			`INSERT INTO chats (bot_id, visitor_id) VALUES (?, ?)`,
			l.BotID, visitor)
		if err != nil {
			return 0, fmt.Errorf("Capture: synth chat: %w", err)
		}
		chatID, _ = res.LastInsertId()
	}

	res, err := d.db.ExecContext(ctx, `
		INSERT INTO leads (chat_id, email, summary)
		VALUES (?, ?, ?)
	`, chatID, l.Email, l.Summary)
	if err != nil {
		return 0, fmt.Errorf("Capture: insert lead: %w", err)
	}
	leadID, _ := res.LastInsertId()

	bot, err := storage.LoadBot(ctx, d.db, l.BotID)
	if err != nil {
		// Lead is persisted; we just can't enqueue dispatch. Surface
		// the error so the caller can decide.
		return leadID, fmt.Errorf("Capture: load bot: %w", err)
	}

	if bot.WebhookURL.Valid && bot.WebhookURL.String != "" {
		if _, err := d.db.ExecContext(ctx, `
			INSERT INTO lead_dispatch (lead_id, target, scheduled_at)
			VALUES (?, 'webhook', ?)
		`, leadID, d.now().UTC()); err != nil {
			return leadID, fmt.Errorf("Capture: enqueue webhook: %w", err)
		}
	}
	return leadID, nil
}

// ProcessOnce drains the lead_dispatch queue once: picks up to `batch`
// pending rows and delivers each. Designed for periodic invocation
// (e.g. every 30s from a long-running goroutine, or in tests synchronously).
func (d *Dispatcher) ProcessOnce(ctx context.Context, batch int) error {
	rows, err := d.db.QueryContext(ctx, `
		SELECT lead_dispatch.id, lead_dispatch.lead_id, lead_dispatch.attempts,
		       leads.email, leads.summary, leads.chat_id,
		       chats.bot_id,
		       bots.webhook_url, bots.webhook_secret_hash
		FROM lead_dispatch
		JOIN leads ON leads.id = lead_dispatch.lead_id
		JOIN chats ON chats.id = leads.chat_id
		JOIN bots  ON bots.id  = chats.bot_id
		WHERE lead_dispatch.delivered_at IS NULL
		  AND lead_dispatch.attempts < ?
		ORDER BY lead_dispatch.id
		LIMIT ?
	`, d.MaxAttempts, batch)
	if err != nil {
		return fmt.Errorf("ProcessOnce: query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	type job struct {
		dispatchID, leadID, botID, chatID int64
		attempts                          int
		email, summary, webhookURL        string
		webhookSecretHash                 sql.NullString
	}
	var jobs []job
	for rows.Next() {
		var j job
		var webhookURL sql.NullString
		if err := rows.Scan(&j.dispatchID, &j.leadID, &j.attempts,
			&j.email, &j.summary, &j.chatID, &j.botID,
			&webhookURL, &j.webhookSecretHash); err != nil {
			return fmt.Errorf("ProcessOnce: scan: %w", err)
		}
		j.webhookURL = webhookURL.String
		jobs = append(jobs, j)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_ = rows.Close() // Free the connection before re-using db for retries.

	for _, j := range jobs {
		if j.webhookURL == "" {
			d.markDelivered(ctx, j.dispatchID) // nothing to deliver
			continue
		}
		// Phase 3: webhook secret = sha256 of bot's webhook_secret. We
		// store the hash, not the cleartext, so we can't sign retroactively.
		// For now, sign with the SHA256 hex itself as the HMAC key — the
		// admin UI surfaces the same hash for the receiver to configure.
		secret := ""
		if j.webhookSecretHash.Valid {
			secret = j.webhookSecretHash.String
		}
		body, _ := json.Marshal(map[string]any{
			"event":      "lead.created",
			"lead_id":    j.leadID,
			"bot_id":     j.botID,
			"chat_id":    j.chatID,
			"email":      j.email,
			"summary":    j.summary,
			"created_at": d.now().UTC().Format(time.RFC3339),
		})
		_, err := d.webhook.Deliver(ctx, j.webhookURL, secret, body)
		if err != nil {
			d.markFailed(ctx, j.dispatchID, j.attempts+1, err.Error())
			continue
		}
		d.markDelivered(ctx, j.dispatchID)
	}
	return nil
}

func (d *Dispatcher) markDelivered(ctx context.Context, id int64) {
	if _, err := d.db.ExecContext(ctx,
		`UPDATE lead_dispatch SET delivered_at = ?, attempts = attempts + 1 WHERE id = ?`,
		d.now().UTC(), id); err != nil {
		log.Printf("lead.markDelivered: %v", err)
	}
}

func (d *Dispatcher) markFailed(ctx context.Context, id int64, attempts int, msg string) {
	if _, err := d.db.ExecContext(ctx,
		`UPDATE lead_dispatch SET attempts = ?, last_error = ?, scheduled_at = ? WHERE id = ?`,
		attempts, msg, d.now().Add(backoff(attempts)).UTC(), id); err != nil {
		log.Printf("lead.markFailed: %v", err)
	}
}

// backoff is exponential with a 60s cap. Attempt 1 → 5s, 2 → 10s,
// 3 → 20s, 4 → 40s, 5 → 60s.
func backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 6 {
		// Beyond 6 the cap dominates anyway; this guard also avoids
		// gosec G115 noise around the int → uint shift conversion.
		return 60 * time.Second
	}
	secs := 5 << (attempt - 1)
	if secs > 60 {
		secs = 60
	}
	return time.Duration(secs) * time.Second
}
