-- 004_phase3.sql — Phase 3 (Embed + Lead Capture + DSGVO) schema additions.
--
-- Visitor-facing surface: widgets are served from /widget/{token}/shell
-- and authenticate to /api/v1/* via the public bot token. Origin allow-list
-- enforces which sites may embed the widget. Webhook columns hold the
-- owner-configured lead delivery target (HubSpot adapter is Phase 5 — its
-- column lands then).
--
-- Idempotent: each ALTER is wrapped so re-running doesn't fail mid-way.
-- SQLite doesn't support ADD COLUMN IF NOT EXISTS, so we rely on the
-- migrations table to gate re-application.

ALTER TABLE bots ADD COLUMN widget_template TEXT NOT NULL DEFAULT 'bubble';
ALTER TABLE bots ADD COLUMN widget_theme_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE bots ADD COLUMN embed_token_hash TEXT;
ALTER TABLE bots ADD COLUMN embed_origin_allow TEXT NOT NULL DEFAULT '';
ALTER TABLE bots ADD COLUMN webhook_url TEXT;
ALTER TABLE bots ADD COLUMN webhook_secret_hash TEXT;

CREATE INDEX IF NOT EXISTS idx_bots_embed_token_hash ON bots(embed_token_hash);

-- lead_dispatch — outbound delivery retry queue.
-- target is an integration-agnostic enum so the Phase 5 HubSpot adapter
-- plugs in by adding a new value, not by altering the schema.
CREATE TABLE IF NOT EXISTS lead_dispatch (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    lead_id       INTEGER NOT NULL REFERENCES leads(id) ON DELETE CASCADE,
    target        TEXT NOT NULL,                  -- 'webhook' (Phase 3); 'hubspot' lands in Phase 5
    attempts      INTEGER NOT NULL DEFAULT 0,
    last_error    TEXT,
    scheduled_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    delivered_at  TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_lead_dispatch_pending
    ON lead_dispatch(scheduled_at) WHERE delivered_at IS NULL;
