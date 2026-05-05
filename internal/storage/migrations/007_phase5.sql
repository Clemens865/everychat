-- 007_phase5.sql — Phase 5 (Multi-Chat Dashboard) schema additions.
--
-- Two new tables for the bot-to-bot adversarial tester:
--   adversary_runs   — one row per `everychat adversary` invocation,
--                      tracks turn cap, cost cap, accumulated cost,
--                      terminal status + verdict.
--   adversary_turns  — one row per turn (tester or victim), preserved
--                      verbatim so the admin can scroll the transcript.
--
-- Status enum lives at the application layer (internal/adversary):
--   'running'         — run kicked off, no terminal state yet
--   'completed'       — finished within turn + cost cap
--   'cost_exhausted'  — halted by the CostGate before finishing
--   'error'           — LLM/infra failure; transcript still inspectable
--
-- Both tables cascade on bot delete so DSGVO erasure (Phase 3) keeps
-- working without a bespoke cleanup pass.

CREATE TABLE IF NOT EXISTS adversary_runs (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    bot_id            INTEGER NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
    persona           TEXT NOT NULL,
    turn_cap          INTEGER NOT NULL,
    max_cost_cents    INTEGER NOT NULL,
    total_cost_cents  INTEGER NOT NULL DEFAULT 0,
    status            TEXT NOT NULL DEFAULT 'running',
    verdict           TEXT,
    started_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    finished_at       TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_adversary_runs_bot     ON adversary_runs(bot_id);
CREATE INDEX IF NOT EXISTS idx_adversary_runs_started ON adversary_runs(started_at);

CREATE TABLE IF NOT EXISTS adversary_turns (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id         INTEGER NOT NULL REFERENCES adversary_runs(id) ON DELETE CASCADE,
    turn_index     INTEGER NOT NULL,
    role           TEXT NOT NULL,
    content        TEXT NOT NULL,
    input_tokens   INTEGER NOT NULL DEFAULT 0,
    output_tokens  INTEGER NOT NULL DEFAULT 0,
    created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(run_id, turn_index, role)
);

CREATE INDEX IF NOT EXISTS idx_adversary_turns_run ON adversary_turns(run_id);
