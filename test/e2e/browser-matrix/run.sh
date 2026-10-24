#!/usr/bin/env bash
# Runs the browser matrix (ADR-022 §10.6, F §2.4): Aruma's download replies in Chromium, Firefox
# and WebKit, driven by Playwright. server/main.go starts a real Aruma server with Storage on a
# throwaway Postgres database and stores the matrix's objects; pages.mjs serves the app's pages
# on two other origins. The engines run one after the other, against the same server.
#
#   test/e2e/browser-matrix/run.sh [engine ...] [-- playwright test arguments]
#
#   engines              chromium firefox webkit (default: all three)
#   ARUMA_E2E_ADDR       Aruma's address (default 127.0.0.1:9260)
#   ARUMA_E2E_PAGE_PORT  the pages' port (default 9261), under 127.0.0.1 and localhost
#   ARUMA_E2E_DB_PREFIX  prefix of the throwaway database (default aruma_e2e_)
#
# It needs the local Postgres (ARUMA_TEST_DATABASE_URL), Go and Node. Playwright's browsers go to
# PLAYWRIGHT_BROWSERS_PATH (.cache/env.sh keeps them off the system disk). On Linux the engines need
# a sound device, a PulseAudio null sink will do (as the CI job has): Firefox fails case 14's song
# without one.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../../.." && pwd)
gen=$here/.generated
engines=()
while [[ $# -gt 0 && "$1" != -- ]]; do engines+=("$1"); shift; done
[[ "${1:-}" == -- ]] && shift
[[ ${#engines[@]} -gt 0 ]] || engines=(chromium firefox webkit)

addr=${ARUMA_E2E_ADDR:-127.0.0.1:9260}
port=${ARUMA_E2E_PAGE_PORT:-9261}
export ARUMA_E2E_PAGE_PORT=$port ARUMA_E2E_DIR=$gen

server_pid=
stop_server() {
  if [[ -n "$server_pid" ]]; then
    kill "$server_pid" 2>/dev/null || true
    wait "$server_pid" 2>/dev/null || true
  fi
  server_pid=
}
trap stop_server EXIT

rm -rf "$gen"
mkdir -p "$gen"
(cd "$root" && go build -o "$gen/server" ./test/e2e/browser-matrix/server)
"$gen/server" -addr "$addr" -db-prefix "${ARUMA_E2E_DB_PREFIX:-aruma_e2e_}" \
  -fixtures "$here/fixtures" -out "$gen" >"$gen/server.out" 2>"$gen/server.log" &
server_pid=$!
for _ in $(seq 1 240); do
  grep -q ready "$gen/server.out" 2>/dev/null && break
  kill -0 "$server_pid" 2>/dev/null || { cat "$gen/server.log" >&2; server_pid=; exit 1; }
  sleep 0.25
done
grep -q ready "$gen/server.out" || { echo "Aruma did not start" >&2; exit 1; }

# Under test/ci/egress-guard.sh nothing is installed: the job installed the packages, the engine
# and its system dependencies before turning the guard on.
[[ -n ${ARUMA_EGRESS_GUARD:-} ]] || (cd "$here" && npm ci --no-audit --no-fund --silent)
status=0
for engine in "${engines[@]}"; do
  printf '\n==> browser matrix: %s\n' "$engine"
  [[ -n ${ARUMA_EGRESS_GUARD:-} ]] || (cd "$here" && npx playwright install "$engine" >/dev/null)
  (cd "$here" && npx playwright test --project "$engine" "$@") || status=1
done
exit $status
