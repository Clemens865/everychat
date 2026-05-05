# Everychat

EU-sovereign LLM chatbot product for DACH SMB / Mittelstand. Single-tenant Go binary architecture (one VM per customer), sold as managed-hosted SaaS.

This repository implements **Phase 1 (Foundation)** as defined in [`PRD.md`](PRD.md). Subsequent phases (LLM integration, RAG, embed widget, billing, etc.) are out of scope here.

## Prerequisites

- Go 1.22+ (`brew install go`)
- [`golangci-lint`](https://golangci-lint.run/) (`brew install golangci-lint`) — for `make lint`
- [`mkcert`](https://github.com/FiloSottile/mkcert) (`brew install mkcert`) — for local HTTPS certs (Sprint 3+)
- [`caddy`](https://caddyserver.com/) (`brew install caddy`) — for local TLS termination (Sprint 3+)
- Docker (24+) with `docker compose` — for the full POC (Sprint 6)

## Quickstart (single binary, plain HTTP)

```bash
make dev
```

In another terminal:

```bash
curl http://localhost:8080/healthz
# -> ok
```

## Quickstart (full stack with HTTPS)

1. Install the mkcert local CA so your OS trusts the certs we'll generate
   (one-time per machine — needs sudo on macOS to add to the system keychain):

   ```bash
   sudo mkcert -install
   ```

2. Generate certs for `everychat.local` (filenames are referenced verbatim by
   the Caddyfile, so don't rename them):

   ```bash
   mkdir -p certs
   cd certs && mkcert everychat.local "*.everychat.local" && cd ..
   # produces: certs/everychat.local+1.pem and certs/everychat.local+1-key.pem
   ```

3. Add a hosts entry so the browser resolves `everychat.local`:

   ```
   127.0.0.1 everychat.local
   ```

   On macOS / Linux this lives in `/etc/hosts` (requires sudo).

4. Boot the stack (two terminals):

   ```bash
   make dev   # terminal 1: Go server on :8080
   make caddy # terminal 2: Caddy reverse proxy on :443
   ```

5. Verify:

   ```bash
   curl https://everychat.local/healthz
   # -> ok
   ```

   In a browser, `https://everychat.local/healthz` should show a green padlock
   once the mkcert CA is installed.

## Quickstart (Docker Compose POC — Sprint 6)

```bash
make compose
```

Then browse to `https://everychat.local/login`.

Tear down with `make compose-down`.

## Make targets

| Target            | What it does                                  |
| ----------------- | --------------------------------------------- |
| `make dev`        | Run the `everychat` HTTP server               |
| `make test`       | Run all tests                                 |
| `make build`      | Compile `everychat` and `everychat-ops` into `./bin` |
| `make lint`       | Run `golangci-lint`                           |
| `make caddy`      | Run Caddy reverse proxy                       |
| `make compose`    | Bring up the docker-compose stack             |
| `make compose-down` | Tear down docker-compose                    |
| `make smoke`      | Run the smoke test (`scripts/smoke.sh`)       |
| `make check-corpus` | Validate every embedded industry corpus (schema + holdout-disjointness) |
| `make clean`      | Remove build artifacts                        |

## Repository layout

```
cmd/everychat/         HTTP server binary entrypoint
cmd/everychat-ops/     Provisioning CLI binary entrypoint
internal/storage/      SQLite layer, schema, migrations
internal/auth/         Magic-link auth, signed sessions
internal/email/        Email Sender interface + stdout impl
internal/web/          HTTP handlers, embedded HTMX templates
internal/provisioner/  Hetzner Cloud client (stub in Phase 1)
internal/web/templates/   Go html/template files (embedded into binary)
internal/web/static/      CSS, JS (HTMX, Alpine) (embedded into binary)
internal/storage/migrations/  Numbered SQL migration files (embedded into binary)
scripts/               Helper scripts (smoke test, etc.)
docs/                  Architecture, blueprints, validation logs
Caddyfile              Local-dev TLS reverse proxy config
docker-compose.yml     Two-container POC stack
Dockerfile             Multi-stage build for the everychat binary
```

## CLI: `everychat-ops`

```bash
./bin/everychat-ops version
# -> everychat-ops 0.1.0-dev
```

More subcommands (provisioning) arrive in Sprint 5.

## License

MIT — see [`LICENSE`](LICENSE).
