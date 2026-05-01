-- 001_init.sql
-- Initial Everychat schema. See PRD Sprint 2 / blueprint for context.
-- Phase 1: vector embeddings (sqlite-vec) reserved as BLOB columns; not populated.
-- DSGVO: erasure cascade rules added in Phase 3 (TODOs flagged below).

PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS tenants (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT NOT NULL,
    domain      TEXT NOT NULL UNIQUE,
    status      TEXT NOT NULL DEFAULT 'active', -- active | suspended | deleted
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS bots (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    tenant_id          INTEGER NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name               TEXT NOT NULL,
    system_prompt      TEXT NOT NULL DEFAULT '',
    draft_prompt       TEXT NOT NULL DEFAULT '',
    status             TEXT NOT NULL DEFAULT 'draft', -- draft | published | archived
    privacy_policy_url TEXT,
    agb_url            TEXT,
    retention_days     INTEGER NOT NULL DEFAULT 90,
    created_at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    published_at       TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_bots_tenant ON bots(tenant_id);

CREATE TABLE IF NOT EXISTS chats (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    bot_id      INTEGER NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
    visitor_id  TEXT NOT NULL,
    started_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    ended_at    TIMESTAMP,
    lang        TEXT NOT NULL DEFAULT 'de'
);

CREATE INDEX IF NOT EXISTS idx_chats_bot ON chats(bot_id);
CREATE INDEX IF NOT EXISTS idx_chats_visitor ON chats(visitor_id);

CREATE TABLE IF NOT EXISTS messages (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    chat_id     INTEGER NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
    role        TEXT NOT NULL CHECK (role IN ('user','assistant','system','tool')),
    content     TEXT NOT NULL,
    tokens_in   INTEGER NOT NULL DEFAULT 0,
    tokens_out  INTEGER NOT NULL DEFAULT 0,
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_messages_chat ON messages(chat_id);

-- kb_chunks: knowledge-base chunks. embedding column reserved for Phase 2 sqlite-vec.
CREATE TABLE IF NOT EXISTS kb_chunks (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    bot_id      INTEGER NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
    source      TEXT NOT NULL,
    chunk_index INTEGER NOT NULL,
    content     TEXT NOT NULL,
    embedding   BLOB,
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_kb_chunks_bot ON kb_chunks(bot_id);

CREATE TABLE IF NOT EXISTS leads (
    id                       INTEGER PRIMARY KEY AUTOINCREMENT,
    chat_id                  INTEGER NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
    email                    TEXT NOT NULL,
    summary                  TEXT NOT NULL DEFAULT '',
    captured_at              TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    hubspot_form_submitted_at TIMESTAMP,
    webhook_delivered_at     TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_leads_chat ON leads(chat_id);

CREATE TABLE IF NOT EXISTS magic_links (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    email       TEXT NOT NULL,
    token_hash  TEXT NOT NULL UNIQUE,
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at  TIMESTAMP NOT NULL,
    used_at     TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_magic_links_email ON magic_links(email);

CREATE TABLE IF NOT EXISTS sessions (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    user_email  TEXT NOT NULL,
    token_hash  TEXT NOT NULL UNIQUE,
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at  TIMESTAMP NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_sessions_email ON sessions(user_email);

CREATE TABLE IF NOT EXISTS audit_log (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    actor        TEXT NOT NULL,
    action       TEXT NOT NULL,
    target_type  TEXT NOT NULL,
    target_id    TEXT NOT NULL DEFAULT '',
    payload_json TEXT NOT NULL DEFAULT '{}',
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_audit_actor ON audit_log(actor);
CREATE INDEX IF NOT EXISTS idx_audit_target ON audit_log(target_type, target_id);
