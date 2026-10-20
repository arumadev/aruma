#!/usr/bin/env bash
# A Realtime Database client must survive a server restart: serve this checkout's Aruma,
# connect the JS SDK (rtdb-restart/client.cjs), stop the server with SIGTERM (its graceful
# shutdown), restart it on the same database, and check that the client reconnects and that
# a write it queued while the server was down lands. A shutdown that sent the s control
# message failed this: the SDK stops reconnecting for good (PersistentConnection.ts:876-882).
#
#   test/experiments/rtdb-restart.sh
#
# Uses the @firebase packages the RTDB compat harness installs
# (test/compat/firebase-js/run.sh rtdb, at least once). Needs Go, Node, curl, openssl, psql
# and the local Postgres.
#
# Environment:
#   RTDB_RESTART_PORT       the server's loopback port (default 9480)
#   RTDB_RESTART_DB_PREFIX  prefix of the throwaway database (default aruma_rtdbrestart_)
#   ARUMA_CACHE_DIR         where the compat harness installed (default .cache of the main checkout)
#   ARUMA_TEST_DATABASE_URL Postgres admin URL; ARUMA_PG_BIN the directory of psql
set -euo pipefail

exp=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$exp/../.." && pwd)
main_root=$(dirname "$(git -C "$root" rev-parse --path-format=absolute --git-common-dir)")
cache=${ARUMA_CACHE_DIR:-$main_root/.cache}
modules=$cache/upstream/rtdb/firebase-js-sdk/node_modules
[[ -d $modules/@firebase/database ]] || { echo "run test/compat/firebase-js/run.sh rtdb once to install the SDK" >&2; exit 3; }
psql=${ARUMA_PG_BIN:+$ARUMA_PG_BIN/}psql
admin=${ARUMA_TEST_DATABASE_URL:-postgres://postgres@127.0.0.1:5432/postgres?sslmode=disable}
port=${RTDB_RESTART_PORT:-9480}
base=http://127.0.0.1:$port
db=${RTDB_RESTART_DB_PREFIX:-aruma_rtdbrestart_}$(openssl rand -hex 4)

work=$(mktemp -d "${TMPDIR:-/tmp}/aruma-rtdb-restart.XXXXXX")
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
printf '%s\n' "$password" | "$work/aruma" accounts create --email owner@restart.test >/dev/null

serve() {
  "$work/aruma" serve >>"$work/aruma.log" 2>&1 &
  server=$!
  pids+=("$server")
  for _ in $(seq 1 100); do curl -fs "$base/healthz" >/dev/null && return; sleep 0.1; done
  echo "Aruma did not start:" >&2
  cat "$work/aruma.log" >&2
  exit 3
}
serve

# A project, created as the dashboard does: sign in, then POST with the CSRF token.
login=$(curl -fsS -D "$work/login.headers" -H 'Content-Type: application/json' \
  -d "{\"email\":\"owner@restart.test\",\"password\":\"$password\"}" "$base/console/v1/auth:login")
cookie=$(grep -i '^set-cookie:' "$work/login.headers" | head -1 | sed -E 's/^[^:]*: *//; s/;.*//' | tr -d '\r')
csrf=$(printf '%s' "$login" | sed -E 's/.*"csrfToken":"([^"]+)".*/\1/')
curl -fsS -H "Cookie: $cookie" -H "X-Aruma-CSRF: $csrf" -H 'Content-Type: application/json' \
  -d '{"projectId":"restart-test","displayName":"Restart"}' "$base/console/v1/projects" >/dev/null

NODE_PATH=$modules node "$exp/rtdb-restart/client.cjs" "$base" restart-test-default-rtdb >"$work/client.log" 2>&1 &
client=$!
pids+=("$client")
for _ in $(seq 1 100); do grep -q '"step":"ready"' "$work/client.log" && break; sleep 0.1; done
echo "==> stopping Aruma gracefully (SIGTERM)"
kill -TERM "$server"
wait "$server" || true
sleep 3
echo "==> restarting it"
serve
status=0
wait "$client" || status=$?
cat "$work/client.log"
exit "$status"
