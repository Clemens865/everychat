-- 005_phase3_dsgvo.sql — Phase 3 Sprint 6: DSGVO baseline.
--
-- Three concerns:
--   1. visitor_email on chats so admin "show me all of this person's
--      data" works without joining through leads (lead-only visitors
--      don't have chats; chat-only visitors don't have leads)
--   2. bots.dsgvo_doc_url — required by the third publish-gate door
--      (visitor-facing widget links to it)
--   3. _retention_runs — audit trail for the daily sweeper. DSGVO Art.
--      30 requires a record of processing activities; the sweeper IS
--      a processing activity, so its runs are evidence material.

ALTER TABLE chats ADD COLUMN visitor_email TEXT;
ALTER TABLE bots  ADD COLUMN dsgvo_doc_url TEXT;

CREATE INDEX IF NOT EXISTS idx_chats_visitor_email ON chats(visitor_email);

-- _retention_runs is the auditor's view into the sweeper. Each run logs
-- counts of cascaded rows by table; the audit_log row created alongside
-- carries the actor + timestamp so the two together satisfy the Art. 30
-- "evidence of erasure" requirement.
CREATE TABLE IF NOT EXISTS _retention_runs (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    started_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    finished_at     TIMESTAMP,
    bots_swept      INTEGER NOT NULL DEFAULT 0,
    chats_deleted   INTEGER NOT NULL DEFAULT 0,
    messages_deleted INTEGER NOT NULL DEFAULT 0,
    leads_deleted   INTEGER NOT NULL DEFAULT 0,
    error           TEXT
);
