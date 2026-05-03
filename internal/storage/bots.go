package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
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

	// Phase 3 — widget surface
	WidgetTemplate   string         // 'bubble' | 'inline'
	WidgetThemeJSON  string         // raw JSON; parsed by internal/widget
	EmbedTokenHash   sql.NullString // sha256 of the public bot-token
	EmbedOriginAllow string         // CSV of allowed origins; empty = dev-only
	WebhookURL       sql.NullString
	WebhookSecretSet bool // true if webhook_secret_hash IS NOT NULL

	// Phase 4 — corpus + judge
	Industry          sql.NullString // 'steuerberater' | 'handwerk' | …
	EvalMode          string         // 'keyword' | 'llm_judge'
	LastHoldoutScore  sql.NullFloat64
	EvalQuestionsPath sql.NullString // tests/eval/<bot>.yaml after Seed runs
}

// ErrBotNotFound is returned when a Bot lookup misses.
var ErrBotNotFound = errors.New("storage: bot not found")

// ListBots returns every bot ordered by id ascending. Phase 2 only ever
// has zero or one bot, but the dashboard renders for ≥1 anyway.
func ListBots(ctx context.Context, db *sql.DB) ([]Bot, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, tenant_id, name, system_prompt, draft_prompt, status,
		       privacy_policy_url, agb_url, retention_days, eval_threshold,
		       created_at, updated_at, published_at,
		       widget_template, widget_theme_json, embed_token_hash,
		       embed_origin_allow, webhook_url,
		       (webhook_secret_hash IS NOT NULL) AS webhook_secret_set,
		       industry, eval_mode, last_holdout_score, eval_questions_path
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
		       created_at, updated_at, published_at,
		       widget_template, widget_theme_json, embed_token_hash,
		       embed_origin_allow, webhook_url,
		       (webhook_secret_hash IS NOT NULL) AS webhook_secret_set,
		       industry, eval_mode, last_holdout_score, eval_questions_path
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

// CreateBot inserts a new bot row under the first tenant (Phase 2 is
// single-tenant) and returns the new id. The system_prompt and
// draft_prompt start empty; the wizard fills draft_prompt at step 3.
func CreateBot(ctx context.Context, db *sql.DB, name, domain string) (int64, error) {
	if name == "" {
		return 0, errors.New("CreateBot: name required")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("CreateBot: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	// Pick the first tenant; create one if the table is empty.
	var tenantID int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM tenants ORDER BY id LIMIT 1`).Scan(&tenantID)
	if errors.Is(err, sql.ErrNoRows) {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO tenants (name, domain) VALUES (?, ?)`,
			"Default", "default.local")
		if err != nil {
			return 0, fmt.Errorf("CreateBot: seed tenant: %w", err)
		}
		tenantID, _ = res.LastInsertId()
	} else if err != nil {
		return 0, fmt.Errorf("CreateBot: lookup tenant: %w", err)
	}

	res, err := tx.ExecContext(ctx, `
		INSERT INTO bots (tenant_id, name, system_prompt, draft_prompt, status, retention_days, eval_threshold)
		VALUES (?, ?, '', '', 'draft', 90, 0.85)
	`, tenantID, name)
	if err != nil {
		return 0, fmt.Errorf("CreateBot: insert: %w", err)
	}
	id, _ := res.LastInsertId()
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("CreateBot: commit: %w", err)
	}
	committed = true
	return id, nil
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
		&b.WidgetTemplate, &b.WidgetThemeJSON, &b.EmbedTokenHash,
		&b.EmbedOriginAllow, &b.WebhookURL, &b.WebhookSecretSet,
		&b.Industry, &b.EvalMode, &b.LastHoldoutScore, &b.EvalQuestionsPath,
	)
	if err != nil {
		return Bot{}, err
	}
	return b, nil
}

// SetIndustry persists a bot's industry tag. The taxonomy enum is
// enforced by the corpus loader at the call site; this helper accepts
// any string + persists it to keep the storage layer dumb.
func SetIndustry(ctx context.Context, db *sql.DB, id int64, industry string) error {
	_, err := db.ExecContext(ctx,
		`UPDATE bots SET industry = NULLIF(?, ''), updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		strings.TrimSpace(industry), id)
	if err != nil {
		return fmt.Errorf("SetIndustry: %w", err)
	}
	return nil
}

// SetEvalMode persists 'keyword' | 'llm_judge'. Whitelisted at storage
// boundary so a UI bug can't write a junk value that crashes the runner.
func SetEvalMode(ctx context.Context, db *sql.DB, id int64, mode string) error {
	switch mode {
	case "keyword", "llm_judge":
	default:
		return fmt.Errorf("SetEvalMode: unknown mode %q", mode)
	}
	_, err := db.ExecContext(ctx,
		`UPDATE bots SET eval_mode = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		mode, id)
	if err != nil {
		return fmt.Errorf("SetEvalMode: %w", err)
	}
	return nil
}

