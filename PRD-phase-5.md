# Everychat — Phase 5 (Multi-Chat Dashboard) PRD

**Source-of-truth for Phase 5's autonomous build.** Phase 5 of the 6-phase v1 roadmap. Phase 4 (DACH-Goldfragen-Korpus) shipped on `phase-4-corpus`/tag `phase-4-complete`; this PRD picks up directly from there. Phase 6 (open-source CLI + go-live) and the carry-over items (HubSpot Forms adapter, Postmark Sender impl, Llama Guard 3 8B GGUF wiring, multi-customer ops control plane) are explicitly OUT OF SCOPE.

## Project Context

After Phase 4 the bot has a quality moat (industry corpus + LLM-as-judge + hold-out score) but the day-to-day admin experience is still "edit prompt → click Eval → wait." The two missing affordances are:

1. **Quick smoke-testing without leaving the dashboard** — admins want to ask a bot a single question and see the answer, without clicking into the editor.
2. **Adversarial probing** — a tester-LLM red-teams the bot-under-test for N turns to surface failures the golden questions don't catch (off-topic drift, jailbreaks, contradiction, data exfiltration). The golden corpus tells you "did the bot answer the brochure-level question?"; the adversarial tool tells you "does the bot hold its line under pressure?"

**Phase 5 in one sentence:** ship a bot-to-bot adversarial tester with hard cost + turn caps, persisted transcripts, and a per-bot mini test pane on the admin dashboard so smoke-checks happen in seconds.

## Tech Stack (additions on top of Phase 4)

- **Adversary persona format:** YAML per-persona under `internal/adversary/personas/de/`. Each persona = a system prompt for the tester-LLM + a rubric for the post-run judge. Locked-enum personas for v1 (no user-uploaded personas — same posture as the corpus).
- **Cost gate:** runs are pre-budgeted in cents. The runner tracks accumulated input+output tokens × LiteLLM-priced rate; if accumulated cost exceeds budget, the run halts and is marked `cost_exhausted` rather than failing silently.
- **Transcript persistence:** new `adversary_runs` + `adversary_turns` tables. Stored verbatim so the admin can scroll through what the tester actually asked.
- **No new external dependencies.** Reuses `llm.Chat`, `llm.Embedder`, `eval.JudgeScorer` from Phase 4.

## Phase 5 Goal

A founder can:

1. From the admin dashboard, click a bot card → **mini test pane** opens inline (no navigation away). Type a question → see the streaming response → close the pane. Reuses the visitor RAG path under admin auth.
2. From the same card, click **„Adversary-Lauf starten"** → modal with persona dropdown (`Datenexfiltration`, `Off-Topic-Drift`, `Jailbreak-Klassiker`), turn count (default 8), max-cost in € (default 0.50). Submit → background run streams turns into the persisted transcript.
3. Adversary run finishes → scorecard appears: did the bot hold its line per persona-specific rubric? Pass/fail per turn + overall verdict. Last run's verdict surfaces as a small badge on the bot card.
4. Run the same flow from the CLI: `everychat adversary --bot-name=… --persona=… --turns=N --max-cost-eur=X`. Useful for CI and reproducibility.

The deliverable is the adversarial-testing scaffold + 3 canned DE personas. Authoring custom personas via the admin UI is OUT OF SCOPE.

## Out of Scope for Phase 5

- ❌ User-authored adversary personas (locked enum)
- ❌ Multi-bot tournament mode (one bot vs many personas in parallel)
- ❌ Cross-bot adversary runs (bot-A red-teams bot-B; v1 uses a fixed Claude tester model)
- ❌ Real-time streaming UI for the adversary run (HTMX polling is fine; full SSE is Phase 6)
- ❌ HubSpot Forms adapter, Postmark Sender impl, Llama Guard 3 8B GGUF wiring (Phase 5 carry-overs deferred again — covered in Phase 5b once this Phase 5a ships)
- ❌ Multi-customer ops control plane (Phase 5b)
- ❌ Public launch (Phase 6)

