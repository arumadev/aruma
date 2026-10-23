#!/usr/bin/env bash
# S0-B, Admin SDK for Go: firebase-admin-go 4.22.0 and cloud.google.com/go/storage 1.62.1 against
# the recording stub, one process per configuration (admin-go/main.go, a module of its own).
# HTTPS_PROXY points at the stub's guard proxy, which refuses and records every request that
# would leave loopback (Go never proxies loopback).
#
#   test/experiments/storage-path/admin-go.sh [scenario…]
#
# Over http: Go on macOS verifies TLS with the system's trust store only (SSL_CERT_FILE is ignored),
# and the TLS path is not what these runs measure; the Node runs cover https.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
. "$here/lib.sh"
gen=$here/.generated/admin-go
rm -rf "$gen"
mkdir -p "$gen"
trap stop_stub EXIT

scenarios=("$@")
[[ ${#scenarios[@]} -gt 0 ]] || scenarios=(
  admin-default admin-emulator-http admin-endpoint admin-endpoint-jsonreads admin-endpoint-prefix gcs-endpoint
)

start_stub "$gen" -buckets s0path.appspot.com
(cd "$here/admin-go" && go build -o "$gen/admin-go" .)

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
    admin-emulator-http) extra=("STORAGE_EMULATOR_HOST=${STUB_HTTP#http://}") ;;
  esac
  echo "{\"marker\":\"$s\"}" >>"$STUB_LOG"
  env -u STORAGE_EMULATOR_HOST -u FIREBASE_STORAGE_EMULATOR_HOST -u GOOGLE_APPLICATION_CREDENTIALS \
    KEY_FILE="$gen/service-account.json" BUCKET=s0path.appspot.com STUB_URL="$STUB_HTTP" \
    HTTPS_PROXY="$STUB_GUARD" HTTP_PROXY="$STUB_GUARD" NO_PROXY= ${extra[@]+"${extra[@]}"} \
    "$gen/admin-go" "$s" 2>>"$gen/stderr.log" >>"$gen/results.jsonl" \
    || echo "{\"scenario\":\"$s\",\"results\":[{\"op\":\"process\",\"ok\":false,\"detail\":\"exited with $?\"}]}" >>"$gen/results.jsonl"
done
python3 "$here/summarize_results.py" "$gen/results.jsonl" "$STUB_LOG" --paths
