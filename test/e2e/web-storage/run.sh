#!/usr/bin/env bash
# Runs the web Storage end-to-end tests (ADR-022 §16.1): the official firebase package in production
# mode, with Storage's host rewritten at build time by @aruma/web, built with the real npm Vite 8,
# webpack and esbuild (build.mjs, which also checks the builds that must fail), then driven by
# Playwright in Chromium against a real Aruma server with Storage (server/main.go), with every
# request off this machine blocked.
#
#   test/e2e/web-storage/run.sh [playwright test arguments]
#
#   ARUMA_E2E_ADDR       Aruma's address (default 127.0.0.1:9420)
#   ARUMA_E2E_DB_PREFIX  prefix of the throwaway database (default aruma_e2e_)
#   ARUMA_E2E_PAGE_PORT  the page server's port (default 9421)
#   ARUMA_E2E_SSR_PORT   the SvelteKit server's port (default 9422)
#   ARUMA_E2E_SSR=off    skip the SvelteKit app (apps/sveltekit)
#
# It needs the local Postgres (ARUMA_TEST_DATABASE_URL), Go and Node.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../../.." && pwd)
gen=$here/.generated
addr=${ARUMA_E2E_ADDR:-127.0.0.1:9420}
export ARUMA_URL="http://$addr" ARUMA_E2E_DIR=$gen
export ARUMA_E2E_PAGE_PORT=${ARUMA_E2E_PAGE_PORT:-9421} ARUMA_E2E_SSR_PORT=${ARUMA_E2E_SSR_PORT:-9422}

log() { printf '\n==> %s\n' "$*"; }

server_pid=
ssr_pid=
stop() {
  for pid in $ssr_pid $server_pid; do
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  done
}
trap stop EXIT

rm -rf "$gen"
mkdir -p "$gen"
(cd "$root" && go build -o "$gen/server" ./test/e2e/web-storage/server)
"$gen/server" -addr "$addr" -db-prefix "${ARUMA_E2E_DB_PREFIX:-aruma_e2e_}" -out "$gen" >"$gen/server.out" 2>"$gen/server.log" &
server_pid=$!
for _ in $(seq 1 240); do
  grep -q ready "$gen/server.out" 2>/dev/null && break
  kill -0 "$server_pid" 2>/dev/null || { cat "$gen/server.log" >&2; exit 1; }
  sleep 0.25
done
grep -q ready "$gen/server.out" || { echo "Aruma did not start" >&2; exit 1; }

cd "$here"
npm ci --no-audit --no-fund --silent
npx playwright install ${CI:+--with-deps} chromium >/dev/null

log "builds: Vite 8, webpack and esbuild, and the builds that must fail"
node build.mjs

apps=vite,webpack,esbuild
if [[ "${ARUMA_E2E_SSR:-on}" != off ]]; then
  log "SvelteKit (SSR): build, and serve with Node under the network guard"
  (cd apps/sveltekit && npm ci --no-audit --no-fund --silent && npx vite build --logLevel warn)
  ARUMA_NETGUARD_LOG=$gen/ssr-netguard.log NODE_OPTIONS="--require $root/test/experiments/netguard.cjs" \
    PORT=$ARUMA_E2E_SSR_PORT HOST=127.0.0.1 ORIGIN="http://127.0.0.1:$ARUMA_E2E_SSR_PORT" \
    node apps/sveltekit/build/index.js >"$gen/ssr.log" 2>&1 &
  ssr_pid=$!
  export ARUMA_E2E_SSR_URL="http://127.0.0.1:$ARUMA_E2E_SSR_PORT"
fi

log "Playwright: $apps${ssr_pid:+, SvelteKit}"
ARUMA_E2E_APPS=$apps npx playwright test "$@"
if [[ -n "$ssr_pid" ]] && grep -q BLOCK "$gen/ssr-netguard.log" 2>/dev/null; then
  echo "the SvelteKit server tried to leave the machine:" >&2
  grep BLOCK "$gen/ssr-netguard.log" >&2
  exit 1
fi
log "web Storage e2e passed"
