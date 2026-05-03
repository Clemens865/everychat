# Everychat — Phase 4 (DACH-Goldfragen-Korpus) PRD

**Source-of-truth for Phase 4's autonomous build.** Phase 4 of the 6-phase v1 roadmap defined in `docs/blueprints/everychat-managed-blueprint.md`. Phase 3 (Embed + Lead Capture + DSGVO) shipped on `phase-3-embed`; this PRD picks up directly from `phase-3-complete`. Phases 5–6 are explicitly OUT OF SCOPE.

## Project Context

**Everychat Managed** — EU-sovereign LLM chatbot product for DACH SMB / Mittelstand. After Phase 3, bots can be authored, embedded, and run on customer sites with DSGVO compliance. **Phase 4 is the moat** — a curated corpus of industry-specific golden questions that, when paired with a customer's KB, lets bots pass 90% of *unseen* industry-relevant questions on a hold-out set.

**Phase 4 in one sentence:** during the wizard the founder picks an industry tag (`steuerberater`, `handwerk`, `anwaltskanzlei`, …); the corpus loader seeds the bot's eval YAML + few-shot prompt exemplars from `corpus/de/<industry>/`; the publish-gate eval now uses LLM-as-judge for semantic scoring; a hold-out evaluator (50 unseen questions per industry) provides the bind quality signal that 90% target hangs on.

**Why this is the moat.** Anyone can build a Claude-powered chatbot in a weekend. What's hard is *industry-grade quality* without each customer hand-curating 50 golden questions before they trust the publish-gate. The corpus collapses that onboarding cost — pick "Steuerberater," get a working baseline immediately.

Read `docs/blueprints/everychat-managed-blueprint.md` §4 Phase 4 for the full goal.

## Tech Stack (additions on top of Phase 3)

