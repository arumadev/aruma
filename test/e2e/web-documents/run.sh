#!/usr/bin/env bash
# Runs the web Document Database end-to-end tests: the official firebase package's Firestore (the
# full SDK over WebChannel, streaming and long polling, and the lite SDK over REST) built by Vite
# through @aruma/web (aruma({ url, firestore: true })), in a browser on another origin than Aruma,
# signed in with Aruma Auth, against a real Aruma server whose cloud.firestore rules are released
# through the firebaserules API (firestore.rules), with every request off this machine blocked.
#
#   test/e2e/web-documents/run.sh [engine ...] [-- playwright test arguments]
#
#   engines              chromium firefox webkit (default: chromium)
#   ARUMA_E2E_PAGE_PORT  the app's port (default 4181)
#   ARUMA_E2E_ADDR       Aruma's address (default 127.0.0.1:0: any free port)
#
# It needs the local Postgres (ARUMA_TEST_DATABASE_URL), Go and Node. Playwright's browsers go to
# PLAYWRIGHT_BROWSERS_PATH (.cache/env.sh keeps them off the system disk).
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../../.." && pwd)
gen=$here/.generated
engines=()
while [[ $# -gt 0 && "$1" != -- ]]; do engines+=("$1"); shift; done
[[ "${1:-}" == -- ]] && shift
[[ ${#engines[@]} -gt 0 ]] || engines=(chromium)
# shellcheck disable=SC1091
. "$here/../server.sh"
trap stop_server EXIT

log() { printf '\n==> %s\n' "$*"; }

log "starting Aruma with the Document Database"
start_server "$gen" -documents -log "${ARUMA_E2E_LOG:-warn}"

# firebase deploy --only firestore:rules: a ruleset, then the cloud.firestore release.
auth=(-H "Authorization: Bearer $ARUMA_CLI_TOKEN" -H "Content-Type: application/json")
ruleset=$(python3 -c 'import json, sys; print(json.dumps({"source": {"files": [{"name": "firestore.rules", "content": open(sys.argv[1]).read()}]}}))' "$here/firestore.rules" \
  | curl -fsS -X POST "${auth[@]}" --data-binary @- "$ARUMA_URL/v1/projects/$ARUMA_PROJECT_ID/rulesets" \
  | python3 -c 'import json, sys; print(json.load(sys.stdin)["name"])')
python3 -c 'import json, sys; print(json.dumps({"name": "projects/%s/releases/cloud.firestore" % sys.argv[1], "rulesetName": sys.argv[2]}))' "$ARUMA_PROJECT_ID" "$ruleset" \
  | curl -fsS -X POST "${auth[@]}" --data-binary @- "$ARUMA_URL/v1/projects/$ARUMA_PROJECT_ID/releases" >/dev/null

# The web config the Management API serves, as a developer copies it.
app_id=$(node -p "require('$gen/expected.json').web.appId")
curl -fsS -H "Authorization: Bearer $ARUMA_CLI_TOKEN" \
  "$ARUMA_URL/v1beta1/projects/$ARUMA_PROJECT_ID/webApps/$app_id/config" >"$gen/firebase-config.json"
export ARUMA_URL ARUMA_E2E_DIR=$gen

# Under test/ci/egress-guard.sh nothing is installed: the job installed the packages, the engines
# and their system dependencies before turning the guard on.
[[ -n ${ARUMA_EGRESS_GUARD:-} ]] || (cd "$here" && npm ci --no-audit --no-fund --silent)
status=0
for engine in "${engines[@]}"; do
  log "web Documents e2e: $engine"
  [[ -n ${ARUMA_EGRESS_GUARD:-} ]] || (cd "$here" && npx playwright install "$engine" >/dev/null)
  (cd "$here" && npx playwright test --project "$engine" "$@") || status=1
done
exit $status
