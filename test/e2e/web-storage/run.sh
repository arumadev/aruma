#!/usr/bin/env bash
# Runs the web Storage end-to-end tests (ADR-022 §16.1): the official firebase package in production
# mode, with Storage's host rewritten at build time by @aruma/web, built with the real npm Vite 8,
# webpack, esbuild, Rollup and Rspack (build.mjs, which also checks the builds that must fail) and as a SvelteKit
# app, then driven by Playwright in Chromium against a real Aruma server with Storage
# (server/main.go), with every request off this machine blocked.
#
#   test/e2e/web-storage/run.sh [playwright test arguments]
#
#   ARUMA_E2E_ADDR, ARUMA_E2E_DB_PREFIX  see lib.sh
#   ARUMA_E2E_PAGE_PORT  the page server's port (default 9421)
#   ARUMA_E2E_SSR_PORT   the SvelteKit server's port (default 9422)
#   ARUMA_E2E_SSR=off    skip the SvelteKit app (apps/sveltekit)
#
# The other bundlers and frameworks (Vite 5 to 7 and the dev servers, Turbopack, Expo, Flutter web,
# the run-time fallback) are measured by measure.sh. It needs the local Postgres
# (ARUMA_TEST_DATABASE_URL), Go, Node and curl.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../../.." && pwd)
gen=$here/.generated
# shellcheck source=lib.sh
. "$here/lib.sh"
trap stop_all EXIT
ssr_port=${ARUMA_E2E_SSR_PORT:-9422}

start_aruma
cd "$here"
npm ci --no-audit --no-fund --silent
npx playwright install ${CI:+--with-deps} chromium >/dev/null

log "builds: Vite 8, webpack, esbuild, Rollup, Rspack and the fallback, and the builds that must fail"
node build.mjs

if [[ "${ARUMA_E2E_SSR:-on}" != off ]]; then
  log "SvelteKit (SSR): build, and serve with Node under the network guard"
  (cd apps/sveltekit && npm ci --no-audit --no-fund --silent && npx vite build --logLevel error)
  ARUMA_NETGUARD_LOG=$gen/ssr-netguard.log NODE_OPTIONS="--require $root/test/experiments/netguard.cjs" \
    PORT=$ssr_port HOST=127.0.0.1 ORIGIN="http://127.0.0.1:$ssr_port" \
    node apps/sveltekit/build/index.js >"$gen/ssr.log" 2>&1 &
  pids+=($!)
  export ARUMA_E2E_SSR_URL="http://127.0.0.1:$ssr_port"
  wait_http "$ARUMA_E2E_SSR_URL/"
fi

log "Playwright: Vite 8, webpack, esbuild, Rollup, Rspack, the fallback${ARUMA_E2E_SSR_URL:+, SvelteKit}"
ARUMA_E2E_APPS=vite,webpack,esbuild,rollup,rspack,fallback npx playwright test "$@"
if [[ -n "${ARUMA_E2E_SSR_URL:-}" ]] && grep -q BLOCK "$gen/ssr-netguard.log" 2>/dev/null; then
  echo "the SvelteKit server tried to leave the machine:" >&2
  grep BLOCK "$gen/ssr-netguard.log" >&2
  exit 1
fi
log "web Storage e2e passed"
