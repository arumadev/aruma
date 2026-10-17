#!/usr/bin/env bash
# T22: firebase-admin (Node) with an Aruma service account key. Starts a throwaway Aruma
# (test/smoke/genconfig -serve) and runs admin-node/credential.test.mjs against it:
#   1. cert(keyFile).getAccessToken() ignores token_uri and goes to oauth2.googleapis.com
#      (refused and recorded by the network guard);
#   2. arumaCredential(keyFile) exchanges the key at token_uri and the token lists the project
#      on GET /v1beta1/projects.
#
#   test/experiments/admin-node.sh
#
# Needs Go, Node 20+ and the local Postgres. Results: docs/research/experiments.md.
set -euo pipefail

exp=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$exp/../.." && pwd)
[[ -f "$root/.cache/env.sh" ]] && source "$root/.cache/env.sh"
source "$exp/server.sh"

work=$(mktemp -d "${TMPDIR:-/tmp}/aruma-t22.XXXXXX")
cleanup() {
  stop_aruma
  rm -rf "$work"
}
trap cleanup EXIT INT TERM

echo "==> starting Aruma"
start_aruma "$work"
echo "ARUMA_URL=$ARUMA_URL"

cd "$exp/admin-node"
[[ -d node_modules ]] || npm ci --no-audit --no-fund --silent
echo "==> firebase-admin $(node -p 'require("./node_modules/firebase-admin/package.json").version')"
ARUMA_KEY_FILE="$ARUMA_GEN/service-account.json" npm test --silent
