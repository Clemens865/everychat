package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Bot is the typed view of a row in the `bots` table that the admin UI
// and Phase 2 sprints need. Phase 1 ops queries continue to use raw SQL.
type Bot struct {
	ID               int64
	TenantID         int64
	Name             string
	SystemPrompt     string
	DraftPrompt      string
	Status           string // draft | published | archived
	PrivacyPolicyURL sql.NullString
	AGBURL           sql.NullString
	RetentionDays    int
	EvalThreshold    float64
	CreatedAt        time.Time
	UpdatedAt        time.Time
	PublishedAt      sql.NullTime
}

// ErrBotNotFound is returned when a Bot lookup misses.
var ErrBotNotFound = errors.New("storage: bot not found")

// ListBots returns every bot ordered by id ascending. Phase 2 only ever
// has zero or one bot, but the dashboard renders for ≥1 anyway.
func ListBots(ctx context.Context, db *sql.DB) ([]Bot, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, tenant_id, name, system_prompt, draft_prompt, status,
		       privacy_policy_url, agb_url, retention_days, eval_threshold,
		       created_at, updated_at, published_at
		FROM bots
		ORDER BY id ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("ListBots: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Bot
	for rows.Next() {
		b, err := scanBot(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ListBots iterate: %w", err)
	}
	return out, nil
}

// LoadBot fetches a single Bot by id. Returns ErrBotNotFound if no row.
func LoadBot(ctx context.Context, db *sql.DB, id int64) (Bot, error) {
	row := db.QueryRowContext(ctx, `
		SELECT id, tenant_id, name, system_prompt, draft_prompt, status,
		       privacy_policy_url, agb_url, retention_days, eval_threshold,
		       created_at, updated_at, published_at
		FROM bots WHERE id = ?
	`, id)
	b, err := scanBot(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Bot{}, ErrBotNotFound
	}
	return b, err
}

// UpdateDraftPrompt replaces draft_prompt and bumps updated_at. Idempotent.
func UpdateDraftPrompt(ctx context.Context, db *sql.DB, id int64, draft string) error {
	res, err := db.ExecContext(ctx, `
		UPDATE bots SET draft_prompt = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?
	`, draft, id)
	if err != nil {
		return fmt.Errorf("UpdateDraftPrompt: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrBotNotFound
	}
	return nil
}

// PromoteDraftToPublished copies draft_prompt → system_prompt, sets status
// to "published", and stamps published_at. Returns ErrBotNotFound if id
// doesn't exist. Phase 2 publishes are unconditional at this layer; the
// gate logic lives one level up in the web handler (Sprint 6).
func PromoteDraftToPublished(ctx context.Context, db *sql.DB, id int64) error {
	res, err := db.ExecContext(ctx, `
		UPDATE bots
		SET system_prompt = draft_prompt,
		    status = 'published',
		    published_at = CURRENT_TIMESTAMP,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, id)
	if err != nil {
		return fmt.Errorf("PromoteDraftToPublished: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrBotNotFound
	}
	return nil
}

// SetCompliance updates the privacy / AGB URLs. Used by the settings rail.
func SetCompliance(ctx context.Context, db *sql.DB, id int64, privacyURL, agbURL string) error {
	_, err := db.ExecContext(ctx, `
		UPDATE bots
		SET privacy_policy_url = NULLIF(?, ''),
		    agb_url            = NULLIF(?, ''),
		    updated_at         = CURRENT_TIMESTAMP
		WHERE id = ?
	`, privacyURL, agbURL, id)
	if err != nil {
		return fmt.Errorf("SetCompliance: %w", err)
	}
	return nil
}

// scanBot is shared by ListBots / LoadBot. Both *sql.Row and *sql.Rows
// satisfy this minimal Scanner interface.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanBot(s rowScanner) (Bot, error) {
	var b Bot
	err := s.Scan(
		&b.ID, &b.TenantID, &b.Name, &b.SystemPrompt, &b.DraftPrompt, &b.Status,
		&b.PrivacyPolicyURL, &b.AGBURL, &b.RetentionDays, &b.EvalThreshold,
		&b.CreatedAt, &b.UpdatedAt, &b.PublishedAt,
	)
	if err != nil {
		return Bot{}, err
	}
	return b, nil
}
