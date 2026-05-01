-- 003_vec.sql — Phase 2 sqlite-vec virtual table for KB embeddings.
--
-- Locked to FLOAT[1536] (OpenAI text-embedding-3-small). Phase 4 EU-
-- sovereignty pass swapping to Voyage-3 (1024-d) requires a fresh
-- migration that recreates this table; existing chunks must be re-embedded.
--
-- Schema:
--   * chunk_id  — 1:1 with kb_chunks.id (so JOINs are trivial)
--   * bot_id    — partition key; vec0 uses it to prune the search space
--                 before the KNN scan, which is what keeps multi-bot
--                 deployments fast
--   * embedding — the vector itself
CREATE VIRTUAL TABLE IF NOT EXISTS kb_vec USING vec0(
    chunk_id INTEGER PRIMARY KEY,
    bot_id INTEGER PARTITION KEY,
    embedding FLOAT[1536]
);

-- Keep kb_vec in sync with deletes from kb_chunks. Inserts and updates are
-- explicit from Go (the embedding doesn't live in kb_chunks anymore — the
-- BLOB column is reserved but unused since Phase 1).
CREATE TRIGGER IF NOT EXISTS kb_chunks_after_delete
AFTER DELETE ON kb_chunks
BEGIN
    DELETE FROM kb_vec WHERE chunk_id = OLD.id;
END;
