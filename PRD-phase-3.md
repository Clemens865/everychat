# Everychat — Phase 3 (Embed + Lead Capture + DSGVO Baseline) PRD

**Source-of-truth for Phase 3's autonomous build.** Phase 3 of the 6-phase v1 roadmap defined in `docs/blueprints/everychat-managed-blueprint.md`. Phase 2 (Bot Authoring Core) shipped on `phase-2-bot-authoring`; this PRD picks up directly from `phase-2-complete`. Phases 4–6 are explicitly OUT OF SCOPE.

## Project Context

**Everychat Managed** — EU-sovereign LLM chatbot product for DACH SMB / Mittelstand. Phase 2 made the founder able to author + test + publish a bot inside the admin UI. **Phase 3 makes the published bot actually run on a customer's website** — the chat artifact leaves the editor and lives as an embeddable widget that visitors interact with, captures leads, and erases personal data on request.

**Phase 3 in one sentence:** the founder can paste a `<script>` snippet into a customer's site (Webflow, plain HTML, anything), visitors get a bubble launcher, the chat streams answers + retrieved sources, lead intents trigger an inline capture artifact that posts to HubSpot Forms + an owner-configured webhook, and the founder can erase any visitor's data with one click — all under DSGVO retention rules.

**Widget is template-based, not LLM-generated.** Per Phase-2 design discussion: the widget code itself is a small, vetted set of templates. The LLM only generates *theme defaults* (accent color sampled from the customer's site, welcome message, starter prompts) which the founder can edit. This keeps the security/audit/version surface controllable for a DACH compliance audience.

Read `docs/blueprints/everychat-managed-blueprint.md` §3.5 (visitor-message data flow), §3.6 (lead capture), and §4 Phase 3 for full architectural context.

## Tech Stack (additions on top of Phase 2)

- **Widget runtime:** vanilla TypeScript + Web Components (`<everychat-launcher>`), no framework. Bundled to a single `embed.js` (~10 KB gz target) served from `/embed.js`.
- **Iframe sandbox:** `sandbox="allow-scripts allow-forms allow-popups"` + strict CSP (`default-src 'self' https://everychat.local; connect-src 'self' wss://everychat.local`). No third-party origins reachable from inside the iframe.
- **HTML sanitization:** `DOMPurify` (vendored, locked version) for assistant-rendered Markdown inside the widget; server-side `bluemonday` strict policy as belt-and-braces.
- **Markdown rendering:** `goldmark` server-side → sanitized HTML → into the streaming SSE payload.
- **Moderation:** Llama Guard 3 8B via the LiteLLM sidecar (CPU quant — Q4_K_M GGUF — Phase 3; GPU lift in Phase 5). Input check + output check on the streamed completion.
- **Lead capture:** `internal/integrations/webhook.go` (SSRF guard: block private IP ranges, RFC 6890 reserved space, AWS/GCP metadata endpoints; no following redirects). HubSpot Forms is **deferred to Phase 5** — Phase 3 ships webhook-only delivery; the dispatcher is structured so a HubSpot adapter is a drop-in addition later.
- **Email:** the Phase-1 stdout `Sender` impl stays for the entire Phase 3. Postmark integration is **deferred to Phase 5**. Production lead summaries land in the founder's `docker compose logs everychat` output until then; the interface is stable so the Postmark swap is a one-file change.
- **DSGVO scheduler:** `internal/retention/` — daily sweep over `chats.ended_at + bot.retention_days`; cascade-deletes via foreign keys + manual kb_vec cleanup.
- **Build:** widget TS compiled via `esbuild` (vendored binary; no Node runtime required at server time). Output committed to `internal/widget/dist/embed.js`.

## Phase 3 Goal

A founder can:

1. Open a published bot in the editor → click **"Einbettungs-Code kopieren"** → paste the resulting `<script src="https://everychat.local/embed.js" data-bot="<id>"></script>` snippet on any HTML page.
2. Visitors see the configured launcher (bubble bottom-right or inline block, per template choice in settings) → open it → chat with full RAG + token streaming, identical to the Phase-2 sandbox.
3. The bot detects lead intent → an inline **Lead-Capture-Artefakt** appears in the conversation with name + email fields → submit posts to HubSpot Forms + webhook (owner-configured) + persists to `leads` table; founder sees the lead in the dashboard.
4. The founder receives an email with the conversation summary (Postmark in production, stdout sender in `make dev`).
5. Visit `/admin/visitors/<visitor_id>` → click **"Daten löschen (DSGVO)"** → all chats/messages/leads/kb_chunks rows for that visitor are erased (audit-logged); cascade is verified via integration test.
6. The retention scheduler runs daily and deletes any chat older than the bot's `retention_days` setting; founder can see the next sweep timestamp in admin settings.

No real visitor traffic at scale, no multi-customer ops control plane — those land in Phase 5.

## Out of Scope for Phase 3

- ❌ Multi-customer ops control plane / fleet view (Phase 5)
- ❌ Mollie billing (Phase 5)
- ❌ Real Hetzner provisioning (Phase 5 — stub stays from Phase 1)
- ❌ Languages other than DE + EN (Phase 4 brings AT/CH locale)
- ❌ DACH-Goldfragen-Korpus (Phase 4)
- ❌ GPU-served Llama Guard (Phase 5 — CPU-quant in Phase 3)
- ❌ LLM-generated widget code (replaced by template + AI-default-slots hybrid; locked)
- ❌ Rich-media uploads in chat beyond text + PDF reference (Phase 5)
- ❌ A/B widget testing or analytics dashboards (Phase 5)
- ❌ HubSpot Forms integration — webhook delivery covers Phase 3; HubSpot adapter ships in Phase 5
- ❌ Postmark transactional email — stdout sender stays for Phase 3; Postmark swap ships in Phase 5
- ❌ Anything from Phases 4–6

## Repo Conventions (additions)

- `internal/widget/` — Go package that serves `embed.js`, the iframe shell HTML, and template assets. `dist/` holds the bundled JS (committed; build is reproducible via `make widget`).
- `internal/widget/templates/` — the two widget variants (`bubble.html`, `inline.html`) as Go html/template files; HTML is rendered server-side per visit, themed via CSS custom properties.
- `internal/widget/src/` — TypeScript source for `embed.js` (loader) and the in-iframe runtime (`runtime.ts`).
- `internal/api/` — versioned visitor-facing endpoints: `POST /api/v1/chat`, `POST /api/v1/lead`, `GET /api/v1/widget/{bot}/config`. Distinct from admin routes — no session cookie, signed bot-token instead.
- `internal/moderation/` — Llama Guard 3 client (calls LiteLLM with the `guard` model alias).
- `internal/lead/` — intent detection (Claude tool-use), summary generator, dispatcher.
- `internal/integrations/webhook.go` — outbound webhook delivery with retry queue + SSRF guard. HubSpot adapter (Phase 5) plugs into the same dispatcher.
- `internal/email/` — Phase-1 stdout `Sender` stays. Postmark impl deferred to Phase 5.
- `internal/retention/` — DSGVO scheduler.
- `internal/storage/migrations/004_phase3.sql` — adds `widget_template`, `widget_theme_json`, `webhook_url`, `webhook_secret_hash` to `bots`; `lead_dispatch` table for retry queue. (`hubspot_form_guid` column lands with the Phase 5 HubSpot adapter.)
- `tests/integration/embed_e2e_test.go` — Playwright-driven end-to-end against a real Webflow-style fixture page.

## Validation Gate (applies to every sprint)

- `go vet ./...` passes
- `go test ./...` passes
- `golangci-lint run` passes (Phase 1 config still applies)
- **Eval gate**: `everychat eval --bot-name=steuerkanzlei-demo --questions=tests/eval/steuerkanzlei-demo.yaml` reports score ≥ 0.85
- **Security gate** (Phase 3 specific): `lazy secure` audit reports no new critical/high findings; manual checks for XSS via the widget input + SSRF via the webhook target + Llama Guard refuses prompt-injection attempts in the `tests/adversarial/` corpus
- No secrets in committed files (`.env` git-ignored)
- Sprint's acceptance criteria demonstrably met

---

## Sprint 1: Visitor-facing chat API + bot-token auth

Carve out the visitor surface from the admin surface. Phase 2's sandbox endpoint (`POST /admin/bots/{id}/sandbox`) is admin-cookie-gated; we now need a parallel `POST /api/v1/chat` that visitors hit from the widget — no admin cookie, signed bot-token instead.

### Tasks
- Migration `internal/storage/migrations/004_phase3.sql`:
  - `bots.widget_template TEXT NOT NULL DEFAULT 'bubble'` (`bubble` | `inline`)
  - `bots.widget_theme_json TEXT NOT NULL DEFAULT '{}'` — accent, position, welcome, starter_prompts, locale
  - `bots.embed_token_hash TEXT` — sha256 of the public bot-token used by the widget
  - `bots.embed_origin_allow TEXT NOT NULL DEFAULT ''` — CSV of allowed origins (e.g. `https://kanzlei-mustermann.de`); empty = any (dev only)
  - `lead_dispatch (id, lead_id, target, attempts, last_error, scheduled_at, delivered_at)` for the retry queue
- `internal/api/chat.go` — `POST /api/v1/chat` handler. Body: `{bot_token, message, visitor_id?, session_id?}`. Validates `Origin` against `embed_origin_allow`. Rate-limits per IP (sliding window, 30 req / minute). SSE response identical to Phase 2 sandbox.
- `internal/api/widget_config.go` — `GET /api/v1/widget/{bot_token}/config`. Returns the public theme + welcome + starter prompts JSON. Does NOT include the system prompt or KB chunks.
- `internal/api/embed_js.go` — serves `/embed.js` (the bundled loader, see Sprint 2). For Sprint 1 a placeholder that just `console.error`s "embed.js not built yet".
- `internal/auth/embed.go` — `IssueEmbedToken(botID)` generates a 256-bit token, stores `sha256(token)` on the bot row, returns the cleartext for one-time display in the admin UI. `LookupBotByEmbedToken` reverses it.
- Admin UI: in the editor settings rail, add a **"Einbettung"** group with the bot-token (regenerate button), origin allow-list editor, and a copy-to-clipboard `<script>` snippet preview.
- Tests:
  - `internal/api/chat_test.go` — happy path (valid token + allowed origin → SSE stream), invalid token → 401, disallowed origin → 403, rate-limit → 429
  - `internal/auth/embed_test.go` — issue → lookup round-trip, replay safety (only the hash is stored)

### Acceptance
- `curl -H "Origin: https://allowed.test" -d '{"bot_token":"<t>","message":"hi"}' https://everychat.local/api/v1/chat` streams the same SSE shape as the Phase-2 sandbox.
- Same call with a different `Origin` header → 403.
- `GET /api/v1/widget/<token>/config` returns `{template, accent, welcome, starter_prompts, locale}` and never leaks `system_prompt` or `draft_prompt`.
- All Sprint-1 tests pass.

### Out of Scope
- Llama Guard (Sprint 5)
- Markdown sanitization (Sprint 2 — once the widget renders responses)
- Cross-customer rate-limit isolation — Phase 3 ships per-IP global limits; per-bot quotas land in Phase 5

---

## Sprint 2: Widget runtime + iframe shell + bubble template

The visitor-facing artifact comes alive. Vanilla TS + Web Components, single bundled `embed.js`.

### Tasks
- `internal/widget/src/embed.ts` — the loader. Parses `data-bot=` from the host script tag, fetches `/api/v1/widget/{token}/config`, injects an iframe pointing at `/widget/{token}/shell` with the configured launcher position. Defines `<everychat-launcher>` Web Component on the host page (closed shadow DOM so the customer's CSS can't break it).
- `internal/widget/src/runtime.ts` — runs INSIDE the iframe. Renders the bubble template, handles open/close, calls `POST /api/v1/chat` with SSE, renders streamed tokens, renders Markdown via DOMPurify-sanitized goldmark output.
- `internal/widget/templates/bubble.html` — the bubble template (server-rendered with theme tokens injected as CSS custom properties on `:root`).
- `internal/widget/serve.go` — `GET /widget/{token}/shell` returns the iframe HTML with strict CSP headers; `GET /embed.js` serves the bundled output from `dist/embed.js`.
- `Makefile` target `make widget` — runs `esbuild internal/widget/src/embed.ts --bundle --minify --format=iife --outfile=internal/widget/dist/embed.js`. Vendor an `esbuild` binary into `tools/` so a clean checkout can build without npm.
- `tests/integration/embed_e2e_test.go` — Playwright spins up a static fixture page that includes the embed snippet, opens the widget, sends "Hallo", asserts a streamed reply appears.
- Update the editor's "Live-Vorschau" column 3: replace the Phase-2 inline mock widget with a real iframe pointing at `/widget/<bot_token>/shell` so the founder sees the *actual* widget code, not a mock.

### Acceptance
- `make widget` produces `internal/widget/dist/embed.js` (≤ 25 KB pre-gzip).
- A static page at `tests/integration/fixture.html` with `<script src="/embed.js" data-bot="<token>"></script>` renders the bubble launcher; clicking it opens the chat.
- `make compose` + opening that fixture page → real Claude streams into the bubble.
- The editor's Live-Vorschau renders the same iframe so changes to settings (accent color, welcome) preview live.
- CSP header on `/widget/.../shell` is strict and verified via test (`Content-Security-Policy: default-src 'self'; …`).
- Playwright e2e test passes.

### Out of Scope
- Inline template (Sprint 3)
- Lead-capture artifact rendering (Sprint 5)
- Theme suggestion from KB (Sprint 4)

---

## Sprint 3: Inline template variant + theme-token system

Ship the second template and prove the theme-token system holds across both.

### Tasks
- `internal/widget/templates/inline.html` — inline-block variant (mounts wherever the host page places `<everychat-inline data-bot="…">`). Same conversation UI, no launcher/close button, no fixed position.
- `internal/widget/theme.go` — typed `Theme` struct (Accent, Position, Radius, Locale, Welcome, StarterPrompts) with a `ToCSSVars()` method emitting `--w-accent: …; --w-radius: …;` etc. Unmarshalled from `bots.widget_theme_json`.
- Editor settings rail: extend the existing **Verhalten** group from Phase 2 to include a Template radio (`Bubble` / `Inline`), an Accent color picker, and (for `inline`) an "Einbettungs-CSS-Selektor" hint field.
- The Live-Vorschau column 3 now also renders the customer-page mockup with the chosen template — `bubble` floats bottom-right, `inline` renders inside a service-card slot in the mockup.
- Tests: theme-token round-trip (Theme → JSON → DB → Theme), template-switch in editor flips the iframe `src` correctly, both templates pass the same Playwright "send a message, receive a reply" test.

### Acceptance
- Switching the template in the rail re-renders the Live-Vorschau within ~1 s (no full page reload).
- Both templates handle a 20-message conversation without layout breaking.
- Theme tokens drive both templates from the same `widget_theme_json` row.
- All Sprint-3 tests pass.

### Out of Scope
- More than two templates — Phase 5 if customers ask
- Custom CSS upload — never (security boundary)

---

## Sprint 4: AI-generated theme defaults from KB

The hybrid the user requested. Widget code stays a vetted template; *defaults* for the slots come from Claude during the wizard's crawl step.

### Tasks
- Extend `internal/prompt/` (or a new `internal/widget/themer/`) with `SuggestTheme(ctx, BotID) (Theme, error)`:
  - Pulls top representative chunks via `storage.SearchChunks` (same probe as `prompt.Draft`)
  - Asks Claude (separate meta-prompt template `theme_de.tmpl`): given these excerpts, suggest (a) a hex accent color appropriate for the brand voice, (b) a one-line German welcome message in the configured tone, (c) 3 starter prompts the bot will obviously answer well
  - Returns a `Theme` struct; persisted to `bots.widget_theme_json` via `storage.UpdateBotTheme`
- Wizard step 3: after the prompt is drafted, also call `SuggestTheme` and show the proposed accent + welcome + starter prompts as editable fields (founder can override before clicking "Zum Editor").
- Editor settings rail: a small **"Vorschläge generieren"** button next to the Accent / Welcome / Starter Prompts fields that re-runs `SuggestTheme` (founder-triggered, idempotent).
- Tests: mock LLM returns a fixture Theme JSON; assert it round-trips through `SuggestTheme` → DB → Live-Vorschau iframe.

### Acceptance
- New-bot wizard against a fresh customer site produces a coherent default theme without manual intervention; founder can still override.
- Re-clicking "Vorschläge generieren" against a populated KB returns plausible alternatives.
- The Live-Vorschau reflects suggested theme within 1 s of acceptance.
- Eval gate (steuerkanzlei-demo eval) still ≥ 0.85.

### Out of Scope
- Theme A/B testing (Phase 5)
- Sampling colors from the actual customer site DOM via Playwright (interesting but Phase 5)

---

## Sprint 5: Lead capture + Llama Guard moderation

The two security-sensitive sprints land together because they share the moderation pipeline.

### Tasks
- `internal/moderation/llama_guard.go` — wraps a LiteLLM `guard` model alias (`meta-llama/Llama-Guard-3-8B` quantized GGUF). `Check(ctx, role, content) (allowed bool, category string, err error)`. Two call sites: visitor input pre-LLM, assistant output post-stream.
- `internal/llm/litellm/config.yaml` — add the `guard` model. Document the GGUF download path.
- `internal/lead/intent.go` — Claude tool-use: pass a `lead_handoff` tool to the chat completion; when the model calls it, surface the artifact card. Extracts `name?`, `email?`, `summary` arguments from the tool call.
- `internal/lead/dispatcher.go` — on artifact submit:
  - Persist to `leads` table (existing schema from Phase 1 migration 001)
  - Generate a 3–5 line summary via a cheap follow-up LLM call (`claude-haiku` model alias if available, otherwise reuse `claude-sonnet`)
  - Enqueue webhook POST to `lead_dispatch`; a goroutine processes the queue with exponential backoff. (HubSpot Forms adapter slots into the same queue in Phase 5.)
  - Send the founder a lead summary via the existing stdout `Sender` (Postmark swap is Phase 5).
- `internal/integrations/webhook.go` — POSTs to the bot's `webhook_url` with a `lead.created` payload signed by `webhook_secret_hash` (HMAC-SHA-256). **SSRF guard**: pre-flight checks the resolved IP is not in 10/8, 172.16/12, 192.168/16, 169.254/16, ::1, fd00::/8; rejects redirects; 5-second connect timeout, 10-second total timeout.
- Widget runtime: when the SSE stream returns a `lead_capture` event, the runtime renders the inline artifact card from a vetted template (name + email + textarea + submit). Submit calls `POST /api/v1/lead`.
- DOMPurify integration (deferred from Sprint 2): assistant-rendered Markdown passes through DOMPurify with a strict allow-list (`p, ul, ol, li, strong, em, code, pre, a[href]`); server also runs the same content through `bluemonday` strict policy as a backstop.
- `tests/adversarial/` — small corpus of prompt-injection probes Llama Guard must refuse (the "ignore previous instructions" / "DAN" family, plus DACH-specific exfiltration attempts). Asserted via integration test.

### Acceptance
- Asking the bot "Ich brauche einen Termin" (or any clear lead-intent variant) produces an inline lead-capture card; submitting it persists a `leads` row + delivers to a webhook fixture endpoint with a valid HMAC-SHA-256 signature.
- Founder sees the lead summary in `docker compose logs everychat` (stdout sender). Postmark swap is a Phase 5 follow-up.
- Llama Guard refuses every prompt in `tests/adversarial/` with the right category label; the visitor sees a polite refusal message.
- SSRF probe (`webhook_url=http://169.254.169.254/`) → blocked + audit-logged.
- DOMPurify strips a `<script>` payload smuggled into a chunk → unit test asserts.
- Eval gate still ≥ 0.85.

### Out of Scope
- Multi-step lead-capture flows (just a single artifact card in Phase 3)
- HubSpot Forms adapter — Phase 5 (the dispatcher's queue interface stays
  HubSpot-ready so the swap is a one-file addition)
- Postmark transactional email — Phase 5 (stdout sender stays)
- Salesforce / Pipedrive — never in v1
- LLM-as-judge for lead quality (Phase 4 corpus territory)

---

## Sprint 6: DSGVO baseline + retention scheduler + erasure cascade

DSGVO compliance is non-negotiable for DACH Mittelstand. This is the security audit's first line of questioning.

### Tasks
- `internal/storage/migrations/005_phase3_dsgvo.sql`:
  - Add `chats.visitor_email TEXT` (NULL until lead-capture)
  - Add `bots.dsgvo_doc_url TEXT` — link to the customer's DSGVO documentation, surfaced in the widget consent line
  - `INSERT INTO _retention_runs ...` table for sweep audit trail
- `internal/retention/sweeper.go` — daily background job (`time.Ticker` 24h):
  - For each bot, find chats older than `bot.retention_days` and delete them; trigger cascade deletes leads, messages
  - Manually delete `kb_chunks` entries older than retention if they're flagged transient (Phase 5 — for now KB chunks live as long as the bot)
  - Audit-log the run to `audit_log` + `_retention_runs`
- `internal/web/dsgvo.go`:
  - `GET /admin/visitors/{visitor_id}` — show every chat + lead + message for a visitor across all bots in this tenant
  - `POST /admin/visitors/{visitor_id}/erase` — delete-cascade everything for that visitor; audit-log the request with the founder's email; require typing the visitor_id as confirmation; return an erasure receipt PDF (HTML→PDF via Chromedp could be Phase 5; Phase 3 ships an HTML receipt)
- `internal/api/visitor_export.go` — `GET /api/v1/visitor/{id}/export?token=<one-time>` — visitor-initiated DSGVO data export (the founder generates a one-time token from the admin UI and forwards it).
- Widget consent gate: if `bot.privacy_policy_url` is set AND `bot.embed_origin_allow` is non-empty, the first widget open shows a one-screen consent ("Ich stimme der Verarbeitung gemäß Datenschutzhinweisen zu") that must be checked before the composer activates. Phase-2 banner-line consent stays as a fallback.
- Bot publish-gate hardening (extends Phase 2): publish now also requires `bot.embed_origin_allow` non-empty AND a verified `dsgvo_doc_url`.
- Tests:
  - `internal/retention/sweeper_test.go` — sweep deletes only past-retention rows; audit-log contains the run
  - `internal/web/dsgvo_test.go` — `/erase` endpoint cascades to chats / messages / leads / audit_log entries that reference the visitor; idempotent on second call
  - Property test: after `/erase`, no SELECT * across all tables returns the visitor_id (modulo audit_log entries that record the erasure itself, which legally must persist)

### Acceptance
- Founder navigates to `/admin/visitors/<id>` → sees the visitor's full record → clicks Erase → confirms → all rows except the erasure audit entry are gone.
- The retention sweep deletes a chat created with `started_at = now() - 91 days` for a bot with `retention_days=90`.
- Visitor export endpoint returns a JSON document containing every retained record for the visitor.
- Widget shows consent gate on first open; visitor cannot send a message without checking.
- Publish-gate refuses without `embed_origin_allow` or `dsgvo_doc_url`.
- All Sprint-6 tests pass; security audit (`lazy secure`) reports no new findings.

### Out of Scope
- PDF receipts (Phase 5)
- Visitor-initiated rectification UI (Phase 5; Phase 3 ships erase + export only)

---

## Final Phase 3 Acceptance

After Sprint 6, the founder can dogfood the bot on one real customer site for 2 weeks and:

1. Paste the embed snippet on Webflow / Wix / plain HTML → widget appears.
2. Visitors chat, get streamed RAG-grounded answers, see retrieved sources, hit the lead-capture artifact, submit.
3. Leads land in the configured webhook (HMAC-signed) + the admin dashboard + stdout-logged for the founder. (HubSpot + Postmark land in Phase 5.)
4. Llama Guard refuses adversarial input + assistant outputs that violate policy.
5. The founder erases any visitor's data on request via `/admin/visitors/<id>/erase`.
6. Daily retention sweep auto-deletes chats past their bot's retention_days.

This is the deliverable. Phase 4 (DACH-Goldfragen-Korpus) starts when the founder reviews + approves Phase 3 results — and ideally has 10+ real captured leads in the DB to point at.

## Notes for the Build Agent

- **Security-first sprint discipline:** Phase 3 is rated complexity-L in the blueprint specifically because this is where attacks land. After every sprint, run `lazy secure`; treat any new high/critical finding as a sprint-failure even if functional acceptance passes.
- **Eval gate stays alive:** every sprint must keep the seeded `steuerkanzlei-demo` eval ≥ 0.85. If a Llama Guard tweak in Sprint 5 starts refusing legitimate questions and tanks the score, the change is wrong, not the eval.
- **No third-party JS in the widget runtime.** DOMPurify is the only vendored dep; everything else is hand-rolled. The CSP guarantees the iframe can't load anything else even if a templating bug tries.
- **Origin enforcement is not optional.** `embed_origin_allow` empty is a dev-only mode; production publishes require non-empty entries.
- **Branch + commits:** all Phase-3 work on a new `phase-3-embed` branch cut from `phase-2-complete`. Push to origin after every meaningful unit; one commit per sprint minimum, conventional-commit style (`feat(p3-sprint-N): …`).
- **Don't bypass the gate.** The publish-gate ladder gains a third door in Sprint 6 (`embed_origin_allow` + `dsgvo_doc_url`); do not add an override flag, even for the founder. Phase 2 explicitly locked "no override" and Phase 3 inherits that.
- **Tests:** every package gets table-driven tests + at least 2 failure modes; security-sensitive paths (SSRF, XSS, HMAC) get adversarial tests with malicious inputs.
- **Cost discipline:** Llama Guard runs on every visitor turn. CPU-quant in Phase 3 keeps it cheap; if latency or quality degrade, the answer is GPU lift in Phase 5, not removing the moderation step.
