#!/usr/bin/env bash
# Runs Team Board's end-to-end test: the app built with Vite (the official firebase package in
# production mode, @aruma/web's plugin with storage and firestore on), configured only with the
# web config the Management API serves, against a fresh Aruma server with the Realtime Database,
# Storage and the Document Database, with the app's three rules files and its index file deployed
# (scripts/deploy-aruma.mjs), driven by Playwright with every request off this machine blocked.
#
#   examples/web/e2e/run.sh [--emulators] [--screenshots] [--repeat N] [playwright test arguments]
#
#   --emulators     the same test against the Firebase Emulator Suite instead of Aruma: the same
#                   code, built with VITE_FIREBASE_EMULATORS=true and a demo- project's config.
#                   Needs Java 11+ and firebase-tools (FIREBASE_BIN, default: npx firebase-tools).
#   --screenshots   take the README's screenshots (e2e/screenshots.spec.ts) into docs/
#   --repeat N      run the test N times in a row against the same server
#
#   ARUMA_E2E_ADDR, ARUMA_E2E_DB_PREFIX  see test/e2e/server.sh
#   E2E_PAGE_PORT  the app's port (default 4191)
#   PW_CHROMIUM    a Chromium to use instead of Playwright's own
#
# It needs the local Postgres (ARUMA_TEST_DATABASE_URL), Go, Node, curl and python3. Under
# test/ci/egress-guard.sh (ARUMA_EGRESS_GUARD=1) it installs nothing: the job did that before.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
app=$(cd "$here/.." && pwd)
root=$(cd "$app/../.." && pwd)
gen=$here/.generated

backend=aruma
screenshots=
repeat=1
args=()
while [[ $# -gt 0 ]]; do
  case $1 in
    --emulators) backend=emulators ;;
    --screenshots) screenshots=1 ;;
    --repeat) repeat=$2; shift ;;
    *) args+=("$1") ;;
  esac
  shift
done

log() { printf '\n==> %s\n' "$*"; }

emulators_pid=
cleanup() {
  if [[ -n $emulators_pid ]]; then
    kill "$emulators_pid" 2>/dev/null || true
    wait "$emulators_pid" 2>/dev/null || true
  fi
  if declare -F stop_server >/dev/null; then stop_server; fi
}
trap cleanup EXIT

if [[ -z ${ARUMA_EGRESS_GUARD:-} ]]; then
  log "installing the app's packages"
  (cd "$app" && npm ci --no-audit --no-fund --silent)
  [[ -n ${PW_CHROMIUM:-} ]] || (cd "$app" && npx playwright install ${CI:+--with-deps} chromium >/dev/null)
fi

export E2E_PAGE_PORT=${E2E_PAGE_PORT:-4191}
export FIREBASE_CONFIG=$gen/firebase-config.json
export E2E_DIST=$gen/dist
export E2E_BACKEND=$backend

if [[ $backend == aruma ]]; then
  log "starting Aruma (Realtime Database, Storage, Document Database)"
  # shellcheck source=../../../test/e2e/server.sh
  . "$root/test/e2e/server.sh"
  start_server "$gen" -rtdb -storage -documents -switch 127.0.0.1:0 -log "${ARUMA_E2E_LOG:-warn}"

  # The web config the Management API serves, as a developer downloads it from the dashboard.
  app_id=$(python3 -c 'import json, sys; print(json.load(open(sys.argv[1]))["web"]["appId"])' "$gen/expected.json")
  curl -fsS -H "Authorization: Bearer $ARUMA_CLI_TOKEN" \
    "$ARUMA_URL/v1beta1/projects/$ARUMA_PROJECT_ID/webApps/$app_id/config" >"$FIREBASE_CONFIG"

  log "deploying the rules and the index (scripts/deploy-aruma.mjs)"
  (cd "$app" && ARUMA_TOKEN=$ARUMA_CLI_TOKEN node scripts/deploy-aruma.mjs)

  export E2E_STORAGE_ORIGIN=$ARUMA_URL
  build_env=(ARUMA_URL="$ARUMA_URL" VITE_FIREBASE_EMULATORS=false)
else
  log "starting the Firebase Emulator Suite"
  rm -rf "$gen" && mkdir -p "$gen"
  project=demo-team-board
  # A demo- project needs no Google account and reaches nothing outside this machine.
  cat >"$FIREBASE_CONFIG" <<EOF
{
  "projectId": "$project",
  "appId": "1:000000000000:web:0000000000000000",
  "apiKey": "demo-api-key",
  "authDomain": "$project.firebaseapp.com",
  "databaseURL": "http://127.0.0.1:9000/?ns=$project-default-rtdb",
  "storageBucket": "$project.appspot.com",
  "messagingSenderId": "000000000000"
}
EOF
  # The emulators are Java programs and firebase-tools a Node one; the proxy settings of this
  # shell must not reach them, or their loopback requests go to the proxy.
  (
    cd "$app"
    unset JAVA_TOOL_OPTIONS HTTP_PROXY HTTPS_PROXY http_proxy https_proxy NO_PROXY no_proxy
    exec ${FIREBASE_BIN:-npx --yes firebase-tools@15.37.0} emulators:start \
      --project "$project" --only auth,firestore,database,storage
  ) >"$gen/emulators.log" 2>&1 &
  emulators_pid=$!
  for _ in $(seq 1 240); do
    grep -q 'All emulators ready' "$gen/emulators.log" 2>/dev/null && break
    kill -0 "$emulators_pid" 2>/dev/null || { cat "$gen/emulators.log" >&2; exit 1; }
    sleep 0.5
  done
  grep -q 'All emulators ready' "$gen/emulators.log" || { cat "$gen/emulators.log" >&2; exit 1; }
  export E2E_STORAGE_ORIGIN=http://127.0.0.1:9199
  build_env=(ARUMA_URL= VITE_FIREBASE_EMULATORS=true)
fi

log "building the app ($backend)"
(cd "$app" && env "${build_env[@]}" npx vite build --outDir "$E2E_DIST" --emptyOutDir --logLevel warn)

if [[ -n $screenshots ]]; then
  log "taking the screenshots"
  (cd "$app" && E2E_SCREENSHOTS=1 npx playwright test -c e2e/playwright.config.ts "${args[@]}")
  exit 0
fi

for i in $(seq 1 "$repeat"); do
  log "Team Board e2e on $backend ($i of $repeat)"
  (cd "$app" && npx playwright test -c e2e/playwright.config.ts "${args[@]}")
done
log "Team Board e2e passed on $backend"
