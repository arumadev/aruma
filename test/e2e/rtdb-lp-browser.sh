#!/usr/bin/env bash
# The long-poll trap (ADR-020 §4) against this checkout's
# Aruma: the official web SDK in Chromium (rtdb-lp-browser/run.mjs), with the database
# enabled and dev_open_rules on loopback. It checks that a page reconnects after the server
# was down for 10 s, comes back over long polling, upgrades to a WebSocket again and clears
# the localStorage flag; and that with localStorage blocked, and WebSockets refused as well,
# long polling carries the data.
#
#   test/e2e/rtdb-lp-browser.sh
#
# Run `npm ci` in test/e2e/rtdb-lp-browser once; Playwright's Chromium must be
# installed (npx playwright-core install chromium). Needs Go, Node, curl, openssl, psql and
# the local Postgres.
#
# Environment:
#   RTDB_LP_PORT            the server's loopback port (default 9481); the page uses the next
#   RTDB_LP_DB_PREFIX       prefix of the throwaway database (default aruma_rtdblp_)
#   ARUMA_TEST_DATABASE_URL Postgres admin URL; ARUMA_PG_BIN the directory of psql
set -euo pipefail

exp=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$exp/../.." && pwd)
[[ -d $exp/rtdb-lp-browser/node_modules/firebase ]] || { echo "run npm ci in $exp/rtdb-lp-browser first" >&2; exit 3; }
psql=${ARUMA_PG_BIN:+$ARUMA_PG_BIN/}psql
admin=${ARUMA_TEST_DATABASE_URL:-postgres://postgres@127.0.0.1:5432/postgres?sslmode=disable}
port=${RTDB_LP_PORT:-9481}
base=http://127.0.0.1:$port
db=${RTDB_LP_DB_PREFIX:-aruma_rtdblp_}$(openssl rand -hex 4)

work=$(mktemp -d "${TMPDIR:-/tmp}/aruma-rtdb-lp.XXXXXX")
pids=()
cleanup() {
  for p in "${pids[@]}"; do kill "$p" 2>/dev/null || true; done
  wait 2>/dev/null || true
  "$psql" -q "$admin" -c "DROP DATABASE IF EXISTS $db WITH (FORCE)" >/dev/null || true
  rm -rf "$work"
}
trap cleanup EXIT INT TERM

(cd "$root" && go build -o "$work/aruma" ./cmd/aruma)
"$psql" -q "$admin" -c "CREATE DATABASE $db" >/dev/null
export ARUMA_DATABASE_URL="${admin%%\?*}" ARUMA_PUBLIC_URL=$base ARUMA_LISTEN=127.0.0.1:$port \
  ARUMA_MASTER_KEY=$(openssl rand -base64 32) ARUMA_METRICS_LISTEN=- ARUMA_LOG_LEVEL=warn \
  ARUMA_RTDB_ENABLED=true ARUMA_RTDB_DEV_OPEN_RULES=true
export ARUMA_DATABASE_URL="${ARUMA_DATABASE_URL%/*}/$db?sslmode=disable"
"$work/aruma" migrate up >/dev/null
password=$(openssl rand -hex 16)
printf '%s\n' "$password" | "$work/aruma" accounts create --email owner@lp.test >/dev/null

# A project, created as the dashboard does: sign in, then POST with the CSRF token.
"$work/aruma" serve >>"$work/aruma.log" 2>&1 &
server=$!
pids+=("$server")
for _ in $(seq 1 100); do curl -fs "$base/healthz" >/dev/null && break; sleep 0.1; done
login=$(curl -fsS -D "$work/login.headers" -H 'Content-Type: application/json' \
  -d "{\"email\":\"owner@lp.test\",\"password\":\"$password\"}" "$base/console/v1/auth:login")
cookie=$(grep -i '^set-cookie:' "$work/login.headers" | head -1 | sed -E 's/^[^:]*: *//; s/;.*//' | tr -d '\r')
csrf=$(printf '%s' "$login" | sed -E 's/.*"csrfToken":"([^"]+)".*/\1/')
curl -fsS -H "Cookie: $cookie" -H "X-Aruma-CSRF: $csrf" -H 'Content-Type: application/json' \
  -d '{"projectId":"lp-browser","displayName":"Long poll"}' "$base/console/v1/projects" >/dev/null
kill -TERM "$server"
wait "$server" || true

# run.mjs starts and stops the server itself.
ARUMA_BIN=$work/aruma BASE=$base NS=lp-browser-default-rtdb PROJECT_ID=lp-browser PAGE_PORT=$((port + 1)) \
  node "$exp/rtdb-lp-browser/run.mjs" 2>>"$work/aruma.log" || { status=$?; echo "server log:" >&2; tail -40 "$work/aruma.log" >&2; exit $status; }
