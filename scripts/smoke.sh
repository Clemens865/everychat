#!/usr/bin/env bash
# scripts/smoke.sh — POC smoke test against the docker-compose stack.
# Boots the stack, hits the public endpoints, and tears down.

set -euo pipefail

REPO_ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

CA_BUNDLE="${MKCERT_CAROOT:-$(mkcert -CAROOT 2>/dev/null || true)}/rootCA.pem"
HOST="everychat.local"
HEALTHZ="https://${HOST}/healthz"
LOGIN="https://${HOST}/login"

echo "▶ booting docker compose…"
docker compose up --build -d

cleanup() {
  echo
  echo "▶ tearing down docker compose…"
  docker compose down
}
trap cleanup EXIT

echo "▶ waiting for healthz…"
for i in {1..30}; do
  if curl --fail --silent --resolve "${HOST}:443:127.0.0.1" \
        --cacert "$CA_BUNDLE" "$HEALTHZ" >/dev/null 2>&1; then
    echo "  reachable after ${i} attempt(s)"
    break
  fi
  sleep 1
  if [[ "$i" == 30 ]]; then
    echo "✗ healthz never came up" >&2
    docker compose logs --tail=80 || true
    exit 1
  fi
done

echo "▶ /healthz returns 200 + 'ok'"
body="$(curl --silent --resolve "${HOST}:443:127.0.0.1" --cacert "$CA_BUNDLE" "$HEALTHZ")"
[[ "$body" == "ok" ]] || { echo "✗ unexpected body: $body" >&2; exit 1; }

echo "▶ /login returns 200"
status="$(curl -s -o /dev/null -w "%{http_code}" --resolve "${HOST}:443:127.0.0.1" \
            --cacert "$CA_BUNDLE" "$LOGIN")"
[[ "$status" == "200" ]] || { echo "✗ /login status $status" >&2; exit 1; }

echo
echo "✔ smoke test passed"
