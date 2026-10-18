#!/usr/bin/env bash
# Runs the web Auth end-to-end tests (AC-A1, X-4, X-6): a Vite app on the official firebase
# package (11, 12 and 13), configured only with the web config Aruma's Management API serves
# plus the one connectAuthEmulator line (ADR-013), driven by Playwright against a real Aruma
# server on a throwaway database, with every request off this machine blocked.
#
#   test/e2e/web-auth/run.sh [playwright test arguments]
#
# ARUMA_E2E_MODE picks how the app reaches Aruma (vite.config.mjs):
#   emulator (default)  connectAuthEmulator, the one line of ADR-013.
#   production          no connectAuthEmulator: the SDK stays in production mode and @aruma/web's
#                       Vite plugin redirects its Auth requests (sdk/web/aruma-web).
#   production-build    the same plugin in mode 'build': the host literals are rewritten in the build.
#
# It needs the local Postgres (ARUMA_TEST_DATABASE_URL), Go and Node.
set -euo pipefail

mode=${ARUMA_E2E_MODE:-emulator}
case "$mode" in emulator|production|production-build) ;; *) echo "unknown ARUMA_E2E_MODE $mode" >&2; exit 2 ;; esac
export ARUMA_E2E_MODE=$mode

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../../.." && pwd)
gen=$here/.generated
cd "$root"

log() { printf '\n==> %s\n' "$*"; }

server=
stop() {
  # SIGTERM makes genconfig stop the server and drop its database.
  if [[ -n "$server" ]]; then
    kill "$server" 2>/dev/null
    wait "$server" 2>/dev/null || true
  fi
  server=
}
trap stop EXIT

# start runs a fresh Aruma server and writes the web config the Management API serves.
start() {
  rm -rf "$gen"
  mkdir -p "$gen"
  go build -o "$gen/genconfig" ./test/smoke/genconfig
  "$gen/genconfig" -out "$gen" -serve -anonymous >"$gen/server.env" 2>"$gen/server.log" &
  server=$!
  for _ in $(seq 1 240); do
    grep -q ARUMA_CLI_TOKEN "$gen/server.env" 2>/dev/null && break
    kill -0 "$server" 2>/dev/null || { cat "$gen/server.log" >&2; exit 1; }
    sleep 0.25
  done
  # shellcheck disable=SC1091
  . "$gen/server.env"
  local project app_id
  project=$(node -p "require('$gen/expected.json').projectId")
  app_id=$(node -p "require('$gen/expected.json').web.appId")
  curl -fsS -H "Authorization: Bearer $ARUMA_CLI_TOKEN" \
    "$ARUMA_URL/v1beta1/projects/$project/webApps/$app_id/config" >"$gen/firebase-config.json"
}

(cd "$here" && npm ci --no-audit --no-fund --silent)
# On CI the browser needs its system libraries too.
(cd "$here" && npx playwright install ${CI:+--with-deps} chromium >/dev/null)

# One server per firebase major version: every request comes from 127.0.0.1, and Aruma's
# per-address limits are sized for one app, not three in a row.
for major in 11 12 13; do
  log "web Auth e2e ($mode mode): firebase $major against a fresh Aruma server"
  start
  (cd "$here" && ARUMA_URL=$ARUMA_URL ARUMA_E2E_DIR=$gen npx playwright test --project "firebase-$major" "$@")
  stop
done
