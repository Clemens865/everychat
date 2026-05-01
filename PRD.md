# Everychat — Phase 1 (Foundation) PRD

**Source-of-truth for tonight's autonomous build.** Phase 1 of the 6-phase v1 roadmap defined in `docs/blueprints/everychat-managed-blueprint.md`. Subsequent phases are explicitly OUT OF SCOPE.

## Project Context

**Everychat Managed** — EU-sovereign LLM chatbot product for DACH SMB / Mittelstand. Single-tenant Go binary architecture (one VM per customer), sold as managed-hosted SaaS. Default LLM is Anthropic Claude. POC runs locally in Docker Compose; production target is Hetzner Cloud (and later Yorizon when GPU SKUs land).

Read `docs/blueprints/everychat-managed-blueprint.md` for full architectural context.

## Tech Stack (locked)

- **Language:** Go 1.22
- **Storage:** SQLite 3.45 (with `sqlite-vec` to be added in Phase 2; not in Phase 1)
- **HTTP framework:** `net/http` + `chi` router (no heavy framework)
- **Admin UI:** HTMX + Alpine.js, embedded in the Go binary via `embed.FS`
- **Reverse proxy / TLS:** Caddy 2.8 (mkcert for local dev TLS)
- **LLM (deferred to Phase 2 — not used in Phase 1):** Anthropic Claude via `github.com/anthropics/anthropic-sdk-go`
- **Email (Phase 1, mocked):** Postmark (real integration deferred to Phase 5; Phase 1 uses an interface with a stdout-printing impl)
- **Provisioning (Phase 1, stub only):** Hetzner Cloud API client `github.com/hetznercloud/hcloud-go/v2` — STUB IMPLEMENTATION, no real API calls

## Phase 1 Goal

A founder can run `make dev` locally and reach an admin login page over HTTPS at `https://everychat.local`, log in via a magic-link printed to the terminal, see an empty admin dashboard ("Create your first bot"), and run `everychat-ops provision --dry-run` to preview what creating a customer instance would do (without actually calling Hetzner).

## Out of Scope for Phase 1

- ❌ Any LLM integration (no Anthropic SDK calls — Phase 2)
- ❌ RAG / sqlite-vec / embeddings (Phase 2)
- ❌ Crawler, MD ingestion, prompt generation (Phase 2)
- ❌ Eval harness / golden questions (Phase 2)
- ❌ iframe widget / embed.js (Phase 3)
- ❌ Lead capture / HubSpot Forms / webhooks (Phase 3)
- ❌ DSGVO erasure cascade (Phase 3 — minimum is schema TODOs only)
- ❌ Llama Guard / moderation (Phase 5)
- ❌ Mollie billing (Phase 5)
- ❌ Real Postmark calls — interface only with stdout impl
- ❌ Real Hetzner provisioning — stub only with `--dry-run` flag
- ❌ Multi-customer ops control plane logic (Phase 5)
- ❌ Anything from Phases 2–6

## Repo Conventions

- `cmd/everychat/` — main HTTP server binary
- `cmd/everychat-ops/` — provisioning CLI binary
- `internal/storage/` — SQLite layer, schema, migrations
- `internal/auth/` — magic-link auth, signed tokens
- `internal/email/` — email sender interface + stdout impl
- `internal/web/` — HTTP handlers, embedded HTMX templates
- `web/templates/` — Go html/template files (embedded via `//go:embed`)
- `web/static/` — CSS, JS (htmx.min.js, alpine.min.js), embedded
- `migrations/` — numbered SQL files (`001_init.sql`, etc.)
- `Caddyfile` — repo root, dev TLS config
- `Makefile` — `make dev`, `make test`, `make build`, `make lint`
- `docker-compose.yml` — repo root, 2-container POC

## Validation Gate (applies to every sprint)

- `go vet ./...` passes
- `go test ./...` passes
- `golangci-lint run` passes (config: `.golangci.yml` with sensible defaults — gofmt, govet, staticcheck, errcheck, gosec)
- No secrets in committed files (Lazy `secure` audit)
- Sprint's acceptance criteria demonstrably met

---

## Sprint 1: Go Module Skeleton + Makefile + CI Baseline

Bootstrap the Go module, repository scaffolding, and developer workflow tooling.