- **Corpus content format:** YAML per industry, three files each:
  - `questions.yaml` — the golden questions seeded into the bot's `tests/eval/<bot>.yaml`
  - `exemplars.yaml` — Q&A pairs blended into the system prompt as few-shot examples (paraphrasing-resistant)
  - `holdout.yaml` — 50 unseen questions used by the hold-out evaluator (never in the bot's own eval set, never in the system prompt)
- **LLM-as-judge model:** `claude-haiku` via the LiteLLM sidecar (cheap; Phase 5 may switch to a Phase-4-curated `judge` model alias if quality demands it). Judge prompt asks "does this answer satisfy this golden question?" with a structured `pass | fail | reason` JSON output.
- **Industry taxonomy:** locked to a small enum (`steuerberater`, `handwerk`, `anwaltskanzlei`, `maschinenbau`, `versicherung`, `saas-b2b`) — no free-form. Adding a new industry is a corpus-curation PR, not a runtime config change.
- **No new external dependencies.** Phase 4 is mostly schema, loader, prompt assembly, eval-mode flag.

## Phase 4 Goal

A founder can:

1. Run the new-bot wizard, pick a customer name + domain + **industry from a dropdown** (replaces Phase-3's free-text "Branche").
2. The wizard now automatically:
   - Crawls the customer's site (Phase 2 unchanged)
   - Drafts the system prompt with **few-shot exemplars** from `corpus/de/<industry>/exemplars.yaml` blended in
   - Seeds `tests/eval/<bot>.yaml` from `corpus/de/<industry>/questions.yaml`
3. Click "Eval ausführen" — the gate now uses **LLM-as-judge** scoring (configurable per bot via `bots.eval_mode = 'keyword' | 'llm_judge'`).
4. Run a new CLI: `everychat eval-holdout --bot-name=… --industry=…` against the 50 hold-out questions. Target: ≥ 90% pass rate. Result is admin-visible but does NOT block publish (the publish-gate eval is separate).
5. The admin dashboard surfaces a **Hold-out-Score** badge per published bot.

The deliverable is the corpus-engineering scaffold + initial content for **two industries** (`steuerberater` + `handwerk`). Recruiting 4–6 design-partner customers to contribute KB + content for the other industries is human-work, explicitly out of scope here — see Notes for the Build Agent.

## Out of Scope for Phase 4

- ❌ Free-form industry tags (locked enum)
- ❌ Cross-language corpora (DE only — AT/CH locale tags arrive in Phase 6)
- ❌ A/B testing of corpora (Phase 5+)
- ❌ Customer-private corpus uploads (corpus is shared/version-controlled; per-tenant overlay arrives Phase 5)
- ❌ Real design-partner recruitment (human work; PRD lists initial corpus seeds for two industries only)
- ❌ Production scaling, multi-customer ops control plane (Phase 5)
- ❌ Public launch (Phase 6)
- ❌ Anything from Phases 5–6

## Repo Conventions (additions)

- `corpus/de/<industry>/{questions,exemplars,holdout}.yaml` — checked-in content. Treated as code: PR review, conventional-commit style.
- `internal/corpus/` — loader, schema validators, taxonomy enum.
- `internal/prompt/fewshot.go` — exemplar blending helpers consumed by `prompt.Generator`.
- `internal/eval/judge.go` — LLM-as-judge scoring mode alongside the keyword scorer.
- `internal/storage/migrations/006_phase4.sql` — adds `bots.industry`, `bots.eval_mode`, `bots.last_holdout_score`.
- `cmd/everychat eval-holdout` — new subcommand.

## Validation Gate (applies to every sprint)

- `go vet ./...` passes
- `go test ./...` passes
- `golangci-lint run` passes
- **Eval gate**: `everychat eval --bot-name=steuerkanzlei-demo --questions=tests/eval/steuerkanzlei-demo.yaml` reports score ≥ 0.85 (regression check; the seeded demo bot stays the canary even after corpus changes).
- **Hold-out gate** (sprints 5+): `everychat eval-holdout --bot-name=steuerkanzlei-demo --industry=steuerberater` reports score ≥ 0.85 (target is 0.90 by sprint 6 — but sprint 5 introduces the bar so it can't fail itself).
- No secrets in committed files.
- Sprint's acceptance criteria demonstrably met.

---

## Sprint 1: Corpus directory + schema + loader

Lays the foundation. No behavior changes to the bot yet — just the structure that the next four sprints consume.

### Tasks
- Migration `internal/storage/migrations/006_phase4.sql`:
  - `bots.industry TEXT` (nullable; locked enum at the loader layer)
  - `bots.eval_mode TEXT NOT NULL DEFAULT 'keyword'` (`keyword` | `llm_judge`)
  - `bots.last_holdout_score REAL` — populated by the hold-out runner; surfaced on the dashboard
- `corpus/` directory at repo root with the six industry subdirs created (empty `questions.yaml` / `exemplars.yaml` / `holdout.yaml` placeholders for the four non-seeded industries; real content for `steuerberater` + `handwerk` lands in Sprint 6).
- `internal/corpus/taxonomy.go` — `Industry` typed string with the locked enum, `Industries()` returning the slice, `Valid(s)` predicate.
- `internal/corpus/loader.go`:
  - `Load(industry Industry) (*Bundle, error)` — reads + validates the three YAML files via `//go:embed corpus/de/*/*.yaml`
  - `Bundle{Questions []eval.Question; Exemplars []QAPair; Holdout []eval.Question}`
  - Validates: at least 10 questions, 3 exemplars, 50 holdout questions per industry; question IDs unique within each file; exemplars carry both `q` and `a` fields.
- `internal/corpus/loader_test.go`:
  - Each industry's bundle loads cleanly (or returns ErrEmpty for placeholders)
  - Schema rejects: missing fields, duplicate IDs, holdout-questions-overlap-with-questions (would invalidate the blind eval)
- `internal/storage/bots.go` — `SetIndustry(ctx, db, id, industry)` + `SetEvalMode` + `SetLastHoldoutScore` helpers; `Bot` struct gains the three columns.

### Acceptance
- `go test ./internal/corpus/...` passes
- `tree corpus/` shows six subdirs, each with three files (placeholders OK except for `steuerberater/`)
- Migration 006 applies idempotently
- All existing tests still pass; eval gate ≥ 0.85

### Out of Scope
- Wizard integration (Sprint 2)
- Few-shot blending (Sprint 3)
- LLM judge (Sprint 4)

---

## Sprint 2: Industry picker in wizard + corpus seed-on-create

The corpus becomes visible to the founder at bot-creation time.

### Tasks
- `internal/web/templates/wizard.html` — replace the free-text "Branche" input with a `<select>` populated from `corpus.Industries()`. Keep the field name `industry` so the POST handler picks it up.
- `internal/web/web.go`:
  - `wizardCreate` reads `industry` from the form, validates against `corpus.Valid`, calls `storage.SetIndustry` after `CreateBot`.
  - `wizardDraft` (Sprint 4 of Phase 2) now also calls a new `corpus.Seed(botID, industry)` helper that:
    - Writes `tests/eval/<bot-name>.yaml` from `corpus/de/<industry>/questions.yaml`
    - Persists the resulting filename to `bots.eval_questions_path` (new nullable column — fold into migration 006)
- `internal/corpus/seed.go` — handles writing the eval YAML file under `tests/eval/`. Creates the directory if missing.
- Tests:
  - `wizardCreate_test` (or extend existing) — picking `industry=steuerberater` results in a bot row whose industry column matches AND the eval file gets written.
  - Picking `industry=invalid` → 400.

### Acceptance
- New-bot wizard shows the industry dropdown with the locked enum.
- Creating a bot with `industry=steuerberater` produces a `tests/eval/<bot-name>.yaml` file pre-populated with the corpus questions.
- Existing seeded `steuerkanzlei-demo` bot's eval still scores ≥ 0.85 (regression).
- Eval-modal "Beispiel laden" button still works for bots without a corpus seed.

### Out of Scope
- Few-shot blending in the system prompt (Sprint 3)
- Per-bot corpus override / customization (Phase 5+)

---

## Sprint 3: Few-shot prompt assembly

Exemplars from the corpus get blended into the drafted system prompt so the LLM has worked examples on hand at inference time.

### Tasks
- `internal/prompt/fewshot.go`:
  - `Blend(systemPrompt string, exemplars []corpus.QAPair, max int) string` — appends a `# Beispiele\n\nFrage: …\nAntwort: …` block to the prompt, capped at `max` exemplars. Default 4.
  - Skips blending if exemplars is empty or systemPrompt is empty.
- `internal/prompt/generator.go` — extend `Draft` to accept an optional `industry` parameter; if non-empty, loads exemplars via `corpus.Load(industry).Exemplars` and calls `Blend` after Claude returns the base prompt.
- Wizard's `wizardDraft` handler passes `bot.Industry` into `prompt.Draft`.
- Editor's "Neu generieren" button picks up the same path.
- Tests:
  - `Blend_AppendsExemplars` — output contains all `Frage:`/`Antwort:` lines
  - `Blend_RespectsMax` — feeding 10 exemplars with max=3 yields 3
  - `Blend_NoOpWhenEmpty` — blank exemplars list returns the prompt unchanged
  - `Generator_DraftWithIndustry` — mock LLM + a tiny corpus fixture; assert exemplars land in the final draft

### Acceptance
- `prompt.Draft(bot, industry="steuerberater")` returns a system prompt that ends with a `# Beispiele` block containing 4 Q&A pairs from the corpus.
- The editor's "Neu generieren" button produces a prompt with the exemplars when the bot has an industry set.
- Eval gate ≥ 0.85 (the seeded demo bot doesn't have an industry yet, so its prompt is unchanged — regression-safe).

### Out of Scope
- LLM-as-judge (Sprint 4)
- Hold-out evaluator (Sprint 5)

---

## Sprint 4: LLM-as-judge eval scoring

Keyword scoring (Phase 2) becomes one of two modes; the new judge mode handles paraphrase-tolerant evaluation that the corpus questions need.

### Tasks
- `internal/eval/judge.go`:
  - `JudgeScorer` implements an interface alongside the existing keyword scorer
  - Asks the judge LLM (`claude-haiku` model alias) a structured prompt:
    > Given the golden question, the expected facts (`must_contain` becomes a hint, not a hard match), and the bot's actual answer — does the answer satisfy the question? Return JSON `{"pass": true/false, "reason": "<≤120 chars>"}`.
  - Parses the JSON via the same `extractJSONObject` shape used in `widget.Suggester`.
- `internal/eval/runner.go` — `Run` now picks the scorer based on `bot.EvalMode`. Both modes use the same Report shape so the scorecard renderer + UI don't need to fork.
- `internal/llm/litellm/config.yaml` — register a `claude-haiku` model alias pointing at `anthropic/claude-haiku-4-5`.
- Tests:
  - `judge_test.go` with mock LLM: pass-verdict, fail-verdict-with-reason, malformed-JSON-falls-through-to-keyword, judge-LLM-error → row marked failed with the error reason.
  - `runner_test.go` extended to cover both modes against the same fixture; assert mode switch is reflected in the persisted `eval_runs.report_json`.
- Editor settings rail — add an "Eval-Modus" radio (`Keyword` / `LLM-Judge`) in the Evaluation group; HTMX-saves to `bots.eval_mode`.

### Acceptance
- `everychat eval --bot-name=steuerkanzlei-demo` with `eval_mode=llm_judge` runs against Claude-Haiku and produces a scorecard with reasons.
- Switching the demo bot from keyword to llm_judge keeps the score ≥ 0.85 (target: a small uplift on edge cases like the refusal questions where keywords miss valid answers).
- Cost per run is logged: target ≤ €0.05 per 15-question eval at claude-haiku rates; surfaced in the scorecard footer.

### Out of Scope
- Switching the visitor-chat moderation to LLM (still Phase 5 — that's `internal/moderation`, not `internal/eval`)
- Hold-out evaluator (Sprint 5)

---

## Sprint 5: Hold-out evaluator + `everychat eval-holdout` CLI

The blind quality signal that the 90% Phase-4 acceptance hangs on.

### Tasks
- `cmd/everychat` — new subcommand `eval-holdout --bot-name=… --industry=… [--top-k=50]`. Loads `corpus/de/<industry>/holdout.yaml`, runs each question through the bot's RAG pipeline (same retrieval path as `/api/v1/chat`), scores via the bot's configured `eval_mode`. Prints a scorecard, persists `bots.last_holdout_score`, exits 2 if score < 0.85.
- `internal/eval/holdout.go` — orchestrator: borrows `eval.Runner` for the per-question loop, but injects RAG context via `storage.SearchChunks` so the bot's actual production prompt+KB is what's evaluated. Otherwise the hold-out is testing the pure system prompt, not the deployed shape.
- Admin dashboard — new column on `admin_home.html` showing `Hold-out-Score: 47/50 (0.94)` per bot when `last_holdout_score` is set.
- Tests:
  - `holdout_test.go` with mock LLM + a fixture corpus → asserts persisted `last_holdout_score`
  - Holdout questions never overlap with questions.yaml (loader-level invariant tested in Sprint 1; re-asserted as an integration test here)

### Acceptance
- `./bin/everychat eval-holdout --bot-name=steuerkanzlei-demo --industry=steuerberater` runs cleanly. The first run can score below 0.90 (Sprint 6 gets it to target by completing the corpus content); the *baseline* must be reported and stored.
- Dashboard renders the hold-out score on the seeded demo bot.
- Existing eval gate still ≥ 0.85.

### Out of Scope
- Public-facing hold-out leaderboard (never — corpus must stay blind)
- Hold-out ablation across model versions (Phase 5)

---

## Sprint 6: Initial corpus content for `steuerberater` + `handwerk`

The content sprint. Engineering work is small (validation tooling + a fixture script that asserts coverage); the content itself is human-curated.

### Tasks
- `corpus/de/steuerberater/`:
  - `questions.yaml` — 15 keyword-and-judge-friendly golden questions covering: Leistungen, Lohnbuchhaltung, Jahresabschluss, Honorar (StBVV), ELSTER, Erstgespräch, Verschwiegenheit, Existenzgründung, Betriebsprüfung, Umsatzsteuer-Voranmeldung, Lohnabrechnung, Mandatswechsel, Steuerklasse, AFA, Bewirtungsbelege.
  - `exemplars.yaml` — 6 Q&A pairs (paraphrased so they don't overlap with `questions.yaml`); demonstrate the desired tone + refusal patterns + handoff-to-Anwalt for legal questions.
  - `holdout.yaml` — 50 unseen questions; explicitly disjoint from `questions.yaml` (loader test enforces this).
- `corpus/de/handwerk/`:
  - Same three files, scaled to handwerk topics: Gewerk-Spezialisierung, Stundensatz/Material, Rechnungsstellung, Mehrwertsteuer 7%/19%, Auftragsannahme, Gewährleistung, VOB/B, Aufmaß, Skontosatz, etc.
- `tools/check-corpus.go` (or `Makefile` target `make check-corpus`) — invokes `corpus.Load(industry)` for every industry that has non-empty content; fails loud if validation breaks.
- README addendum: how to add a new industry (PR template, validation steps).

### Acceptance
- `make check-corpus` passes.
- `everychat eval-holdout --bot-name=steuerkanzlei-demo --industry=steuerberater` against the curated corpus scores ≥ 0.85 (stretch: 0.90, the Phase-4 PRD goal).
- Eval gate still ≥ 0.85.
- The `handwerk` industry loads cleanly in the wizard (creating a bot with `industry=handwerk` produces a populated eval YAML + few-shot exemplars in the prompt).

### Out of Scope
- The remaining four industries (recruit + curate in a follow-up phase or with design-partner help)
- Translation to AT/CH locales (Phase 6)
- LLM-generated corpus expansion (interesting, but Phase 5+ research)

---

## Final Phase 4 Acceptance

After Sprint 6, the founder can:

1. Run `make compose`, log in, click "+ Neuer Bot", type a customer name + domain, **pick "Steuerberater" or "Handwerk" from the industry dropdown**.
2. Wizard runs; the editor lands with:
   - System prompt that includes 4 industry-specific Q&A exemplars
   - `tests/eval/<bot-name>.yaml` already populated with 15 corpus questions
   - Eval-modus rail set to `llm_judge` by default for corpus-seeded bots
3. Click "Evals ausführen" — LLM-as-judge run scores ≥ 0.85.
4. Run `./bin/everychat eval-holdout --bot-name=<id> --industry=steuerberater` from the host — 50 unseen questions, score ≥ 0.85 (stretch 0.90).
5. The dashboard shows the hold-out score badge next to the bot.

This is the deliverable. Phase 5 (Production hardening) starts when the founder reviews + approves Phase 4 results — and ideally has corpus content for at least one design-partner customer's industry.

## Notes for the Build Agent

- **Corpus content IS the moat — engineering is the loader.** Don't try to LLM-generate the corpus content during this build; the engineering scaffold is what's autonomously buildable. The actual industry knowledge for `steuerberater` + `handwerk` lives in the `corpus/de/<industry>/*.yaml` files committed alongside Sprint 6's code; treat curation quality the same way you'd treat a security review.
- **Hold-out blindness is a hard invariant.** `holdout.yaml` MUST NOT overlap with `questions.yaml`. The loader test (Sprint 1) enforces this. Never train the bot's prompt or eval set on hold-out content — that defeats the entire 90% Phase-4 metric.
- **Cost discipline:** LLM-as-judge runs every eval; that's why we picked claude-haiku and why each scorecard logs cost. If a single eval ever exceeds €0.10, debug before merging.
- **Eval gate stays alive:** every sprint must keep the seeded `steuerkanzlei-demo` eval ≥ 0.85.
- **Branch + commits:** all Phase-4 work on a new `phase-4-corpus` branch cut from `phase-3-complete`. Push to `origin` after every meaningful unit; one commit per sprint minimum, conventional-commit style (`feat(p4-sprint-N): …`).
- **Deferred:** customer-private corpus overlays, A/B testing across corpora, AT/CH locale variants, and recruiting real design partners — all explicitly Phase 5+ or human-only work.
