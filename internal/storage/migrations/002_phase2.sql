-- 002_phase2.sql — Phase 2 (Bot Authoring Core) schema additions.
--
-- Adds:
--   * bots.eval_threshold     — fraction of golden questions a bot must pass
--                               before publish-gate (default 0.85)
--   * eval_runs               — per-bot eval history
-- Seeds:
--   * one demo tenant + bot named "steuerkanzlei-demo" used by the
--     validation gate from Sprint 3 onward (sprints 3-5 cannot run evals
--     without a bot to score).

-- ── bots: add eval_threshold ──────────────────────────────────────────────
ALTER TABLE bots ADD COLUMN eval_threshold REAL NOT NULL DEFAULT 0.85;

-- ── eval_runs: per-run history ────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS eval_runs (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    bot_id          INTEGER NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
    questions_file  TEXT NOT NULL,
    total           INTEGER NOT NULL,
    passed          INTEGER NOT NULL,
    score           REAL NOT NULL,
    started_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    finished_at     TIMESTAMP,
    report_json     TEXT NOT NULL DEFAULT '{}'
);

CREATE INDEX IF NOT EXISTS idx_eval_runs_bot ON eval_runs(bot_id);
CREATE INDEX IF NOT EXISTS idx_eval_runs_finished ON eval_runs(finished_at);

-- ── seed: demo tenant + bot ───────────────────────────────────────────────
-- Idempotent: ON CONFLICT DO NOTHING relies on the unique domain index
-- already declared on tenants. Phase 2 only ever uses this one bot for
-- the validation gate; the wizard in Sprint 6 creates additional bots
-- via the admin UI.
INSERT INTO tenants (name, domain, status)
VALUES ('Demo Steuerkanzlei', 'steuerkanzlei.demo', 'active')
ON CONFLICT(domain) DO NOTHING;

INSERT INTO bots (
    tenant_id, name, system_prompt, draft_prompt, status,
    privacy_policy_url, agb_url, retention_days, eval_threshold
)
SELECT
    (SELECT id FROM tenants WHERE domain = 'steuerkanzlei.demo'),
    'steuerkanzlei-demo',
    'Du bist der digitale Assistent einer fiktiven deutschen Steuerkanzlei. ' ||
    'Antworte präzise und freundlich auf Deutsch. Beantworte ausschließlich Fragen ' ||
    'zu Steuerberatung, Lohnbuchhaltung und Jahresabschlüssen. Lehne Rechtsberatung ' ||
    'höflich ab und verweise an einen Anwalt.',
    '',
    'draft',
    NULL,
    NULL,
    90,
    0.85
WHERE NOT EXISTS (
    SELECT 1 FROM bots WHERE name = 'steuerkanzlei-demo'
);
