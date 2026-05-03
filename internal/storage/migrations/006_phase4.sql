-- 006_phase4.sql — Phase 4 (DACH-Goldfragen-Korpus) schema additions.
--
-- Three new bot columns:
--   industry            — locked enum (validated in internal/corpus/
--                          taxonomy.go); nullable for legacy bots
--   eval_mode           — 'keyword' (Phase 2 default) | 'llm_judge'
--                          (Phase 4 default for corpus-seeded bots)
--   last_holdout_score  — populated by `everychat eval-holdout`;
--                          surfaced on the admin dashboard
--
-- The eval_questions_path column (folded in here) is what Sprint 2's
-- corpus.Seed writes to so a bot remembers which eval YAML it owns.

ALTER TABLE bots ADD COLUMN industry TEXT;
ALTER TABLE bots ADD COLUMN eval_mode TEXT NOT NULL DEFAULT 'keyword';
ALTER TABLE bots ADD COLUMN last_holdout_score REAL;
ALTER TABLE bots ADD COLUMN eval_questions_path TEXT;

CREATE INDEX IF NOT EXISTS idx_bots_industry ON bots(industry);
