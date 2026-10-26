#!/usr/bin/env bash
# Runs the transport spike's Go and Node clients against the spike server (server/), every variant
# at once: each stack, h2c and TLS, the emulator's credential and the fake service account's, and
# Node's preferRest. Each variant makes a unary call, holds a Listen stream until the later
# message (65 s in, so the stream must stay open past 60 s) and does the Write handshake. The
# server's record is summarized at the end (summarize.py); the variants' own lines are in
# .generated/results/.
#
#   test/spike/documents/transport/run.sh [go] [node]      (default: both)
#
# The network guard: Node runs with the repository's netguard.cjs preloaded (it refuses and logs
# every connection off loopback); the Go client dials through its own guard and sends anything
# proxied to the server's refusing proxy (HTTPS_PROXY). On CI, test/ci/egress-guard.sh runs around
# this script as well. It needs Go, Node 24 and python3; `npm ci` in clients/node first.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
spike_root=$(cd "$here/../../../.." && pwd)
gen=$here/.generated
# shellcheck source=server.sh
. "$here/server.sh"
trap stop_spike EXIT

targets=("$@")
[[ ${#targets[@]} -gt 0 ]] || targets=(go node)
rm -rf "$gen/results"
mkdir -p "$gen/results"
start_spike "$gen" -later "${SPIKE_LATER:-65s}"
hold=${SPIKE_HOLD:-60}
sa=$gen/sa
ca=$gen/ca.pem

pids=() labels=() expect=()
# variant LABEL EXPECT(pass|fail) COMMAND…: runs one client in the background.
variant() {
  local label=$1 want=$2
  shift 2
  ("$@" >"$gen/results/$label.json" 2>"$gen/results/$label.log") &
  pids+=($!) labels+=("$label") expect+=("$want")
}

if [[ " ${targets[*]} " == *" go "* ]]; then
  (cd "$here/clients/go" && go build -o "$gen/bin/spike-go" .)
  export HTTPS_PROXY=http://127.0.0.1:9109 HTTP_PROXY=http://127.0.0.1:9109 NO_PROXY=127.0.0.1,localhost
  g=("$gen/bin/spike-go" -hold "${hold}s")
  variant go-h2c-connect-emulator pass "${g[@]}" -label go-h2c-connect-emulator -addr 127.0.0.1:9100 -auth emulator
  variant go-h2c-grpcgo-emulator pass "${g[@]}" -label go-h2c-grpcgo-emulator -addr 127.0.0.1:9101 -auth emulator
  variant go-h2c-connect-sa pass "${g[@]}" -label go-h2c-connect-sa -addr 127.0.0.1:9100 -auth sa -sa "$sa/sa-google.json"
  variant go-tls-connect-sa pass "${g[@]}" -label go-tls-connect-sa -addr 127.0.0.1:9102 -ca "$ca" -auth sa -sa "$sa/sa-google.json"
  variant go-tls-grpcgo-sa pass "${g[@]}" -label go-tls-grpcgo-sa -addr 127.0.0.1:9103 -ca "$ca" -auth sa -sa "$sa/sa-google.json"
  variant go-tls-connect-sa-aruma-token-uri pass "${g[@]}" -label go-tls-connect-sa-aruma-token-uri -addr 127.0.0.1:9102 -ca "$ca" -auth sa -sa "$sa/sa-aruma.json"
  variant go-tls-grpcgo-exchange-aruma pass "${g[@]}" -label go-tls-grpcgo-exchange-aruma -addr 127.0.0.1:9103 -ca "$ca" -auth exchange -sa "$sa/sa-aruma.json"
  # The token exchange at Google's token_uri, offline: refused by the guard, so every call fails.
  variant go-tls-connect-exchange-google fail "${g[@]}" -label go-tls-connect-exchange-google -addr 127.0.0.1:9102 -ca "$ca" -auth exchange -sa "$sa/sa-google.json" -listen-for 15s
  unset HTTPS_PROXY HTTP_PROXY NO_PROXY
fi

if [[ " ${targets[*]} " == *" node "* ]]; then
  : >"$gen/netguard-node.log"
  n=(env NODE_OPTIONS="--require $spike_root/test/experiments/netguard.cjs --no-deprecation" ARUMA_NETGUARD_LOG="$gen/netguard-node.log"
    NODE_EXTRA_CA_CERTS="$ca" GRPC_DEFAULT_SSL_ROOTS_FILE_PATH="$ca" node "$here/clients/node/spike.mjs" --hold "$hold")
  variant node-h2c-connect-emulator pass "${n[@]}" --label node-h2c-connect-emulator --addr 127.0.0.1:9100 --auth emulator
  variant node-h2c-grpcgo-emulator pass "${n[@]}" --label node-h2c-grpcgo-emulator --addr 127.0.0.1:9101 --auth emulator
  variant node-h2c-connect-sa pass "${n[@]}" --label node-h2c-connect-sa --addr 127.0.0.1:9100 --auth sa --sa "$sa/sa-google.json"
  variant node-tls-connect-sa pass "${n[@]}" --label node-tls-connect-sa --addr 127.0.0.1:9102 --ssl --auth sa --sa "$sa/sa-google.json"
  variant node-tls-grpcgo-sa pass "${n[@]}" --label node-tls-grpcgo-sa --addr 127.0.0.1:9103 --ssl --auth sa --sa "$sa/sa-google.json"
  variant node-tls-connect-sa-aruma-token-uri pass "${n[@]}" --label node-tls-connect-sa-aruma-token-uri --addr 127.0.0.1:9102 --ssl --auth sa --sa "$sa/sa-aruma.json"
  variant node-h2c-connect-sa-rest pass "${n[@]}" --label node-h2c-connect-sa-rest --addr 127.0.0.1:9100 --auth sa --sa "$sa/sa-google.json" --prefer-rest
  variant node-h2c-grpcgo-sa-rest pass "${n[@]}" --label node-h2c-grpcgo-sa-rest --addr 127.0.0.1:9101 --auth sa --sa "$sa/sa-google.json" --prefer-rest
  variant node-tls-connect-sa-rest pass "${n[@]}" --label node-tls-connect-sa-rest --addr 127.0.0.1:9102 --ssl --auth sa --sa "$sa/sa-google.json" --prefer-rest
  variant node-tls-grpcgo-sa-rest pass "${n[@]}" --label node-tls-grpcgo-sa-rest --addr 127.0.0.1:9103 --ssl --auth sa --sa "$sa/sa-google.json" --prefer-rest
fi

echo "waiting for ${#pids[@]} variants (each holds a Listen stream ~${hold} s)"
status=0
for i in "${!pids[@]}"; do
  got=pass
  wait "${pids[$i]}" || got=fail
  line=$(tail -1 "$gen/results/${labels[$i]}.json" 2>/dev/null || true)
  if [[ "$got" == "${expect[$i]}" ]]; then
    echo "ok    ${labels[$i]} ($got, as expected) $line"
  else
    echo "WRONG ${labels[$i]} ($got, expected ${expect[$i]}) $line"
    sed 's/^/      /' "$gen/results/${labels[$i]}.log" | tail -20
    status=1
  fi
done
stop_spike

echo
echo "== what each client sent (the server's record; credentials by shape only) =="
python3 -I "$here/summarize.py" "$gen/record.jsonl"
if [[ -s "$gen/netguard-node.log" ]]; then
  echo
  echo "== Node network guard =="
  awk '{print $1, $2}' "$gen/netguard-node.log" | sort | uniq -c
fi
echo
echo "== server log: shutdown and TLS errors =="
grep -Ei 'shutdown|tls|error' "$gen/server.log" | grep -v 'level=INFO msg=request' | tail -20 || true
exit "$status"
