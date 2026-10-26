#!/usr/bin/env bash
# Runs the official FirebaseFirestore (TransportSpikeTests) against the transport spike server on an
# iOS simulator, once per listener: h2c to connect and to grpc-go (isSSLEnabled=false, required),
# then TLS to each, reported but not required: iOS's gRPC trusts only the roots.pem built into it,
# never the spike's CA, so the TLS runs show how that fails (the server logs the handshake).
# CI only (.github/workflows/documents-spike.yml): it builds Firebase and runs a simulator.
#
# The network guard is test/e2e/ios-rtdb's socket half: gRPC's sockets do not go through
# URLSession, so this samples the test process's sockets (lsof) while the tests run and fails on
# any whose far end is not loopback.
#
#   ARUMA_IOS_SIMULATOR  simulator UDID (default: a booted iPhone, else the first available one)
#   ARUMA_XCODE_DIR      Swift package checkouts and DerivedData (default .cache/xcode)
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
spike=$(cd "$here/../.." && pwd)
spike_root=$(cd "$spike/../../../.." && pwd)
gen=$spike/.generated
derived=${ARUMA_XCODE_DIR:-$spike_root/.cache/xcode}
# shellcheck source=../../server.sh
. "$spike/server.sh"

log() { printf '\n==> %s\n' "$*"; }

watcher=
stop_watcher() {
  if [[ -n "$watcher" ]]; then
    kill "$watcher" 2>/dev/null || true
    wait "$watcher" 2>/dev/null || true
  fi
  watcher=
}
trap 'stop_watcher; stop_spike' EXIT

simulator() {
  if [[ -n "${ARUMA_IOS_SIMULATOR:-}" ]]; then
    echo "$ARUMA_IOS_SIMULATOR"
    return
  fi
  xcrun simctl list devices available -j | python3 -c '
import json, sys
devices = [d for rt, ds in json.load(sys.stdin)["devices"].items() if "iOS" in rt for d in ds if d["name"].startswith("iPhone")]
booted = [d for d in devices if d["state"] == "Booted"]
print((booted or devices)[0]["udid"] if devices else "")'
}

# watch_sockets appends the internet sockets of the running test process to sockets.txt
# (local->remote) every 200 ms. A simulator's process is a process of this Mac.
watch_sockets() {
  while :; do
    for pid in $(pgrep -x xctest || true); do
      lsof -nP -a -p "$pid" -i -F n 2>/dev/null | sed -n 's/^n//p' >>"$gen/ios-sockets.txt" || true
    done
    sleep 0.2
  done
}

sim=$(simulator)
[[ -n "$sim" ]] || { echo "no iPhone simulator available" >&2; exit 1; }
mkdir -p "$gen"

log "starting the spike server"
start_spike "$gen" -later "${SPIKE_LATER:-65s}"

log "building FirebaseFirestore and the tests for simulator $sim"
xcode=(-scheme TransportSpikeIOS-Package -destination "platform=iOS Simulator,id=$sim"
  -clonedSourcePackagesDirPath "$derived/spm" -derivedDataPath "$derived/dd-spike-transport")
(cd "$here" && xcodebuild build-for-testing "${xcode[@]}" >"$gen/ios-build.log" 2>&1) \
  || { tail -60 "$gen/ios-build.log" >&2; exit 1; }

: >"$gen/ios-sockets.txt"
watch_sockets &
watcher=$!

status=0
# run LABEL PORT SSL REQUIRED(yes|no) LISTEN_FOR
run() {
  local label=$1 port=$2 ssl=$3 required=$4 listen_for=$5
  log "ios $label (127.0.0.1:$port, isSSLEnabled=$ssl)"
  if (cd "$here" && TEST_RUNNER_SPIKE_ADDR="127.0.0.1:$port" TEST_RUNNER_SPIKE_SSL=$ssl TEST_RUNNER_SPIKE_LABEL=$label \
    TEST_RUNNER_SPIKE_HOLD="${SPIKE_HOLD:-60}" TEST_RUNNER_SPIKE_LISTEN_FOR=$listen_for \
    xcodebuild test-without-building "${xcode[@]}" >"$gen/ios-$label.log" 2>&1); then
    echo "PASS ios-$label"
  elif [[ $required == yes ]]; then
    echo "FAIL ios-$label (required)"
    grep -E "RESULT|error:|Test Case.*failed" "$gen/ios-$label.log" | head -20 || tail -40 "$gen/ios-$label.log"
    status=1
  else
    echo "FAIL ios-$label (reported, not required)"
  fi
  grep -E "^RESULT " "$gen/ios-$label.log" || true
}
run h2c-connect 9100 false yes 100
run h2c-grpcgo 9101 false yes 100
run tls-connect 9102 true no 30
run tls-grpcgo 9103 true no 30
stop_watcher

sort -u "$gen/ios-sockets.txt" >"$gen/ios-sockets.unique.txt"
outside=$(grep -- '->' "$gen/ios-sockets.unique.txt" | sed 's/.*->//' | grep -Ev '^(127\.0\.0\.1|\[::1\]|localhost):' || true)
echo
echo "sockets of the test process seen: $(grep -c -- '->' "$gen/ios-sockets.unique.txt" || true)"
if [[ -n "$outside" ]]; then
  echo "sockets of the test process to outside this machine:" >&2
  echo "$outside" >&2
  status=1
fi
grep -q -- '->127.0.0.1:910[0-3]$' "$gen/ios-sockets.unique.txt" \
  || { echo "the socket watcher saw no connection to the spike server: it did not watch the tests" >&2; status=1; }

stop_spike
echo
echo "== what FirebaseFirestore sent (the server's record; credentials by shape only) =="
python3 -I "$spike/summarize.py" "$gen/record.jsonl"
echo
echo "== server log: TLS errors =="
grep -i 'tls' "$gen/server.log" | tail -10 || true
exit "$status"
