#!/usr/bin/env bash
# Runs the iOS Realtime Database end-to-end tests: the official FirebaseDatabase and FirebaseAuth in
# production mode (no useEmulator) in an XCTest bundle hosted by an empty app on an iOS simulator,
# configured only with the GoogleService-Info.plist a real Aruma server generates (its DATABASE_URL
# is the database; ArumaRedirect sends Auth to Aruma). The server runs with the database enabled
# and the rules of ../rtdb-rules.json (not dev_open_rules).
#
# The network guard has two halves: LoopbackGuard refuses and reports every URLSession request that
# neither goes to this machine nor is taken by ArumaRedirect, and this script samples the host app's
# sockets (lsof) while the tests run, since the database's WebSockets do not go through URLSession;
# the run fails on any socket whose far end is not loopback.
#
#   test/e2e/ios-rtdb/run.sh [xcodebuild arguments, e.g. -only-testing:RtdbE2ETests/RtdbE2ETests/testQueryWindowFollowsWrites]
#
#   ARUMA_IOS_SIMULATOR  simulator UDID (default: a booted iPhone, else the first available one)
#   ARUMA_XCODE_DIR      Swift package checkouts and DerivedData (default .cache/xcode)
#   ARUMA_DERIVED_DATA   DerivedData alone (default $ARUMA_XCODE_DIR/dd-rtdb-e2e)
#   ARUMA_E2E_ADDR, ARUMA_E2E_DB_PREFIX  see ../server.sh
#   ARUMA_E2E_SWITCH_ADDR  the server's off switch (default 127.0.0.1:0)
#
# It needs Xcode, the local Postgres (ARUMA_TEST_DATABASE_URL), Go, curl and python3. The same script
# runs in Codemagic (codemagic.yaml, workflow ios-rtdb).
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../../.." && pwd)
gen=$here/.generated
derived=${ARUMA_XCODE_DIR:-$root/.cache/xcode}
cd "$root"
# shellcheck source=../server.sh
. "$root/test/e2e/server.sh"

watcher=
stop_watcher() {
  if [[ -n "$watcher" ]]; then
    kill "$watcher" 2>/dev/null || true
    wait "$watcher" 2>/dev/null || true
  fi
  watcher=
}
cleanup() {
  stop_watcher
  stop_server
}
trap cleanup EXIT

log() { printf '\n==> %s\n' "$*"; }

# simulator prints ARUMA_IOS_SIMULATOR, or a booted iPhone, or the first available iPhone.
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

# watch_sockets appends the internet sockets of the running host app to sockets.txt, as lsof names
# them (local->remote), every 200 ms. A simulator's app is a process of this Mac.
watch_sockets() {
  while :; do
    for pid in $(pgrep -x RtdbE2EHost || true); do
      lsof -nP -a -p "$pid" -i -F n 2>/dev/null | sed -n 's/^n//p' >>"$gen/sockets.txt" || true
    done
    sleep 0.2
  done
}

sim=$(simulator)
[[ -n "$sim" ]] || { echo "no iPhone simulator available" >&2; exit 1; }

log "starting Aruma with the database and its rules, and generating GoogleService-Info.plist"
start_server "$gen" -rtdb -switch "${ARUMA_E2E_SWITCH_ADDR:-127.0.0.1:0}"
put_rtdb_rules "$root/test/e2e/rtdb-rules.json"
echo "Aruma at $ARUMA_URL, project $ARUMA_PROJECT_ID"

: >"$gen/sockets.txt"
watch_sockets &
watcher=$!

log "FirebaseDatabase + FirebaseAuth (production mode) + ArumaRedirect on simulator $sim"
status=0
# TEST_RUNNER_* reach the tests without the prefix; ARUMA_URL as a build setting becomes ArumaURL
# in the host app's Info.plist.
TEST_RUNNER_ARUMA_URL=$ARUMA_URL TEST_RUNNER_ARUMA_PROJECT_ID=$ARUMA_PROJECT_ID \
  TEST_RUNNER_ARUMA_CLI_TOKEN=$ARUMA_CLI_TOKEN TEST_RUNNER_ARUMA_SWITCH_URL=$ARUMA_SWITCH_URL \
  xcodebuild test -project "$here/RtdbE2E.xcodeproj" -scheme RtdbE2E \
  -destination "platform=iOS Simulator,id=$sim" \
  -clonedSourcePackagesDirPath "$derived/spm" -derivedDataPath "${ARUMA_DERIVED_DATA:-$derived/dd-rtdb-e2e}" \
  -resultBundlePath "$gen/RtdbE2E.xcresult" \
  ARUMA_URL="$ARUMA_URL" "$@" >"$gen/xcodebuild.log" 2>&1 || status=$?
stop_watcher

grep -E "^Test Case|Executed [0-9]+ test|error:|ARUMA_E2E" "$gen/xcodebuild.log" | grep -v '^ld: warning' || true

# The socket half of the guard: every far end is this machine.
port=${ARUMA_URL##*:}
sort -u "$gen/sockets.txt" >"$gen/sockets.unique.txt"
outside=$(grep -- '->' "$gen/sockets.unique.txt" | sed 's/.*->//' | grep -Ev '^(127\.0\.0\.1|\[::1\]|localhost):' || true)
echo "sockets of the host app seen: $(grep -c -- '->' "$gen/sockets.unique.txt" || true), to Aruma's port $port: $(grep -c -- "->127.0.0.1:$port\$" "$gen/sockets.unique.txt" || true)"
if [[ -n "$outside" ]]; then
  echo "sockets of the host app to outside this machine:" >&2
  echo "$outside" >&2
  status=1
fi
grep -q -- "->127.0.0.1:$port\$" "$gen/sockets.unique.txt" \
  || { echo "the socket watcher saw no connection to Aruma: it did not watch the app" >&2; status=1; }

if [[ $status -ne 0 ]]; then
  echo "failed ($status); the last lines of $gen/xcodebuild.log:" >&2
  tail -40 "$gen/xcodebuild.log" >&2
  exit "$status"
fi
log "iOS Realtime Database e2e passed"