### Tasks
- Initialize Go module `github.com/everychat/everychat` (or `github.com/clemenshoenig/everychat` — pick a name and stay consistent)
- Create `cmd/everychat/main.go` with a minimal `net/http` server bound to `:8080` returning "ok" at `/healthz`
- Create `cmd/everychat-ops/main.go` with a single `version` subcommand printing the binary version
- `Makefile` with targets: `dev` (run with hot reload via `air` or simple `go run`), `test`, `build`, `lint`, `clean`
- `.golangci.yml` — enable gofmt, govet, staticcheck, errcheck, gosec, ineffassign, unused
- `.gitignore` covering `.env`, build artifacts, `tmp/`, `.lazy/`, `node_modules/` (none expected, but defensive)
- `README.md` at repo root with: project intro, prerequisites, quickstart (`make dev`), repo layout
- `LICENSE` file — MIT (per blueprint Section 6 — open-source CLI is MIT; main binary licensing TBD but MIT is fine for POC)

### Acceptance
- `go build ./...` succeeds
- `make test` runs (even with zero tests — exits 0)
- `make lint` runs and passes (clean code or no warnings)
- `curl http://localhost:8080/healthz` returns `ok` after `make dev`
- `./everychat-ops version` prints something
- README quickstart accurately describes how to get running

### Out of Scope
- Caddy / TLS (Sprint 3)
- SQLite (Sprint 2)
- Auth (Sprint 4)

---

## Sprint 2: SQLite Storage Layer + Schema + Migrations

Establish the persistence layer with a versioned migration system.

### Tasks
- Add dependency `github.com/mattn/go-sqlite3` (CGo) — accept the CGo cost; SQLite is the storage layer for the lifetime of the product
- Create `internal/storage/sqlite.go` — `Open(dsn string) (*sql.DB, error)` opens DB, runs migrations
- Create `internal/storage/migrations.go` — embed `migrations/*.sql` via `//go:embed`, run in order, track applied versions in `_schema_migrations` table
- Create `migrations/001_init.sql` with these tables (matches blueprint):
  - `tenants` (id, name, domain, created_at, status) — single row in single-tenant runtime; the table exists for ops-control-plane queries later
  - `bots` (id, tenant_id, name, system_prompt, draft_prompt, status, privacy_policy_url, agb_url, retention_days, created_at, updated_at, published_at)
  - `chats` (id, bot_id, visitor_id, started_at, ended_at, lang)
  - `messages` (id, chat_id, role, content, tokens_in, tokens_out, created_at) — `role` is `user|assistant|system|tool`
  - `kb_chunks` (id, bot_id, source, chunk_index, content, embedding BLOB, created_at) — `embedding` column reserved for Phase 2; null in Phase 1
  - `leads` (id, chat_id, email, summary, captured_at, hubspot_form_submitted_at, webhook_delivered_at)
  - `magic_links` (id, email, token_hash, created_at, expires_at, used_at)
  - `sessions` (id, user_email, created_at, expires_at, token_hash)
  - `audit_log` (id, actor, action, target_type, target_id, payload_json, created_at) — DSGVO accountability
- Add a `internal/storage/storage_test.go` — opens an in-memory DB, runs migrations, inserts/queries one row per table
- Wire DB open into `cmd/everychat/main.go` — DSN from env var `EVERYCHAT_DB_PATH` (default: `./data/everychat.db`)
- `data/` directory git-ignored

### Acceptance
- `go test ./internal/storage/...` passes
- Starting `everychat` creates `./data/everychat.db` if missing and applies migrations
- Re-running `everychat` is idempotent (no duplicate migration runs)
- All 9 tables exist after first run (verify via test)
- `_schema_migrations` table tracks `001_init` as applied

### Out of Scope
- `sqlite-vec` extension (Phase 2 — schema reserves the `embedding BLOB` column only)
- Real data — empty DB is correct for Phase 1

---

## Sprint 3: Caddy + mkcert TLS for `everychat.local`

Local HTTPS via mkcert + Caddy reverse-proxying to the Go binary.

### Tasks
- Document mkcert install + `mkcert -install` + `mkcert everychat.local "*.everychat.local"` in README
- `Caddyfile` at repo root:
  - `everychat.local` block reverse-proxies to `127.0.0.1:8080`
  - Uses `tls ./certs/everychat.local.pem ./certs/everychat.local-key.pem`
  - `encode gzip` enabled
  - Sensible security headers (`X-Content-Type-Options`, `Referrer-Policy`, `Strict-Transport-Security` once HTTPS confirmed)