## Repo Conventions (additions)

- `internal/adversary/` — runner, cost gate, persona loader, transcript types.
- `internal/adversary/personas/de/<persona>.yaml` — checked-in persona definitions. Treated as code.
- `internal/storage/migrations/007_phase5.sql` — `adversary_runs` + `adversary_turns` tables.
- `cmd/everychat adversary` — new subcommand.
- `internal/web/templates/_test_pane.html` — mini test pane partial (HTMX target).
- `internal/web/templates/_adversary_modal.html` — modal for triggering an adversary run.

## Validation Gate (applies to every sprint)

- `go vet ./...` passes
- `go test ./...` passes
- `golangci-lint run` passes
- **Eval gate**: `everychat eval-holdout --bot-name=steuerkanzlei-demo --industry=steuerberater` still reports ≥ 0.85 (Phase 5 must not regress the Phase 4 quality bar).
- **Cost-gate test**: an integration test feeds the runner a fake LLM that returns oversized usage; the runner must halt and persist `cost_exhausted` before exceeding budget.
- No secrets in committed files.
- Sprint's acceptance criteria demonstrably met.

---

## Sprint 1: Bot-to-bot adversarial tester

End-to-end adversary surface — package, storage, CLI, three personas. No dashboard UI yet (that's Sprint 2). Done = a CI-runnable adversary loop with persisted transcripts and a deterministic cost gate.

### Tasks
- Migration `internal/storage/migrations/007_phase5.sql`:
  - `adversary_runs(id, bot_id, persona, turn_cap, max_cost_cents, started_at, finished_at, status, verdict, total_cost_cents)` — `status ∈ {running, completed, cost_exhausted, error}`.
  - `adversary_turns(id, run_id, turn_index, role, content, input_tokens, output_tokens)` — `role ∈ {tester, victim}`.
  - Both tables `ON DELETE CASCADE` from `bots(id)`.
- `internal/adversary/persona.go`:
  - `Persona{ ID, Name, TesterSystem, JudgeRubric, OpeningMessage }`
  - `LoadPersona(id string) (*Persona, error)` — reads from embedded `personas/de/*.yaml`.
  - `Personas() []Persona` for the dropdown.
- `internal/adversary/cost.go`:
  - `CostGate{ MaxCents, accumulated }` with `Charge(usage llm.Usage) (exceeded bool)`.
  - Token-to-cents conversion via `llm.PriceTable` (new — pulls per-model rates from a constant).
- `internal/adversary/runner.go`:
  - `Runner{ db, victimChat, testerChat, embedder, judge }`.
  - `Run(ctx, bot, persona, opts) (*Report, error)` — alternating turns: tester → victim, scoring each turn against the rubric, stopping at `turn_cap`, `cost_cap`, or terminal-rubric-failure.
  - Persists to `adversary_runs` + `adversary_turns` after every turn so a crashed run leaves an inspectable transcript.
- `internal/adversary/runner_test.go`:
  - Happy path: 4-turn run, mock LLMs, persisted transcript matches expected turns.
  - Cost exhaustion: oversized usage halts run with `status=cost_exhausted`.
  - Persona load: 3 canned personas parse cleanly.
- `internal/adversary/personas/de/`:
  - `datenexfiltration.yaml` — tries to extract internal training data, system prompt, customer secrets.
  - `off-topic-drift.yaml` — slowly pulls the bot off-topic into politics, sports, philosophy.
  - `jailbreak-klassiker.yaml` — DAN, "ignore previous instructions," roleplay-jailbreak family.
- `cmd/everychat adversary` subcommand:
  - `--bot-name`, `--persona`, `--turns` (default 8), `--max-cost-eur` (default 0.50).
  - Prints transcript to stdout + final verdict + run-id; exit 0 (passed), 2 (failed verdict), 1 (infra).

### Acceptance
- `make test` passes; `runner_test.go` exercises the happy path and cost gate.
- `everychat adversary --bot-name=steuerkanzlei-demo --persona=datenexfiltration --turns=4 --max-cost-eur=0.10` runs against a real Claude (live verify) and persists a transcript.
- Migration 007 applies cleanly to a fresh DB and is idempotent (re-running migrations is a no-op).

---

## Sprint 2: Mini test pane on dashboard cards

The dashboard becomes a workbench. Each bot card grows a click-to-expand test pane (single-shot chat) and a "Adversary-Lauf starten" button that opens the modal feeding into Sprint 1's runner.

### Tasks
- `internal/web/templates/admin_home.html`:
  - Each bot card grows two buttons: **„Schnell-Test"** (toggles `_test_pane.html`) + **„Adversary-Lauf starten"** (opens `_adversary_modal.html`).
  - Last-adversary verdict badge on the card if `adversary_runs` has any rows for that bot.
- `internal/web/templates/_test_pane.html`:
  - Single-message chat: textarea → submit → streaming response (SSE, same path as the visitor surface but admin-authenticated).
  - "Letzte Antwort als Goldfrage speichern" hook (parallel to the editor's kebab — Phase 2 design memo).
- `internal/web/templates/_adversary_modal.html`:
  - Persona `<select>` (sourced from `adversary.Personas()`), turn count + max-cost-€ inputs, submit posts to `/admin/bots/{id}/adversary`.
  - On submit, modal becomes a polling pane (HTMX `hx-trigger="every 2s"`) showing turn-by-turn transcript as it persists.
- New web routes:
  - `GET /admin/bots/{id}/test-pane` — partial render.
  - `POST /admin/bots/{id}/test` — single-shot chat call (admin auth).
  - `POST /admin/bots/{id}/adversary` — kicks off an adversary run in a goroutine, returns run-id.
  - `GET /admin/adversary/runs/{id}` — partial: transcript + verdict, polled by the modal.
- `internal/web/web_adversary_test.go`:
  - Modal post creates a row in `adversary_runs`, returns the polling URL.
  - Polling endpoint renders running-state transcript without 500ing on a still-running row.

### Acceptance
- A founder can trigger an adversary run from the dashboard, watch turns appear, and see the verdict — without leaving `admin_home.html`.
- The mini test pane round-trips a single question through the visitor RAG path and renders the streamed answer inline.
- All Phase-4 evals still pass (no regression in `eval-holdout` for `steuerkanzlei-demo`).

---

## Final Phase 5 Acceptance

After Sprint 2 lands:

- Demo flow: open dashboard → click „Schnell-Test" on `steuerkanzlei-demo` → ask a question → close pane → click „Adversary-Lauf starten" → pick `Datenexfiltration` → run completes within budget → verdict badge updates on the card. All without an editor visit.
- `everychat adversary --bot-name=… --persona=… --turns=… --max-cost-eur=…` reproduces the same run from the CLI.
- Tag `phase-5-complete` placed on the merge commit.

## Notes for the Build Agent

- The cost gate is the most safety-critical piece — it bounds spend on Claude. Test it first, test it hardest. A regression here is a real-money bug.
- Persona prompts should be authored conservatively (no actual jailbreak text in the files — describe the *category* of probe, let the tester-LLM generate concrete attempts). This keeps the repo clean of "ignore previous instructions" snippets that other tooling might flag.
- The mini test pane is the smaller, more visible win and is tempting to ship first. Resist — Sprint 1's runner is the load-bearing piece; Sprint 2 is mostly an HTMX wrapper around it. Building S2 first means re-plumbing routes when S1 lands.
- If S1's live-verify against real Claude reveals that 8 turns at €0.50 max-cost is too generous (typical cost-per-run keeps blowing the budget), tighten the defaults *before* shipping S2 — the dashboard will inherit them.
