#!/usr/bin/env bash
# S0-B, Admin SDK for Node: firebase-admin 14.5.0 and @google-cloud/storage 8.4.0 against the
# recording stub, one process per configuration (admin-node/scenario.mjs). The network guard
# (../netguard.cjs) refuses every connection that leaves loopback and records it.
#
#   test/experiments/storage-path/admin-node.sh [scenario…]
#
# Results: .generated/admin-node/results.jsonl (one line per scenario) and requests.jsonl (the
# stub's log); a summary is printed.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
. "$here/lib.sh"
gen=$here/.generated/admin-node
rm -rf "$gen"
mkdir -p "$gen"
trap stop_stub EXIT

scenarios=("$@")
[[ ${#scenarios[@]} -gt 0 ]] || scenarios=(
  admin-cert-default admin-cert-emulator-http admin-cert-emulator-https admin-firebase-emulator-host
  admin-aruma-credential gcs-endpoint-noauth gcs-endpoint-auth-keyfile gcs-endpoint-auth-aruma
  gcs-endpoint-prefix-aruma
)

start_stub "$gen" -buckets s0path.appspot.com
cd "$here/admin-node"
[[ -d node_modules ]] || npm ci --no-audit --no-fund --silent

# A throwaway service account key whose token_uri is the stub, like the key files Aruma issues.
KEY_FILE=$gen/service-account.json node -e '
const { generateKeyPairSync } = require("node:crypto");
const { privateKey } = generateKeyPairSync("rsa", { modulusLength: 2048 });
require("node:fs").writeFileSync(process.env.KEY_FILE, JSON.stringify({
  type: "service_account", project_id: "s0path", private_key_id: "s0path-key-1",
  private_key: privateKey.export({ type: "pkcs8", format: "pem" }),
  client_email: "admin@s0path.iam.gserviceaccount.com", client_id: "1",
  token_uri: process.env.STUB_HTTP + "/oauth2/v1/token",
}, null, 2));'

for s in "${scenarios[@]}"; do
  extra=()
  case $s in
    admin-cert-emulator-http) extra=("STORAGE_EMULATOR_HOST=$STUB_HTTP") ;;
    admin-cert-emulator-https) extra=("STORAGE_EMULATOR_HOST=$STUB_HTTPS") ;;
    admin-firebase-emulator-host) extra=("FIREBASE_STORAGE_EMULATOR_HOST=${STUB_HTTP#http://}") ;;
  esac
  echo "{\"marker\":\"$s\"}" >>"$STUB_LOG"
  env -u STORAGE_EMULATOR_HOST -u FIREBASE_STORAGE_EMULATOR_HOST -u GOOGLE_APPLICATION_CREDENTIALS \
    KEY_FILE="$gen/service-account.json" BUCKET=s0path.appspot.com \
    NODE_EXTRA_CA_CERTS="$STUB_CA" NODE_OPTIONS="--require $here/../netguard.cjs" \
    ARUMA_NETGUARD_LOG="$gen/netguard.log" ${extra[@]+"${extra[@]}"} \
    node scenario.mjs "$s" 2>>"$gen/stderr.log" >>"$gen/results.jsonl" || echo "{\"scenario\":\"$s\",\"results\":[{\"op\":\"process\",\"ok\":false,\"detail\":\"exited with $?\"}]}" >>"$gen/results.jsonl"
done
python3 "$here/summarize_results.py" "$gen/results.jsonl" "$STUB_LOG"