// SetLastHoldoutScore stores a 0.0–1.0 float; called by the hold-out
// evaluator. Surfaced on the dashboard.
func SetLastHoldoutScore(ctx context.Context, db *sql.DB, id int64, score float64) error {
	_, err := db.ExecContext(ctx,
		`UPDATE bots SET last_holdout_score = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		score, id)
	if err != nil {
		return fmt.Errorf("SetLastHoldoutScore: %w", err)
	}
	return nil
}

// SetEvalQuestionsPath records which YAML file holds the bot's golden
// questions. Set by corpus.Seed during the wizard.
func SetEvalQuestionsPath(ctx context.Context, db *sql.DB, id int64, path string) error {
	_, err := db.ExecContext(ctx,
		`UPDATE bots SET eval_questions_path = NULLIF(?, ''), updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		strings.TrimSpace(path), id)
	if err != nil {
		return fmt.Errorf("SetEvalQuestionsPath: %w", err)
	}
	return nil
}

// SetEmbedTokenHash stores sha256(token) for a bot. The cleartext token is
// returned to the caller (admin UI) once and never persisted.
func SetEmbedTokenHash(ctx context.Context, db *sql.DB, id int64, tokenHash string) error {
	_, err := db.ExecContext(ctx,
		`UPDATE bots SET embed_token_hash = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		tokenHash, id)
	if err != nil {
		return fmt.Errorf("SetEmbedTokenHash: %w", err)
	}
	return nil
}

// LookupBotByEmbedTokenHash resolves a bot from sha256(token). Used by the
// visitor-facing /api/v1/* endpoints.
func LookupBotByEmbedTokenHash(ctx context.Context, db *sql.DB, tokenHash string) (Bot, error) {
	row := db.QueryRowContext(ctx, `
		SELECT id, tenant_id, name, system_prompt, draft_prompt, status,
		       privacy_policy_url, agb_url, retention_days, eval_threshold,
		       created_at, updated_at, published_at,
		       widget_template, widget_theme_json, embed_token_hash,
		       embed_origin_allow, webhook_url,
		       (webhook_secret_hash IS NOT NULL) AS webhook_secret_set,
		       industry, eval_mode, last_holdout_score, eval_questions_path
		FROM bots WHERE embed_token_hash = ?
	`, tokenHash)
	b, err := scanBot(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Bot{}, ErrBotNotFound
	}
	return b, err
}

// SetWidgetTemplate updates which template variant the bot uses
// ("bubble" | "inline"). Other values are rejected at this layer so a
// downstream template-load can't spawn a 500.
func SetWidgetTemplate(ctx context.Context, db *sql.DB, id int64, tmpl string) error {
	switch tmpl {
	case "bubble", "inline":
	default:
		return fmt.Errorf("SetWidgetTemplate: unknown template %q", tmpl)
	}
	_, err := db.ExecContext(ctx,
		`UPDATE bots SET widget_template = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		tmpl, id)
	if err != nil {
		return fmt.Errorf("SetWidgetTemplate: %w", err)
	}
	return nil
}

// UpdateWidgetTheme stores the bot's theme JSON. Validation happens at
// the widget layer (Theme.CSSVars sanitizes); we trust the caller to
// hand us valid JSON.
func UpdateWidgetTheme(ctx context.Context, db *sql.DB, id int64, themeJSON string) error {
	_, err := db.ExecContext(ctx,
		`UPDATE bots SET widget_theme_json = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		themeJSON, id)
	if err != nil {
		return fmt.Errorf("UpdateWidgetTheme: %w", err)
	}
	return nil
}

// SetEmbedOriginAllow updates the CSV of allowed origins for a bot's widget.
func SetEmbedOriginAllow(ctx context.Context, db *sql.DB, id int64, csv string) error {
	_, err := db.ExecContext(ctx,
		`UPDATE bots SET embed_origin_allow = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		csv, id)
	if err != nil {
		return fmt.Errorf("SetEmbedOriginAllow: %w", err)
	}
	return nil
}
