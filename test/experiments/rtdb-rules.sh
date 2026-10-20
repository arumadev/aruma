#!/usr/bin/env bash
# Security rules with real users, through the official web SDK: serve this checkout's Aruma
# with the Realtime Database under per-user rules (no dev_open_rules), sign a user up with
# Aruma Auth from the `firebase` package (rtdb-rules/client.mjs), check that the rules let it
# read and write its own data and nothing else, then disable the user from the console, as an
# admin would, and check that the client loses access as a Firebase client does: its listen is
# cancelled with permission_denied, the SDK's token refresh fails and Auth signs the user out,
# and its next write is denied.
#
#   test/experiments/rtdb-rules.sh
#
# Run `npm ci` in test/experiments/rtdb-rules once. Needs Go, Node, curl, openssl, psql and
# the local Postgres.
#
# Environment:
#   RTDB_RULES_PORT         the server's loopback port (default 9482)
#   RTDB_RULES_DB_PREFIX    prefix of the throwaway database (default aruma_rtdbrules_)
#   ARUMA_TEST_DATABASE_URL Postgres admin URL; ARUMA_PG_BIN the directory of psql
set -euo pipefail

exp=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$exp/../.." && pwd)
[[ -d $exp/rtdb-rules/node_modules/firebase ]] || { echo "run npm ci in $exp/rtdb-rules first" >&2; exit 3; }
psql=${ARUMA_PG_BIN:+$ARUMA_PG_BIN/}psql
admin=${ARUMA_TEST_DATABASE_URL:-postgres://postgres@127.0.0.1:5432/postgres?sslmode=disable}
port=${RTDB_RULES_PORT:-9482}
base=http://127.0.0.1:$port
db=${RTDB_RULES_DB_PREFIX:-aruma_rtdbrules_}$(openssl rand -hex 4)
project=rules-test

work=$(mktemp -d "${TMPDIR:-/tmp}/aruma-rtdb-rules.XXXXXX")
pids=()
cleanup() {
  for p in ${pids[@]+"${pids[@]}"}; do kill "$p" 2>/dev/null || true; done
  wait 2>/dev/null || true
  "$psql" -q "$admin" -c "DROP DATABASE IF EXISTS $db WITH (FORCE)" >/dev/null || true
  rm -rf "$work"
}
trap cleanup EXIT INT TERM

(cd "$root" && go build -o "$work/aruma" ./cmd/aruma)
"$psql" -q "$admin" -c "CREATE DATABASE $db" >/dev/null
export ARUMA_DATABASE_URL="${admin%%\?*}" ARUMA_PUBLIC_URL=$base ARUMA_LISTEN=127.0.0.1:$port \
  ARUMA_MASTER_KEY=$(openssl rand -base64 32) ARUMA_METRICS_LISTEN=- ARUMA_LOG_LEVEL=warn \
  ARUMA_RTDB_ENABLED=true ARUMA_MAIL_DEV_MAILBOX=true
export ARUMA_DATABASE_URL="${ARUMA_DATABASE_URL%/*}/$db?sslmode=disable"
"$work/aruma" migrate up >/dev/null
password=$(openssl rand -hex 16)
printf '%s\n' "$password" | "$work/aruma" accounts create --email owner@rules.test >/dev/null
"$work/aruma" serve >>"$work/aruma.log" 2>&1 &
pids+=($!)
for _ in $(seq 1 100); do curl -fs "$base/healthz" >/dev/null && break; sleep 0.1; done

json() { node -e 'let s = ""; process.stdin.on("data", d => s += d).on("end", () => console.log(JSON.parse(s)[process.argv[1]]))' "$1"; }

# The owner's CLI token, as the dashboard issues one; the console API and the rules endpoint
# take it as a bearer.
login=$(curl -fsS -D "$work/login.headers" -H 'Content-Type: application/json' \
  -d "{\"email\":\"owner@rules.test\",\"password\":\"$password\"}" "$base/console/v1/auth:login")
cookie=$(grep -i '^set-cookie:' "$work/login.headers" | head -1 | sed -E 's/^[^:]*: *//; s/;.*//' | tr -d '\r')
csrf=$(json csrfToken <<<"$login")
token=$(curl -fsS -H "Cookie: $cookie" -H "X-Aruma-CSRF: $csrf" -H 'Content-Type: application/json' \
  -d "{\"name\":\"rtdb-rules\",\"currentPassword\":\"$password\"}" "$base/console/v1/me/tokens" | json token)
api() { curl -fsS -H "Authorization: Bearer $token" -H 'Content-Type: application/json' "$@"; }
api -d "{\"projectId\":\"$project\",\"displayName\":\"Rules\"}" "$base/console/v1/projects" >/dev/null
key_id=$(api -d '{"platform":"WEB","displayName":"Web app"}' "$base/console/v1/projects/$project/apps" | json apiKeyId)
api_key=$(api "$base/console/v1/projects/$project/apiKeys/$key_id" | json keyString)

# Each user reads and writes only their own profile, whose age must be a number.
api -X PUT --data-binary @- "$base/.settings/rules.json?ns=$project-default-rtdb" >/dev/null <<'EOF'
{"rules": {"users": {"$uid": {
  ".read": "auth != null && auth.uid == $uid",
  ".write": "auth != null && auth.uid == $uid",
  "age": {".validate": "newData.isNumber()"}
}}}}
EOF

printf '{"base":"%s","apiKey":"%s","projectId":"%s","namespace":"%s-default-rtdb"}\n' \
  "$base" "$api_key" "$project" "$project" >"$work/config.json"
node "$exp/rtdb-rules/client.mjs" "$work/config.json" >"$work/client.log" 2>&1 &
client=$!
pids+=("$client")
for _ in $(seq 1 200); do grep -q '"step":"ready"' "$work/client.log" && break; sleep 0.1; done
uid=$(grep '"step":"ready"' "$work/client.log" | sed -E 's/.*"uid":"([^"]+)".*/\1/')
[[ -n $uid ]] || { cat "$work/client.log"; echo "the client never got ready" >&2; exit 1; }
echo "==> disabling $uid from the console"
api -X PATCH -d '{"disabled":true}' "$base/console/v1/projects/$project/auth/users/$uid" >/dev/null
status=0
wait "$client" || status=$?
cat "$work/client.log"
exit "$status"