- Add `/etc/hosts` entry note in README: `127.0.0.1 everychat.local`
- `make dev` updated: starts Go binary AND Caddy concurrently (or use `caddy run --config Caddyfile` as a separate make target `make caddy`, documenting that both are needed)
- Update `/healthz` test in README to use `https://everychat.local/healthz`

### Acceptance
- `curl --cacert <mkcert-root-ca> https://everychat.local/healthz` returns `ok`
- Browser navigation to `https://everychat.local/healthz` shows green padlock (mkcert CA trusted by OS)
- README clearly explains mkcert setup for a fresh machine

### Out of Scope
- Production TLS via Let's Encrypt (Phase 5)
- ACME automation (Phase 5)

---

## Sprint 4: Magic-Link Auth + Sessions + Stdout Email

Founder can log into the admin UI via magic link printed to terminal.

### Tasks
- `internal/email/email.go` — interface `Sender` with method `Send(ctx, to, subject, body) error`
- `internal/email/stdout.go` — implementation that prints to stdout (used in dev; replaced with Postmark client in Phase 5)
- `internal/auth/magiclink.go`:
  - `RequestLink(email string)` — generates random 256-bit token, stores `sha256(token)` in `magic_links` table with 15-min expiry, calls `Sender.Send()` with link `https://everychat.local/auth/verify?t=<token>`
  - `VerifyLink(token string) (email string, err error)` — looks up by hash, validates not expired and not used, marks `used_at`, returns email
- `internal/auth/session.go`:
  - `CreateSession(email string) (cookieValue string)` — generates 256-bit random token, stores `sha256(token)` in `sessions` with 30-day expiry, returns the cookie value
  - `RequireSession(http.Handler) http.Handler` middleware — extracts cookie, verifies, sets `email` in request context; redirects to `/login` if missing
- HTTP handlers in `internal/web/auth.go`:
  - `GET /login` — simple HTML form with email input
  - `POST /login` — calls `RequestLink`, shows "check your terminal" page (in dev) — Postmark TODO comment for prod
  - `GET /auth/verify?t=...` — calls `VerifyLink`, on success calls `CreateSession`, sets HttpOnly Secure SameSite=Lax cookie, redirects to `/admin`
- HTTP handler in `internal/web/admin.go`:
  - `GET /admin` — protected by `RequireSession`, renders empty admin shell template "Welcome <email> — Create your first bot" with HTMX boilerplate ready for Phase 2
- Wire all routes in `cmd/everychat/main.go`
- Tests: `internal/auth/magiclink_test.go` (request → verify happy path, expired token, replay attack), `internal/auth/session_test.go` (create → require → expire)

### Acceptance
- `make dev` running, navigate to `https://everychat.local/login`, submit your email
- Terminal prints the magic-link URL
- Click that URL → redirected to `/admin` showing "Welcome <email>"
- Restart browser (clear cookie) → `/admin` redirects to `/login`
- Reusing a magic link → 401 / redirect to login with error
- Expired magic link (>15 min) → 401 / redirect to login with error
- All auth tests pass

### Out of Scope
- Postmark real integration (Phase 5)
- Multi-user / team seats (Phase 5)
- Password auth, OAuth, SSO (never — magic link only)

---

## Sprint 5: Hetzner Cloud Provisioner Stub

CLI binary that simulates customer-instance provisioning without making real API calls.

### Tasks
- Add `github.com/hetznercloud/hcloud-go/v2` dependency
- `cmd/everychat-ops/main.go` extended with subcommands using `flag` package or a small CLI lib like `urfave/cli/v2`:
  - `version` (already from Sprint 1)
  - `provision --domain <d> --owner-email <e> [--dry-run]` — STUB: prints what would happen ("Would create CCX13 in eu-central, install everychat-bin v0.1.0, configure Caddy for {domain}, send magic-link to {owner-email}"). With `--dry-run` prints the same. Without `--dry-run`, ALSO prints "[STUB] No real API calls made — set EVERYCHAT_HETZNER_LIVE=1 in Phase 5 to enable."
  - `list` — STUB: prints "No instances yet (stubbed)"
