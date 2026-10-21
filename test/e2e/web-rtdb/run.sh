#!/usr/bin/env bash
# Runs the web Realtime Database end-to-end tests: a Vite app on the official firebase package
# (11, 12 and 13) in production mode, configured only with the web config Aruma's Management API
# serves (its databaseURL included), Auth redirected by @aruma/web's Vite plugin, driven by
# Playwright against a real Aruma server with the database enabled and the rules of
# ../rtdb-rules.json (not dev_open_rules), with every request off this machine blocked.
#
# Then the long-poll trap of test/experiments/rtdb-lp-browser.sh (a restart that leaves the SDK on
# long polling, the upgrade back, blocked localStorage), which this suite does not repeat.
#
#   test/e2e/web-rtdb/run.sh [playwright test arguments]
#
#   ARUMA_E2E_ADDR, ARUMA_E2E_DB_PREFIX  see ../server.sh
#   ARUMA_E2E_SWITCH_ADDR  the server's off switch (default 127.0.0.1:0)
#   ARUMA_E2E_PAGE_PORT    the Vite server's port (default 4181)
#   ARUMA_E2E_LP=off       skip the long-poll trap; RTDB_LP_PORT and RTDB_LP_DB_PREFIX go to it
#
# It needs the local Postgres (ARUMA_TEST_DATABASE_URL), Go, Node, curl and python3; the long-poll
# trap also psql and openssl.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../../.." && pwd)
gen=$here/.generated
cd "$root"
# shellcheck source=../server.sh
. "$root/test/e2e/server.sh"
trap stop_server EXIT

log() { printf '\n==> %s\n' "$*"; }

# start runs a fresh Aruma server with the database, deploys the rules and writes the web config
# the Management API serves.
start() {
  start_server "$gen" -rtdb -switch "${ARUMA_E2E_SWITCH_ADDR:-127.0.0.1:0}" -log warn
  put_rtdb_rules "$root/test/e2e/rtdb-rules.json"
  local app_id
  app_id=$(python3 -c 'import json, sys; print(json.load(open(sys.argv[1]))["web"]["appId"])' "$gen/expected.json")
  curl -fsS -H "Authorization: Bearer $ARUMA_CLI_TOKEN" \
    "$ARUMA_URL/v1beta1/projects/$ARUMA_PROJECT_ID/webApps/$app_id/config" >"$gen/firebase-config.json"
}

(cd "$here" && npm ci --no-audit --no-fund --silent)
(cd "$here" && npx playwright install ${CI:+--with-deps} chromium >/dev/null)

# One server per firebase major version, as in web-auth: Aruma's per-address limits are sized for
# one app, and each version starts from an empty database.
for major in 11 12 13; do
  log "web Realtime Database e2e: firebase $major against a fresh Aruma server"
  start
  (cd "$here" && ARUMA_E2E_DIR=$gen npx playwright test --project "firebase-$major" "$@")
  stop_server
done

if [[ "${ARUMA_E2E_LP:-on}" != off ]]; then
  log "the long-poll trap (test/experiments/rtdb-lp-browser.sh)"
  (cd "$root/test/experiments/rtdb-lp-browser" && npm ci --no-audit --no-fund --silent)
  "$root/test/experiments/rtdb-lp-browser.sh" >"$gen/lp-browser.json" || { cat "$gen/lp-browser.json" >&2; exit 1; }
  python3 -c 'import json, sys; c = json.load(open(sys.argv[1]))["checks"]; [print(f"  {v:3} {k}") for k, v in c.items()]' "$gen/lp-browser.json"
fi
log "web Realtime Database e2e passed"
