# Everychat — Phase 2 (Bot Authoring Core) PRD

**Source-of-truth for Phase 2's autonomous build.** Phase 2 of the 6-phase v1 roadmap defined in `docs/blueprints/everychat-managed-blueprint.md`. Phase 1 (Foundation) shipped on `phase-1-foundation`; this PRD picks up directly from that state. Phases 3–6 are explicitly OUT OF SCOPE.

## Project Context

**Everychat Managed** — EU-sovereign LLM chatbot product for DACH SMB / Mittelstand. Single-tenant Go binary architecture (one VM per customer), sold as managed-hosted SaaS. Default LLM is Anthropic Claude. POC runs locally in Docker Compose.

**Phase 2 in one sentence:** the founder can build a bot end-to-end (crawl a domain → cleaned KB → drafted system prompt → editable in admin UI → testable in a sandbox), and a hard publish gate refuses to flip a bot to live until a golden-questions eval passes and privacy/AGB URLs are set.

**Eval-first ordering (deviation from blueprint).** The blueprint lists the eval harness mid-phase, but the eval *is* the publish-gate quality bar — every later sprint depends on it being trustworthy. So Sprint 2 ships the eval harness, and Sprints 3–6 must register passing evals before advancing.

Read `docs/blueprints/everychat-managed-blueprint.md` for full architectural context (especially §3.5 Data flow — visitor message and §4 Phase 2).

## Tech Stack (additions on top of Phase 1)

