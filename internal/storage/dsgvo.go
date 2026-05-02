package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// VisitorRecord is the union view of every persisted row touching a
// single visitor_id within one tenant. Returned by LoadVisitor; used
// by the admin visitor-detail page and by the visitor-self-service
// export endpoint.
type VisitorRecord struct {
	VisitorID   string
	Chats       []VisitorChat
	Leads       []VisitorLead
	BotMessages int // total messages count across all chats
}

type VisitorChat struct {
	ChatID    int64
	BotID     int64
	BotName   string
	StartedAt time.Time
	EndedAt   sql.NullTime
	Lang      string
}

type VisitorLead struct {
	LeadID     int64
	ChatID     int64
	Email      string
	Summary    string
	CapturedAt time.Time
}

// LoadVisitor fetches every chat + lead for the given visitor_id.
// Tenant scoping happens at a higher layer (Phase 2 single-tenant; the
// query already returns only this binary's data).
func LoadVisitor(ctx context.Context, db *sql.DB, visitorID string) (VisitorRecord, error) {
	rec := VisitorRecord{VisitorID: visitorID}
	if strings.TrimSpace(visitorID) == "" {
		return rec, fmt.Errorf("LoadVisitor: visitor_id required")
	}

	// Chats
	chatRows, err := db.QueryContext(ctx, `
		SELECT chats.id, chats.bot_id, bots.name, chats.started_at, chats.ended_at, chats.lang
		FROM chats
		JOIN bots ON bots.id = chats.bot_id
		WHERE chats.visitor_id = ?
		ORDER BY chats.started_at DESC
	`, visitorID)
	if err != nil {
		return rec, fmt.Errorf("LoadVisitor chats: %w", err)
	}
	defer func() { _ = chatRows.Close() }()
	for chatRows.Next() {
		var c VisitorChat
		if err := chatRows.Scan(&c.ChatID, &c.BotID, &c.BotName, &c.StartedAt, &c.EndedAt, &c.Lang); err != nil {
			return rec, fmt.Errorf("LoadVisitor chat scan: %w", err)
		}
		rec.Chats = append(rec.Chats, c)
	}

	// Leads (joined via chats.visitor_id)
	leadRows, err := db.QueryContext(ctx, `
		SELECT leads.id, leads.chat_id, leads.email, leads.summary, leads.captured_at
		FROM leads
		JOIN chats ON chats.id = leads.chat_id
		WHERE chats.visitor_id = ?
		ORDER BY leads.captured_at DESC
	`, visitorID)
	if err != nil {
		return rec, fmt.Errorf("LoadVisitor leads: %w", err)
	}
	defer func() { _ = leadRows.Close() }()
	for leadRows.Next() {
		var l VisitorLead
		if err := leadRows.Scan(&l.LeadID, &l.ChatID, &l.Email, &l.Summary, &l.CapturedAt); err != nil {
			return rec, fmt.Errorf("LoadVisitor lead scan: %w", err)
		}
		rec.Leads = append(rec.Leads, l)
	}

	// Messages count
	if err := db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(msg_count), 0)
		FROM (
			SELECT (SELECT count(*) FROM messages WHERE chat_id = chats.id) AS msg_count
			FROM chats
			WHERE chats.visitor_id = ?
		)
	`, visitorID).Scan(&rec.BotMessages); err != nil {
		return rec, fmt.Errorf("LoadVisitor messages: %w", err)
	}

	return rec, nil
}

// ErasureSummary is what EraseVisitor returns: row counts per table.
// Surfaced in the admin UI's confirmation receipt + audit_log payload.
type ErasureSummary struct {
	VisitorID       string
	ChatsDeleted    int64
	MessagesDeleted int64
	LeadsDeleted    int64
	ErasedAt        time.Time
}

// EraseVisitor performs a DSGVO right-to-erasure cascade for one
// visitor_id. Order matters: messages → leads → chats so foreign-key
// cascades (kb_chunks_after_delete trigger from migration 003 covers
// kb_vec) and reference-counting both stay sane. The cascade is wrapped
// in a single transaction.
//
// An audit_log row is written inside the same transaction, recording
// the actor + the deleted-row counts. Per DSGVO Art. 30 we keep the
// audit_log row even after the rest of the visitor's data is gone —
// that's the legal basis for the erasure, not personal data of the
// visitor.
func EraseVisitor(ctx context.Context, db *sql.DB, visitorID, actor string) (ErasureSummary, error) {
	if strings.TrimSpace(visitorID) == "" {
		return ErasureSummary{}, fmt.Errorf("EraseVisitor: visitor_id required")
	}
	if strings.TrimSpace(actor) == "" {
		actor = "(unknown admin)"
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return ErasureSummary{}, fmt.Errorf("EraseVisitor: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	// Count first so the audit log can record what was wiped.
	summary := ErasureSummary{
		VisitorID: visitorID,
		ErasedAt:  time.Now().UTC(),
	}

	if err := tx.QueryRowContext(ctx,
		`SELECT count(*) FROM messages WHERE chat_id IN (SELECT id FROM chats WHERE visitor_id = ?)`,
		visitorID).Scan(&summary.MessagesDeleted); err != nil {
		return summary, fmt.Errorf("EraseVisitor count messages: %w", err)
	}
	if err := tx.QueryRowContext(ctx,
		`SELECT count(*) FROM leads WHERE chat_id IN (SELECT id FROM chats WHERE visitor_id = ?)`,
		visitorID).Scan(&summary.LeadsDeleted); err != nil {
		return summary, fmt.Errorf("EraseVisitor count leads: %w", err)
	}
	if err := tx.QueryRowContext(ctx,
		`SELECT count(*) FROM chats WHERE visitor_id = ?`,
		visitorID).Scan(&summary.ChatsDeleted); err != nil {
		return summary, fmt.Errorf("EraseVisitor count chats: %w", err)
	}

	// Cascade. chats has ON DELETE CASCADE refs from messages, leads,
	// and lead_dispatch (via leads). So deleting chats wipes the rest.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM chats WHERE visitor_id = ?`, visitorID); err != nil {
		return summary, fmt.Errorf("EraseVisitor delete chats: %w", err)
	}

	// Audit-log payload — keep it small but legally legible.
	payload, _ := json.Marshal(map[string]any{
		"visitor_id":       visitorID,
		"chats_deleted":    summary.ChatsDeleted,
		"messages_deleted": summary.MessagesDeleted,
		"leads_deleted":    summary.LeadsDeleted,
		"erased_at":        summary.ErasedAt.Format(time.RFC3339),
	})
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO audit_log (actor, action, target_type, target_id, payload_json)
		VALUES (?, 'dsgvo.erase', 'visitor', ?, ?)
	`, actor, visitorID, string(payload)); err != nil {
		return summary, fmt.Errorf("EraseVisitor audit: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return summary, fmt.Errorf("EraseVisitor commit: %w", err)
	}
	committed = true
	return summary, nil
}

// SetDSGVODocURL persists the bot's DSGVO documentation URL. Required
// (along with privacy + AGB URLs) for publish-gate clearance.
func SetDSGVODocURL(ctx context.Context, db *sql.DB, id int64, url string) error {
	_, err := db.ExecContext(ctx, `
		UPDATE bots SET dsgvo_doc_url = NULLIF(?, ''), updated_at = CURRENT_TIMESTAMP WHERE id = ?
	`, strings.TrimSpace(url), id)
	if err != nil {
		return fmt.Errorf("SetDSGVODocURL: %w", err)
	}
	return nil
}

// LoadDSGVODocURL returns the bot's DSGVO doc URL or "" if unset.
// Lighter than LoadBot for publish-gate checks.
func LoadDSGVODocURL(ctx context.Context, db *sql.DB, id int64) (string, error) {
	var url sql.NullString
	err := db.QueryRowContext(ctx, `SELECT dsgvo_doc_url FROM bots WHERE id = ?`, id).Scan(&url)
	if err != nil {
		return "", fmt.Errorf("LoadDSGVODocURL: %w", err)
	}
	if url.Valid {
		return url.String, nil
	}
	return "", nil
}

// SweepStats is what one retention run reports.
type SweepStats struct {
	BotsSwept       int
	ChatsDeleted    int64
	MessagesDeleted int64
	LeadsDeleted    int64
}

// SweepRetention deletes chats older than each bot's retention_days
// and writes a _retention_runs audit row. Atomic per bot — a single
// bot's delete-cascade runs in one transaction so a partial failure
// doesn't leave dangling orphans.
//
// Returns SweepStats summing across all bots.
func SweepRetention(ctx context.Context, db *sql.DB) (SweepStats, error) {
	var stats SweepStats

	// Begin the audit row up-front so failures still leave a trail.
	startRes, err := db.ExecContext(ctx,
		`INSERT INTO _retention_runs (started_at) VALUES (CURRENT_TIMESTAMP)`)
	if err != nil {
		return stats, fmt.Errorf("SweepRetention: open run: %w", err)
	}
	runID, _ := startRes.LastInsertId()
	finalize := func(runErr error) {
		errCol := sql.NullString{}
		if runErr != nil {
			errCol = sql.NullString{Valid: true, String: runErr.Error()}
		}
		_, _ = db.ExecContext(ctx, `
			UPDATE _retention_runs
			SET finished_at = CURRENT_TIMESTAMP,
			    bots_swept = ?, chats_deleted = ?,
			    messages_deleted = ?, leads_deleted = ?,
			    error = ?
			WHERE id = ?
		`, stats.BotsSwept, stats.ChatsDeleted, stats.MessagesDeleted,
			stats.LeadsDeleted, errCol, runID)
	}

	// Per-bot loop.
	rows, err := db.QueryContext(ctx, `SELECT id, retention_days FROM bots`)
	if err != nil {
		finalize(err)
		return stats, fmt.Errorf("SweepRetention: load bots: %w", err)
	}
	type sweepTarget struct {
		id            int64
		retentionDays int
	}
	var targets []sweepTarget
	for rows.Next() {
		var t sweepTarget
		if err := rows.Scan(&t.id, &t.retentionDays); err != nil {
			_ = rows.Close()
			finalize(err)
			return stats, fmt.Errorf("SweepRetention: scan: %w", err)
		}
		targets = append(targets, t)
	}
	_ = rows.Close()

	for _, t := range targets {
		stats.BotsSwept++
		// Count what we're about to nuke for stats + audit.
		var chatCount, msgCount, leadCount int64
		row := db.QueryRowContext(ctx, `
			SELECT
			  (SELECT count(*) FROM chats c WHERE c.bot_id = ? AND c.started_at < datetime('now', ?)),
			  (SELECT count(*) FROM messages m JOIN chats c ON c.id = m.chat_id
				WHERE c.bot_id = ? AND c.started_at < datetime('now', ?)),
			  (SELECT count(*) FROM leads l JOIN chats c ON c.id = l.chat_id
				WHERE c.bot_id = ? AND c.started_at < datetime('now', ?))
		`, t.id, fmt.Sprintf("-%d days", t.retentionDays),
			t.id, fmt.Sprintf("-%d days", t.retentionDays),
			t.id, fmt.Sprintf("-%d days", t.retentionDays))
		if err := row.Scan(&chatCount, &msgCount, &leadCount); err != nil {
			finalize(err)
			return stats, fmt.Errorf("SweepRetention: count for bot %d: %w", t.id, err)
		}
		// Delete; cascade handles messages+leads via FK ON DELETE CASCADE.
		if _, err := db.ExecContext(ctx, `
			DELETE FROM chats WHERE bot_id = ? AND started_at < datetime('now', ?)
		`, t.id, fmt.Sprintf("-%d days", t.retentionDays)); err != nil {
			finalize(err)
			return stats, fmt.Errorf("SweepRetention: delete chats for bot %d: %w", t.id, err)
		}
		stats.ChatsDeleted += chatCount
		stats.MessagesDeleted += msgCount
		stats.LeadsDeleted += leadCount
	}

	finalize(nil)
	return stats, nil
}
