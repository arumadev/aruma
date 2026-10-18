#!/usr/bin/env bash
# Runs the web Auth end-to-end tests (AC-A1, X-4, X-6): a Vite app on the official firebase
# package (11, 12 and 13), configured only with the web config Aruma's Management API serves
# plus the one connectAuthEmulator line (ADR-013), driven by Playwright against a real Aruma
# server on a throwaway database, with every request off this machine blocked.
#
#   test/e2e/web-auth/run.sh [playwright test arguments]
#
# It needs the local Postgres (ARUMA_TEST_DATABASE_URL), Go and Node.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../../.." && pwd)
gen=$here/.generated
cd "$root"

log() { printf '\n==> %s\n' "$*"; }

log "starting a fresh Aruma server"
rm -rf "$gen"
mkdir -p "$gen"
go build -o "$gen/genconfig" ./test/smoke/genconfig
"$gen/genconfig" -out "$gen" -serve -anonymous >"$gen/server.env" 2>"$gen/server.log" &
server=$!
# SIGTERM makes genconfig stop the server and drop its database.
trap 'kill "$server" 2>/dev/null; wait "$server" 2>/dev/null || true' EXIT
for _ in $(seq 1 240); do
  grep -q ARUMA_CLI_TOKEN "$gen/server.env" 2>/dev/null && break
  kill -0 "$server" 2>/dev/null || { cat "$gen/server.log" >&2; exit 1; }
  sleep 0.25
done
# shellcheck disable=SC1091
. "$gen/server.env"

log "fetching the web config from the Management API"
app_id=$(node -p "require('$gen/expected.json').web.appId")
curl -fsS -H "Authorization: Bearer $ARUMA_CLI_TOKEN" \
  "$ARUMA_URL/v1beta1/projects/$(node -p "require('$gen/expected.json').projectId")/webApps/$app_id/config" \
  >"$gen/firebase-config.json"

log "web Auth e2e: firebase 11, 12, 13 against $ARUMA_URL"
cd "$here"
npm ci --no-audit --no-fund --silent
npx playwright install chromium >/dev/null
ARUMA_URL=$ARUMA_URL ARUMA_E2E_DIR=$gen npx playwright test "$@"
