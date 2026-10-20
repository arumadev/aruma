#!/usr/bin/env bash
# Runs the iOS Auth end-to-end tests: the official FirebaseAuth in production mode (no useEmulator)
# in an XCTest bundle hosted by an empty app on an iOS simulator, configured only with the
# GoogleService-Info.plist a real Aruma server generates and sent to that server by ArumaRedirect
# (ArumaURL in the app's Info.plist). LoopbackGuard refuses and reports every other request.
#
#   test/e2e/ios-auth/run.sh [xcodebuild arguments, e.g. -only-testing:AuthE2ETests/AuthE2ETests/testAnonymous]
#
#   ARUMA_IOS_SIMULATOR  simulator UDID (default: a booted iPhone, else the first available one)
#   ARUMA_XCODE_DIR      Swift package checkouts and DerivedData (default .cache/xcode)
#   ARUMA_E2E_ADDR, ARUMA_E2E_DB_PREFIX  see ../server.sh
#
# It needs Xcode, the local Postgres (ARUMA_TEST_DATABASE_URL), Go and python3. The same script runs
# in Codemagic (codemagic.yaml, workflow ios-auth).
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../../.." && pwd)
gen=$here/.generated
derived=${ARUMA_XCODE_DIR:-$root/.cache/xcode}
cd "$root"
# shellcheck source=../server.sh
. "$root/test/e2e/server.sh"
trap stop_server EXIT

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

sim=$(simulator)
[[ -n "$sim" ]] || { echo "no iPhone simulator available" >&2; exit 1; }

log "starting Aruma and generating GoogleService-Info.plist"
start_server "$gen"
echo "Aruma at $ARUMA_URL, project $ARUMA_PROJECT_ID"

log "FirebaseAuth (production mode) + ArumaRedirect on simulator $sim"
status=0
# TEST_RUNNER_* reach the tests without the prefix; ARUMA_URL as a build setting becomes ArumaURL
# in the host app's Info.plist.
TEST_RUNNER_ARUMA_URL=$ARUMA_URL TEST_RUNNER_ARUMA_PROJECT_ID=$ARUMA_PROJECT_ID \
  TEST_RUNNER_ARUMA_CLI_TOKEN=$ARUMA_CLI_TOKEN \
  xcodebuild test -project "$here/AuthE2E.xcodeproj" -scheme AuthE2E \
  -destination "platform=iOS Simulator,id=$sim" \
  -clonedSourcePackagesDirPath "$derived/spm" -derivedDataPath "$derived/dd-auth-e2e" \
  -resultBundlePath "$gen/AuthE2E.xcresult" \
  ARUMA_URL="$ARUMA_URL" "$@" >"$gen/xcodebuild.log" 2>&1 || status=$?

grep -E "^Test Case|Executed [0-9]+ test|error:|ARUMA_E2E" "$gen/xcodebuild.log" | grep -v '^ld: warning' || true
if [[ $status -ne 0 ]]; then
  echo "xcodebuild failed ($status); the last lines of $gen/xcodebuild.log:" >&2
  tail -40 "$gen/xcodebuild.log" >&2
  exit "$status"
fi
log "iOS Auth e2e passed"