- **LLM SDK:** `github.com/anthropics/anthropic-sdk-go` — talks to Claude (default model `claude-sonnet-4-6`)
- **LLM gateway:** LiteLLM Python sidecar in `internal/llm/litellm/` — Dockerized, exposes a single OpenAI-compatible chat endpoint that the Go binary calls. Phase 2 routes Claude through it; future providers (OpenAI, Mistral, local) plug in here without touching Go.
- **Vector storage:** `sqlite-vec` extension (Mike Lindenberger's WASM-ready vec0 virtual tables) loaded at runtime via `github.com/mattn/go-sqlite3`'s extension hook. Embeddings are stored in `kb_chunks.embedding` (the BLOB column reserved in Phase 1 migration `001_init`).
- **Embeddings:** Anthropic's `voyage-3` (or `voyage-3-large`) via the LiteLLM sidecar — keeps a single egress point. If Voyage isn't available, fall back to OpenAI `text-embedding-3-small` via LiteLLM. Decision recorded in Sprint 1.
- **Markdown / HTML parsing:** `github.com/JohannesKaufmann/html-to-markdown/v2` (HTML→Markdown), `github.com/yuin/goldmark` (Markdown→AST → chunks)
- **Crawler:** `github.com/gocolly/colly/v2` for politeness, robots.txt, depth/host limits
- **YAML:** `gopkg.in/yaml.v3` — golden-questions and eval reports
- **Streaming UI:** Phase 1's HTMX placeholder gets replaced with the real `htmx.min.js` (2.0.x) for token-streaming chat in the test sandbox

## Phase 2 Goal

A founder can:

1. From the Phase 1 admin UI, click **"Create your first bot"** and run through a wizard: enter the customer name + domain → backend crawls the public site → drafted system prompt appears in the prompt editor.
2. Edit the system prompt, save it as a *draft*, click **"Test in sandbox"** to chat with the draft (token-streaming, RAG over crawled KB).
3. Author a golden-questions YAML file and click **"Run evals"** to see a pass/fail scorecard.
4. Click **"Publish"** — the gate refuses unless evals reach the configured threshold AND `privacy_policy_url` AND `agb_url` are set on the bot. Override is **not** possible in Phase 2.
5. Run `./bin/everychat eval --bot-id=<id> --questions=path/to/questions.yaml` from the CLI and see the same scorecard.

No real visitor traffic, no embed widget, no lead capture — those land in Phase 3.

## Out of Scope for Phase 2

- ❌ iframe widget / embed.js / cross-origin chat (Phase 3)
- ❌ Lead capture / HubSpot Forms / webhooks (Phase 3)
- ❌ DSGVO erasure cascade beyond schema TODOs (Phase 3)
- ❌ Llama Guard / moderation pipeline (Phase 5)
- ❌ Mollie billing (Phase 5)
- ❌ Real Postmark email — stdout sender stays from Phase 1
- ❌ Real Hetzner provisioning — stub stays from Phase 1
- ❌ Multi-tenant ops control plane (Phase 5)
- ❌ Authoring multiple bots per tenant — Phase 2 ships one-bot-per-tenant; the schema already permits more, the UI deliberately doesn't expose it
- ❌ Anything from Phases 3–6

## Repo Conventions (additions)

- `internal/llm/` — LLM client, streaming chat, embedding interface
- `internal/llm/litellm/` — Dockerfile + config for the LiteLLM sidecar; mounted as a sibling service in `docker-compose.yml`
- `internal/eval/` — golden-questions schema, runner, scorecard
- `internal/crawler/` — Colly-based domain crawler
- `internal/ingest/` — HTML→Markdown, chunking, embedding orchestration
- `internal/prompt/` — Claude-drafted system prompt generation
- `internal/storage/migrations/002_phase2.sql` — schema additions for Phase 2 (see Sprint 2 / Sprint 3 tasks)
- `internal/storage/migrations/003_vec.sql` — sqlite-vec virtual table for KB embeddings
- `tests/eval/` — example golden-questions YAML files used in tests and demos

## Validation Gate (applies to every sprint)

- `go vet ./...` passes
- `go test ./...` passes
- `golangci-lint run` passes (Phase 1 config still applies)
- `everychat eval --bot-id=<demo>` runs against the Sprint 2 demo bot and reports ≥ Phase-2 threshold (sprints 3+ only — Sprint 2 introduces the bar)
- No secrets in committed files (`.env` stays git-ignored; LiteLLM API keys live in `.env`)
- Sprint's acceptance criteria demonstrably met

---

## Sprint 1: LLM Client + LiteLLM Sidecar

Ship a single-purpose LLM layer the rest of Phase 2 can call: streaming chat completion + embedding generation, both routed through a LiteLLM Python sidecar so model swaps don't touch Go.

### Tasks
- Add dependencies: `github.com/anthropics/anthropic-sdk-go`, `gopkg.in/yaml.v3`
- `internal/llm/llm.go` — interface `Chat` with method `Stream(ctx, ChatRequest) (<-chan ChatChunk, error)` and interface `Embedder` with method `Embed(ctx, []string) ([][]float32, error)`. Concrete impl `LiteLLMClient` calls the sidecar's OpenAI-compatible `/v1/chat/completions` (with `stream: true`) and `/v1/embeddings` endpoints
- `internal/llm/litellm/Dockerfile` — `python:3.12-slim` + `litellm[proxy]==1.74.x`
- `internal/llm/litellm/config.yaml` — defines two model groups: `claude-sonnet` (Anthropic) and `embed` (Voyage with OpenAI fallback). API keys loaded from env (`ANTHROPIC_API_KEY`, `VOYAGE_API_KEY`, optional `OPENAI_API_KEY`)
- Extend `docker-compose.yml` with a `litellm` service on port 4000, internal-only (Caddy does not expose it)
- Wire `EVERYCHAT_LITELLM_URL` env var into `cmd/everychat/main.go`; default `http://litellm:4000` in compose, `http://127.0.0.1:4000` for `make dev`
- Update `.env.example` with `ANTHROPIC_API_KEY`, `VOYAGE_API_KEY`, `OPENAI_API_KEY` (commented), `EVERYCHAT_LITELLM_URL`
- Tests in `internal/llm/llm_test.go` — `httptest.NewServer` stub mocks LiteLLM responses; cover happy-path streaming, embedding shape, non-200 errors, context cancellation

### Acceptance
- `make compose` boots three containers: `everychat`, `caddy`, `litellm`
- A throwaway main or test calling `LiteLLMClient.Stream` against the running sidecar with a real `ANTHROPIC_API_KEY` returns a Claude completion in under 10s
- `LiteLLMClient.Embed(["hello"])` returns one 1024-dim (Voyage) or 1536-dim (OpenAI) float32 vector
- All llm tests pass without network access (stubbed httptest)
- README's "Quickstart (Docker Compose POC)" section adds a note: "set `ANTHROPIC_API_KEY` in `.env` before `make compose`"

### Out of Scope
- Streaming UI in the browser (Sprint 5)
- Token accounting persisted to `messages.tokens_in/out` (Sprint 5 — when chat happens against a saved bot)
- Per-tenant model overrides (Phase 5+)

---

## Sprint 2: Eval Harness + Golden-Questions YAML + Scorecard

The publish-gate quality bar. Built before any sprint that depends on the LLM behaving correctly.

### Tasks
- Migration `internal/storage/migrations/002_phase2.sql`:
  - Add `bots.eval_threshold REAL NOT NULL DEFAULT 0.85` (fraction of golden questions that must pass)
  - New table `eval_runs (id, bot_id, questions_file, total, passed, score, started_at, finished_at, report_json)` — eval history per bot
- Define golden-questions schema in `internal/eval/schema.go`:
  ```yaml
  bot: steuerkanzlei-demo
  questions:
    - id: q1-leistungen
      ask: "Welche Leistungen bietet die Kanzlei an?"
      must_contain: ["Steuerberatung", "Lohnbuchhaltung"]
      must_not_contain: ["Rechtsberatung"]
      max_tokens: 400
  ```
- `internal/eval/runner.go` — `Run(ctx, bot Bot, questionsPath) (*Report, error)`:
  - Load YAML → for each question, call `llm.Chat.Stream` with the bot's `system_prompt` (no RAG yet — Sprint 4 plugs that in)
  - Score each answer: pass iff every `must_contain` is present (case-insensitive) AND no `must_not_contain` is present
  - Persist `eval_runs` row, return `Report{Total, Passed, Score, Items: []Item}`
- `internal/eval/scorecard.go` — pretty-print a Report to an `io.Writer` (used by both the CLI subcommand and the admin UI)
- New ops CLI subcommand: extend `cmd/everychat-ops` with `eval --bot-id=<id> --questions=<path>`
- Example golden-questions in `tests/eval/steuerkanzlei-demo.yaml` (12–15 questions)
- Seed fixture in `internal/storage/migrations/002_phase2.sql` (or a one-shot `make seed-demo` target): insert one tenant + one bot named `steuerkanzlei-demo` with a hand-written tiny system prompt. This is the demo bot the validation gate runs against from Sprint 3 onward — without it, sprints 3–5 have nothing to evaluate.
- Tests in `internal/eval/runner_test.go` with a mock `llm.Chat` — cover all-pass, all-fail, partial pass, malformed YAML, empty answer

### Acceptance
- `./bin/everychat-ops eval --bot-id=1 --questions=tests/eval/steuerkanzlei-demo.yaml` prints a scorecard like:
  ```
  Eval: steuerkanzlei-demo  (15 questions)
    PASS  q1-leistungen
    FAIL  q4-honorar — missing keyword "Stundensatz"
    ...
  Score: 12/15 (0.80) — below threshold 0.85
  ```
- `eval_runs` row persisted; re-running produces a second row (history preserved)
- Eval tests pass with mock LLM
- Migration `002_phase2` applied idempotently

### Out of Scope
- LLM-as-judge scoring (Phase 4 — DACH-Goldfragen-Korpus has fuzzy semantic checks; Phase 2 is keyword-only)
- HTML report rendering (the admin UI gets a plain table in Sprint 6)

---

## Sprint 3: sqlite-vec + KB Chunk Storage Layer

Vector storage and retrieval, ready for the crawler (Sprint 4) to fill it.

### Tasks
- Vendor the `sqlite-vec` extension binary for Linux/amd64, Linux/arm64, and Darwin/arm64 into `internal/storage/vec/` (build from upstream tarball; ship as `.so`/`.dylib`). Add a `make vec-extension` target documenting the build command for fresh checkouts.
- Migration `internal/storage/migrations/003_vec.sql`:
  - `CREATE VIRTUAL TABLE kb_vec USING vec0(rowid INTEGER PRIMARY KEY, embedding FLOAT[1024])` (or 1536 — pick once based on Sprint 1's embedding choice and lock it in via a `EVERYCHAT_EMBED_DIMS` constant)
  - Trigger to keep `kb_vec` in sync with `kb_chunks` deletes (insert path is explicit from Go)
- `internal/storage/sqlite.go` — load `sqlite-vec` at `Open` time via `db.LoadExtension(...)`. Path resolved from `EVERYCHAT_VEC_EXT_PATH` env var with a sensible default per OS.
- `internal/storage/kb.go` — typed helpers:
  - `InsertChunk(ctx, BotID, source, chunkIndex, content, embedding []float32) (chunkID int64, err error)`
  - `SearchChunks(ctx, BotID, queryEmbedding []float32, k int) ([]Chunk, error)` — vec0 KNN with `WHERE bot_id = ?`
  - `DeleteChunksForBot(ctx, BotID) error` (used by re-crawl)
- Tests in `internal/storage/kb_test.go` — insert 5 chunks with deterministic toy embeddings, query, assert top-k ordering

### Acceptance
- `go test ./internal/storage/...` passes including KNN ordering test
- `everychat` boot logs confirm `sqlite-vec extension loaded` (one line, version included)
- Re-running migrations is idempotent (003_vec applied once)
- A 1000-chunk synthetic benchmark in the test file completes a top-10 search in < 50 ms on the dev laptop (informational, not a hard gate)

### Out of Scope
- Hybrid search (BM25 + vector) — Phase 4
- Per-bot embedding-model overrides — Phase 5+

---

## Sprint 4: Domain Crawler + Markdown Ingestion → Embedded Chunks

Fill `kb_chunks` (and `kb_vec`) from a customer's public website.

### Tasks
- `internal/crawler/crawler.go` — Colly-based:
  - Respect `robots.txt`, single-host scope, max-depth = 3, rate limit 1 req/sec, max 200 pages
  - Seed: homepage + `/sitemap.xml` (parsed if present)
  - Filter for likely-content paths: `/`, `/about`, `/ueber-uns`, `/leistungen`, `/services`, `/pricing`, `/preise`, `/faq`, `/kontakt`, `/impressum`
  - Skip binaries, images, PDFs (Phase 3 reconsideration)
  - Output: stream of `(url, html, fetched_at)` to a channel
- `internal/ingest/markdown.go`:
  - HTML→Markdown via `html-to-markdown/v2` with a config that strips `<nav>`, `<footer>`, `<script>`, `<style>`, cookie banners
  - Markdown chunking: split on H1/H2 headings, max 800 tokens (~3200 chars), overlap 100 chars
  - Returns `[]Chunk{Source, Index, Content}`
- `internal/ingest/pipeline.go` — `Ingest(ctx, BotID, domain string) error`:
  1. Crawl → 2. Convert/chunk → 3. Embed (batched, 16/request) → 4. `storage.InsertChunk` per chunk → 5. Audit log entry
- New ops CLI subcommand: `everychat-ops crawl --domain=<d> --bot-id=<id>` — emits a progress log
- Tests:
  - `crawler_test.go` — `httptest.NewServer` serving a small fixture site; assert correct URL set is fetched
  - `markdown_test.go` — fixture HTML → expected Markdown → expected chunk count and boundaries
  - `pipeline_test.go` — full pipeline with mocked LLM embedder, assert chunks land in DB and are vector-searchable

### Acceptance
- `./bin/everychat-ops crawl --domain=https://www.kanzlei-mustermann.de --bot-id=1` (against a real demo site) completes in < 90 s and produces ≥ 20 chunks
- `SearchChunks` over the freshly ingested data returns plausible top-3 hits for the query "Welche Leistungen?"
- All crawler/ingest tests pass without network access
- Re-running the crawl on the same bot deletes-then-replaces chunks (no duplicates)

### Out of Scope
- PDF / DOCX upload — Phase 3
- Manual Markdown upload via UI — Phase 3 (CLI works for demos)
- Incremental re-crawl (delta only) — Phase 5

---

## Sprint 5: System-Prompt Generator + Prompt Editor UI

Claude drafts the system prompt from the crawled KB; the founder edits and saves a draft separately from the published prompt.

### Tasks
- `internal/prompt/generator.go`:
  - `Draft(ctx, BotID) (string, error)` — pulls top representative chunks (`SearchChunks` with a fixed "what does this company do?" probe), assembles a meta-prompt that asks Claude to produce a German-language system prompt for a customer-service chatbot, returns the result
  - Meta-prompt template embedded as `prompt/template_de.tmpl`; covers tone, scope, refusal patterns, `{{ .CompanyName }}` / `{{ .DomainSummary }}` placeholders
- Admin UI additions (`internal/web/`):
  - `GET /admin/bots/{id}/prompt` — split-pane editor: left = published, right = draft, both `<textarea>` (HTMX `hx-post` on Save Draft and Promote-to-Published)
  - `POST /admin/bots/{id}/prompt/draft` — saves to `bots.draft_prompt`
  - `POST /admin/bots/{id}/prompt/generate` — calls `prompt.Draft`, fills the right pane (HTMX swap)
  - `POST /admin/bots/{id}/prompt/publish` — copies draft → `system_prompt`, sets `bots.published_at`. **Rejected by the publish-gate middleware (Sprint 6) until evals + URLs OK.**
- New templates: `internal/web/templates/prompt_editor.html`, partial `prompt_panel.html` for HTMX swaps
- Replace the placeholder `htmx.min.js` with the real 2.0.x release (vendored)
- Tests:
  - `prompt/generator_test.go` — mock LLM + mock storage; assert template variables render and Claude is called with the expected meta-prompt
  - `web/prompt_editor_test.go` — `httptest` for each handler; CSRF still enforced

### Acceptance
- Founder navigates to `/admin/bots/1/prompt`, clicks **Generate**, draft pane fills with a multi-paragraph German prompt within 15 s
- Edits in the draft pane survive page reload (persisted)
- Clicking **Publish** without passing evals → user-visible error "Publish gate: evals failing" (the message comes from Sprint 6 middleware; Sprint 5 just hits the route)
- All prompt + web tests pass

### Out of Scope
- Versioned prompt history (a single draft + a single published is fine for Phase 2)
- Branching prompts per locale — Phase 4 (DACH-Goldfragen-Korpus may demand DE/AT/CH variants)

---

## Sprint 6: Bot Wizard + Test Sandbox + Publish Gate + Eval CLI

Tie everything together. After this sprint, the Phase 2 goal is met end-to-end.

### Tasks
- Bot wizard (admin UI):
  - `GET /admin/bots/new` — 3-step wizard (HTMX-driven, single page): name + domain → crawl progress (server-sent log lines via `text/event-stream` from `internal/ingest/pipeline.Ingest`) → first-draft prompt + redirect to `/admin/bots/{id}/prompt`
  - Persists a new `bots` row at step 1
- Test sandbox:
  - `GET /admin/bots/{id}/sandbox` — chat UI; messages stream via SSE from a new endpoint `POST /admin/bots/{id}/sandbox/messages`
  - The streaming handler does RAG: embed the user message → `SearchChunks(top=5)` → assemble a context block → `llm.Chat.Stream` with `bot.draft_prompt` (or `system_prompt` if no draft) + retrieved chunks
  - Persists `chats` + `messages` rows scoped to a synthetic `visitor_id="founder-sandbox"` so eval data doesn't pollute production analytics
  - Token accounting: capture `tokens_in/out` from LiteLLM response and write to `messages`
- Publish gate:
  - `internal/web/publish.go` — middleware/helper that, on `POST .../publish`, runs the latest `eval_runs` row for the bot AND checks `privacy_policy_url`/`agb_url` are non-null. If either fails, return a structured error rendered into the prompt-editor panel. No override.
  - `POST /admin/bots/{id}/evals/run` — kicks off an eval, persists an `eval_runs` row, returns the scorecard
- Eval CLI in the main binary (not just ops): `cmd/everychat/main.go` accepts a subcommand path: `everychat eval --bot-id=<id> --questions=<path>` runs the same code path as the ops CLI and exits non-zero if score < threshold. (Keeps the public CLI surface for Phase 6 small but real.)
- README update: full Phase 2 walkthrough at the bottom — `make compose` → set keys → wizard → publish

### Acceptance — full Phase 2 walkthrough
1. `git pull`, set `ANTHROPIC_API_KEY` + `VOYAGE_API_KEY` in `.env`, `make compose`
2. Log in via magic link (Phase 1 flow still works)
3. **Create your first bot** → wizard runs the crawler against a real demo site, finishes in < 90 s, lands on the prompt editor with a Claude-drafted German prompt visible in the draft pane
4. Click **Test in sandbox**, ask "Welche Leistungen bietet die Kanzlei?" — answer streams in token by token, references retrieved chunks
5. Author a 15-question YAML at `tests/eval/<bot>.yaml`, click **Run evals** → scorecard renders in the panel
6. Click **Publish** → blocked because `privacy_policy_url` is empty; fill it + AGB URL on the bot details page → click **Publish** again, success, `bots.published_at` set
7. Run `./bin/everychat eval --bot-id=1 --questions=tests/eval/<bot>.yaml` from the host — same scorecard, exit 0 because score ≥ 0.85

### Out of Scope
- Multiple-bot-per-tenant UI (schema permits, UI doesn't expose)
- Self-service customer login — Phase 5 (only the founder logs in during Phase 2)
- Production monitoring dashboards — Phase 5

---

## Final Phase 2 Acceptance

After Sprint 6, the founder can complete the 7-step walkthrough above without manual intervention. Every step is automated through the admin UI; the CLI is a parity surface, not a fallback.

This is the deliverable. Phase 3 (Embed + lead capture + DSGVO baseline) starts when the founder reviews and approves Phase 2 results.

## Notes for the Build Agent

- **Eval-first discipline:** every sprint after Sprint 2 must register a passing eval against the demo bot before `lazy yolo advance`. If a sprint's changes degrade the score below the previous high-water mark, that sprint fails and must be fixed before advancing — even if its own acceptance criteria pass in isolation.
- **No secrets in commits:** `ANTHROPIC_API_KEY`, `VOYAGE_API_KEY`, `OPENAI_API_KEY` live in `.env` only. The LiteLLM container reads them from compose env. CI never sees them; tests use mocks.
- **Solo-founder time discipline:** every sprint must produce something demoable on its own slice; no undemoed code accumulates across sprints. If a sprint can't demo without a future sprint's work, the slicing is wrong — restructure first.
- **Streaming responses:** prefer Server-Sent Events (SSE) over WebSockets — Caddy proxies SSE cleanly with no config, and HTMX has first-class SSE support via `hx-ext="sse"`. WebSockets are out of scope until visitor traffic in Phase 3.
- **Branch + commits:** all work stays on a new `phase-2-bot-authoring` branch (cut from `phase-1-foundation`). Push to `origin` after every meaningful unit; one commit per sprint minimum, conventional-commit style (`feat(eval): add scorecard renderer`).
- **Tests:** every package gets table-driven tests for happy path + at least 2 failure modes. LLM/embedding tests use mocks — never real API calls in CI.
- **Cost discipline:** the LLM-as-judge pattern is *explicitly out of scope* for Phase 2 because token cost balloons with eval-on-every-commit. Keep evals keyword-based until Phase 4 introduces the goldfragen corpus.