- `internal/provisioner/hetzner.go` — wraps the hcloud client behind an interface `Provisioner` with method `Provision(ctx, opts) (*Instance, error)`. The default impl is `StubProvisioner` (logs intent only). A `LiveProvisioner` skeleton exists but returns `errors.New("live provisioning disabled until Phase 5")` if invoked.
- Tests: `internal/provisioner/hetzner_test.go` — verify StubProvisioner returns expected canned response, LiveProvisioner returns the expected error.

### Acceptance
- `everychat-ops provision --domain bot.test.de --owner-email me@me.de --dry-run` prints a clear human-readable plan
- Without `--dry-run`, prints the same plan plus the "[STUB] No real API calls" notice
- No real Hetzner API calls happen (verify via test or by running offline)
- Provisioner test suite passes

### Out of Scope
- Real Hetzner API integration (Phase 5)
- VM lifecycle management beyond `provision` (Phase 5)

---

## Sprint 6: Docker Compose 2-Container POC + Final Wire-Up

Whole stack boots via `docker-compose up`. Memory footprint < 500MB. Boots in seconds.

### Tasks
- `Dockerfile` for `everychat`:
  - Multi-stage: builder stage uses `golang:1.22-bookworm` (CGo for SQLite), runtime stage uses `gcr.io/distroless/cc-debian12` or `debian:bookworm-slim`
  - Final image runs `/app/everychat`, exposes 8080
- `Dockerfile.caddy` (or use upstream `caddy:2.8-alpine` directly with mounted Caddyfile)
- `docker-compose.yml` at repo root:
  - Service `everychat` — built from `Dockerfile`, mounts `./data:/app/data`, env `EVERYCHAT_DB_PATH=/app/data/everychat.db`, exposes 8080 internally only
  - Service `caddy` — image `caddy:2.8-alpine`, mounts `./Caddyfile:/etc/caddy/Caddyfile:ro`, mounts `./certs:/certs:ro` for mkcert PEMs, ports `443:443` `80:80`
  - Network so caddy reaches everychat as `everychat:8080`
- `.env.example` at repo root with documented variables (`EVERYCHAT_DB_PATH`, future Anthropic/Postmark/Mollie placeholders all commented)
- Update `Makefile` with `make compose` and `make compose-down`
- Update README with full quickstart: `make compose` → mkcert install → browse `https://everychat.local`
- Final smoke-test script: `scripts/smoke.sh` — boots compose, hits `/healthz`, hits `/login`, asserts 200s, brings stack down

### Acceptance
- Fresh checkout: `mkcert -install && mkcert everychat.local && make compose` → working stack
- `https://everychat.local/login` reachable from host browser with valid TLS
- `scripts/smoke.sh` passes
- Compose process memory total < 500MB (`docker stats`)
- Compose boot time (post-image-pull) < 10 seconds
- README quickstart followable by a fresh developer with no prior context

### Out of Scope
- Production Docker (Hetzner deploy) — Phase 5
- LiteLLM container (Phase 5)
- Llama Guard container (Phase 5)
- Hot-reload in compose (dev mode is `make dev` outside compose; compose is the integration test target)

---

## Final Phase 1 Acceptance

After Sprint 6, the founder can:

1. `git clone` the repo
2. Install mkcert and run `mkcert -install && mkcert everychat.local`
3. Run `make compose`
4. Navigate to `https://everychat.local/login` in a browser, see green padlock
5. Submit their email, see the magic link in `docker compose logs everychat`
6. Click the link, land on `/admin` showing "Welcome <email> — Create your first bot"
7. Run `./bin/everychat-ops provision --domain bot.example.de --owner-email demo@example.de --dry-run` and see a coherent provisioning plan

This is the deliverable. Phase 2 (Bot Authoring Core) starts when the founder reviews and approves Phase 1 results.

## Notes for the Build Agent

- **Solo-founder time discipline:** every sprint should produce something demoable; don't accumulate undemoed code across sprints
- **Security:** all session/auth tokens hashed (sha256) at rest; cookies HttpOnly + Secure + SameSite=Lax; CSRF token on the login POST form
- **No hardcoded secrets** — fail fast at startup if a required env var is missing
- **Go conventions:** `gofmt`, package docs, exported names get doc comments
- **Tests:** prefer table-driven tests; minimum coverage is "happy path + 2 failure modes per package"
- **Commits:** one commit per sprint at minimum; conventional-commits style (`feat(auth): add magic-link verification`)
- **Branch:** all work stays on `phase-1-foundation` branch; do NOT push to remote
