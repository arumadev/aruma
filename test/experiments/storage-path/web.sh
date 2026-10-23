#!/usr/bin/env bash
# S0-B, web: firebase/storage 13.1.0 in Chromium (Playwright) against the recording stub, in
# emulator mode, production mode with @aruma/web as it is, with the XHR spike
# (sdk/web/aruma-web/src/storage-spike.js) on http and https, and with the host constant replaced
# as a build plugin would. See web/run.mjs.
#
#   test/experiments/storage-path/web.sh [mode…]
#
#   S0PATH_PORT       first stub port (default 8820); the page is served on S0PATH_PAGE_PORT (8823)
#   S0PATH_UPLOAD_URL self (default) or google: the stub's X-Goog-Upload-URL
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
. "$here/lib.sh"
gen=$here/.generated/web
rm -rf "$gen"
mkdir -p "$gen"
trap stop_stub EXIT

start_stub "$gen" -upload-url "${S0PATH_UPLOAD_URL:-self}"
cd "$here/web"
[[ -d node_modules ]] || npm ci --no-audit --no-fund --silent
node run.mjs "$@" >"$gen/results.jsonl"
python3 "$here/summarize_results.py" "$gen/results.jsonl" "$STUB_LOG" --paths
