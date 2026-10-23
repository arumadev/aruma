#!/usr/bin/env bash
# S0-B, iOS: FirebaseStorage 12.19.2 in an XCTest bundle on the iOS simulator, one fresh process per
# test (ios/Tests/StoragePathTests.swift), plus GTMSessionUploadFetcher with a background session.
#
#   test/experiments/storage-path/ios.sh [test…]     e.g. testProdSpike testBackgroundUpload
#
#   ARUMA_IOS_SIMULATOR  simulator UDID (default: a booted iPhone)
#   S0PATH_PORT          first stub port (default 8824 here, so the Android runner can use 8820)
#   S0PATH_UPLOAD_URL    self (default) or google: the stub's X-Goog-Upload-URL
#
# The stub's throwaway CA is added to the simulator's trust store (simctl keychain add-root-cert)
# for the https modes; its private key was never written.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
export S0PATH_PORT=${S0PATH_PORT:-8824}
. "$here/lib.sh"
gen=$here/.generated/ios${S0PATH_RUN_SUFFIX:-}
rm -rf "$gen"
mkdir -p "$gen"
trap stop_stub EXIT
derived=$s0_root/../../../.cache/derived-data
[[ -d "$derived" ]] || derived=$s0_root/.cache/derived-data
derived=$(cd "$derived" && pwd)

sim=${ARUMA_IOS_SIMULATOR:-$(xcrun simctl list devices booted -j | python3 -c '
import json, sys
ds = [d for rt, l in json.load(sys.stdin)["devices"].items() if "iOS" in rt for d in l if d["name"].startswith("iPhone")]
print(ds[0]["udid"] if ds else "")')}
[[ -n "$sim" ]] || { echo "boot an iPhone simulator or set ARUMA_IOS_SIMULATOR" >&2; exit 1; }

tests=("$@")
[[ ${#tests[@]} -gt 0 ]] || tests=(testEmulator testProdAsIs testProdSpike testProdSpikeHTTPS
  testBackgroundUpload testBackgroundUploadHTTPS testBackgroundSessionSkipsURLProtocol)

start_stub "$gen" -upload-url "${S0PATH_UPLOAD_URL:-self}"
xcrun simctl keychain "$sim" add-root-cert "$STUB_CA"

(cd "$here/ios" && xcodegen generate --quiet)
echo "==> building for testing on simulator $sim"
xcodebuild build-for-testing -project "$here/ios/StoragePath.xcodeproj" -scheme StoragePath \
  -destination "platform=iOS Simulator,id=$sim" \
  -clonedSourcePackagesDirPath "$derived/s0path-spm" -derivedDataPath "$derived/dd-s0path" \
  >"$gen/build.log" 2>&1 || { tail -30 "$gen/build.log" >&2; exit 1; }

: >"$gen/results.jsonl"
for t in "${tests[@]}"; do
  echo "==> $t"
  echo "{\"marker\":\"$t\"}" >>"$STUB_LOG"
  TEST_RUNNER_STUB_HTTP=$STUB_HTTP TEST_RUNNER_STUB_HTTPS=$STUB_HTTPS \
    xcodebuild test-without-building -project "$here/ios/StoragePath.xcodeproj" -scheme StoragePath \
    -destination "platform=iOS Simulator,id=$sim" -derivedDataPath "$derived/dd-s0path" \
    -only-testing:"StoragePathTests/StoragePathTests/$t" >"$gen/$t.log" 2>&1 || true
  line=$(sed -n 's/^.*S0PATH_RESULT //p' "$gen/$t.log" | head -1)
  if [[ -z "$line" ]]; then
    grep -E 'error|Test Case' "$gen/$t.log" | tail -5 >&2
    line="{\"scenario\":\"$t\",\"results\":[{\"op\":\"xcodebuild\",\"ok\":false,\"detail\":\"no result\"}]}"
  fi
  # The stub log is grouped by test name; results by the scenario they print.
  python3 -c 'import json,sys; d=json.loads(sys.argv[1]); d["scenario"]=sys.argv[2]; print(json.dumps(d))' "$line" "$t" >>"$gen/results.jsonl"
done
python3 "$here/summarize_results.py" "$gen/results.jsonl" "$STUB_LOG" --paths
