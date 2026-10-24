#!/usr/bin/env bash
# Runs the iOS Cloud Storage end-to-end tests on the production path of ADR-022 §16.3: the app asks
# SwiftPM for the OFFICIAL firebase-ios-sdk URL at exactVersion 12.19.3, and `swift package config
# set-mirror` sends it to the Aruma mirror, whose tag 12.19.3 is the patched FirebaseStorage
# (sdk/ios/firebase-storage-patch). FirebaseStorage then reads ArumaURL from the host app's
# Info.plist (given with its host in upper case: the download URL must carry it in lower case);
# FirebaseAuth reaches Aruma through ArumaRedirect, whose Storage fallback is off
# (ArumaStorageRedirect=false). The server runs with Cloud Storage and the rules of storage.rules.
#
# In order (SwiftPM, the default):
#   1. The mirror: built with make-mirror.sh (and FirebaseStorage's unit tests) unless it already
#      has the tag.
#   2. set-mirror, into the project's xcshareddata/swiftpm/configuration/mirrors.json.
#   3. Resolution: Package.resolved and the firebase-ios-sdk checkout must be the mirror's commit.
#   4. The tests (StorageE2ETests): the eleven operations with the download URL in the unified
#      form, names that round-trip through their links, and putFile with a background session.
#
# With --cocoapods, steps 1-3 are instead (cocoapods/, FirebaseStorage 12.19.0, the last release on
# CocoaPods, patched by sdk/ios/ArumaRedirect/Scripts/aruma_storage.rb in post_install):
#   1. The helper's own tests.
#   2. `pod install` with a patch one of whose lines is not in FirebaseStorage 12.19.0: it must fail.
#   3. `pod install`, then again (nothing to change): the pod is patched and the new file compiled.
# and step 4 runs the same tests in that app.
#
# The network guard has two halves: LoopbackGuard refuses and reports every URLSession request that
# neither goes to this machine nor is taken by ArumaRedirect (and the tests fail on any Storage
# request to Google), and this script samples the host app's sockets (lsof); the run fails on any
# far end off this machine. Background uploads run in the simulator's nsurlsessiond, which also
# talks to Apple for itself: its sockets are listed, not judged.
#
#   test/e2e/ios-storage/run.sh [--cocoapods] [xcodebuild arguments, e.g. -only-testing:StorageE2ETests/StorageE2ETests/testElevenOperations]
#
#   ARUMA_FIREBASE_VERSION  the official version the app asks for (default 12.19.3, as project.yml)
#   ARUMA_IOS_STORAGE_CACHE the mirror, its build, Swift package checkouts and DerivedData
#                           (default <repo>/.cache/ios-storage)
#   ARUMA_MIRROR            the mirror repository (default: a local one under the cache, built here)
#   ARUMA_IOS_SIMULATOR     simulator UDID (default: a booted iPhone, else the first available one)
#   ARUMA_E2E_ADDR, ARUMA_E2E_DB_PREFIX  see ../server.sh
#
# It needs Xcode, git, the local Postgres (ARUMA_TEST_DATABASE_URL), Go, curl and python3; with
# --cocoapods, CocoaPods, XcodeGen and ruby.
set -euo pipefail

mode=spm
if [[ "${1:-}" == --cocoapods ]]; then
  mode=cocoapods
  shift
fi
here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../../.." && pwd)
gen=$here/.generated
version=${ARUMA_FIREBASE_VERSION:-12.19.3}
cache=${ARUMA_IOS_STORAGE_CACHE:-$root/.cache/ios-storage}
mirror=${ARUMA_MIRROR:-$cache/mirror/firebase-ios-sdk.git}
official=https://github.com/firebase/firebase-ios-sdk
project=$here/StorageE2E.xcodeproj
swiftpm_config=$project/project.xcworkspace/xcshareddata/swiftpm
pods=$here/cocoapods
cd "$root"
# shellcheck source=../server.sh
. "$root/test/e2e/server.sh"

log() { printf '\n==> %s\n' "$*"; }

watcher= booted_here=
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
  [[ -n "$booted_here" ]] && xcrun simctl shutdown "$booted_here" >/dev/null 2>&1 || true
}
trap cleanup EXIT

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

# watch_sockets appends the internet sockets of the running host app (`app local->remote`) and of
# the simulator's nsurlsessiond (`nsurlsessiond local->remote`) to sockets.txt, every 200 ms.
watch_sockets() {
  while :; do
    for pid in $(pgrep -x StorageE2EHost || true); do
      lsof -nP -a -p "$pid" -i -F n 2>/dev/null | sed -n 's/^n/app /p' >>"$gen/sockets.txt" || true
    done
    for pid in $(pgrep -f 'CoreSimulator.*/nsurlsessiond' || true); do
      lsof -nP -a -p "$pid" -i -F n 2>/dev/null | sed -n 's/^n/nsurlsessiond /p' >>"$gen/sockets.txt" || true
    done
    sleep 0.2
  done
}

sim=$(simulator)
[[ -n "$sim" ]] || { echo "no iPhone simulator available" >&2; exit 1; }
xcrun simctl list devices -j | python3 -c 'import json, sys
sys.exit(0 if any(d["udid"] == sys.argv[1] and d["state"] == "Booted" for ds in json.load(sys.stdin)["devices"].values() for d in ds) else 1)' "$sim" \
  || booted_here=$sim
rm -rf "$gen"
mkdir -p "$gen"

if [[ $mode == spm ]]; then
  log "1. the mirror ($mirror) at $version"
  if git ls-remote --tags "$mirror" "refs/tags/$version" 2>/dev/null | grep -q .; then
    echo "the mirror already has $version"
  else
    ARUMA_MIRROR=$mirror ARUMA_MIRROR_WORK=$cache/mirror "$root/sdk/ios/firebase-storage-patch/make-mirror.sh" "$version"
  fi
  mirror_commit=$(git ls-remote --tags "$mirror" "refs/tags/$version^{}" | cut -f1)
  [[ -n "$mirror_commit" ]] || { echo "the mirror has no tag $version" >&2; exit 1; }
  echo "mirror $version is $mirror_commit"
  mirror_url=$mirror
  [[ "$mirror_url" == *://* ]] || mirror_url=file://$mirror_url

  log "2. swift package config set-mirror --original $official --mirror $mirror_url"
  mkdir -p "$gen/mirror-config"
  printf '// swift-tools-version:5.9\nimport PackageDescription\nlet package = Package(name: "MirrorConfig")\n' >"$gen/mirror-config/Package.swift"
  swift package --package-path "$gen/mirror-config" config set-mirror --original "$official" --mirror "$mirror_url"
  mkdir -p "$swiftpm_config/configuration"
  cp "$gen/mirror-config/.swiftpm/configuration/mirrors.json" "$swiftpm_config/configuration/mirrors.json"
  # Pins of an earlier resolution name revisions of another build: resolve afresh. The checkouts'
  # state counts as a preference too (it kept GTMSessionFetcher at 3.5.0 once), so it goes as well.
  rm -f "$swiftpm_config/Package.resolved" "$cache/spm/workspace-state.json"
  xcb=(-project "$project" -scheme StorageE2E -clonedSourcePackagesDirPath "$cache/spm"
    -packageCachePath "$cache/spm-cache" -derivedDataPath "$cache/dd")

  log "3. resolving $official exactVersion $version"
  xcodebuild -resolvePackageDependencies "${xcb[@]}" >"$gen/resolve.log" 2>&1 || { tail -30 "$gen/resolve.log" >&2; exit 1; }
  checkout=$cache/spm/checkouts/firebase-ios-sdk
  pinned=$(python3 -c 'import json, sys
pins = json.load(open(sys.argv[1]))["pins"]
p = [p for p in pins if p["identity"] == "firebase-ios-sdk"][0]
print(p["location"], p["state"].get("version"), p["state"]["revision"])' "$swiftpm_config/Package.resolved")
  echo "Package.resolved: $pinned"
  [[ "$pinned" == *" $version $mirror_commit" ]] \
    || { echo "firebase-ios-sdk was not resolved to the mirror's $version ($mirror_commit)" >&2; exit 1; }
  [[ -f "$checkout/ARUMA-NOTICE.md" && -f "$checkout/FirebaseStorage/Sources/Internal/ArumaStorageOrigin.swift" ]] \
    || { echo "the firebase-ios-sdk checkout is not the patched one" >&2; exit 1; }
  echo "the checkout is the patched FirebaseStorage"
else
  # CocoaPods' own home and cache (v03-brief rule 7).
  unset CP_HOME_DIR CP_CACHE_DIR
  pod_version=12.19.0
  storage_pod=$pods/Pods/FirebaseStorage

  log "1. the helper's own tests"
  ruby "$root/sdk/ios/ArumaRedirect/Scripts/aruma_storage_test.rb" | tail -1

  log "2. pod install with a patch line that FirebaseStorage $pod_version does not have"
  (cd "$pods" && xcodegen generate --quiet)
  sed 's/^ *  var scheme: String$/   var scheme: String \/\/ not in any release/' \
    "$root/sdk/ios/firebase-storage-patch/storage-host.patch" >"$gen/missing-line.patch"
  ! cmp -s "$gen/missing-line.patch" "$root/sdk/ios/firebase-storage-patch/storage-host.patch" \
    || { echo "could not make the broken patch" >&2; exit 1; }
  if (cd "$pods" && ARUMA_STORAGE_PATCH=$gen/missing-line.patch pod install) >"$gen/pod-install-missing.log" 2>&1; then
    echo "pod install passed with a line missing from FirebaseStorage" >&2
    exit 1
  fi
  grep -q "ArumaStorage: FirebaseStorage/Sources/Storage.swift has 0 copies of these lines" "$gen/pod-install-missing.log" \
    || { tail -20 "$gen/pod-install-missing.log" >&2; echo "pod install failed, but not on the helper" >&2; exit 1; }
  echo "pod install failed: $(grep -o 'ArumaStorage: [^,]*' "$gen/pod-install-missing.log" | head -1)"
  ! grep -q ArumaStorageOrigin "$storage_pod/FirebaseStorage/Sources/Storage.swift" \
    || { echo "the failed pod install left Storage.swift patched" >&2; exit 1; }

  log "3. pod install (FirebaseStorage $pod_version), then again"
  (cd "$pods" && pod install) >"$gen/pod-install.log" 2>&1 || { tail -30 "$gen/pod-install.log" >&2; exit 1; }
  grep "ArumaStorage:" "$gen/pod-install.log"
  grep -q "FirebaseStorage ($pod_version)" "$pods/Podfile.lock" || { echo "FirebaseStorage is not $pod_version" >&2; exit 1; }
  grep -q "let origin = ArumaStorageOrigin.main" "$storage_pod/FirebaseStorage/Sources/Storage.swift" \
    && grep -q "ArumaStorageOrigin.swift in Sources" "$pods/Pods/Pods.xcodeproj/project.pbxproj" \
    || { echo "the FirebaseStorage pod is not patched, or ArumaStorageOrigin.swift is not compiled" >&2; exit 1; }
  sum=$(cd "$storage_pod" && find FirebaseStorage -type f -print0 | sort -z | xargs -0 shasum | shasum)
  (cd "$pods" && pod install) >"$gen/pod-install-again.log" 2>&1 || { tail -30 "$gen/pod-install-again.log" >&2; exit 1; }
  [[ "$(cd "$storage_pod" && find FirebaseStorage -type f -print0 | sort -z | xargs -0 shasum | shasum)" == "$sum" ]] \
    && grep -q "ArumaStorageOrigin.swift in Sources" "$pods/Pods/Pods.xcodeproj/project.pbxproj" \
    || { echo "a second pod install changed the patched pod" >&2; exit 1; }
  echo "the pod is patched, ArumaStorageOrigin.swift is in the FirebaseStorage target, and a second pod install changed nothing"
  xcb=(-workspace "$pods/StorageE2EPods.xcworkspace" -scheme StorageE2E -derivedDataPath "$cache/dd-pods")
fi

log "starting Aruma with Cloud Storage and its rules, and generating GoogleService-Info.plist"
start_server "$gen/server" -storage -anonymous -log warn
cp "$gen/server/GoogleService-Info.plist" "$gen/GoogleService-Info.plist"
python3 -c 'import json, sys; print(json.dumps({"source": open(sys.argv[1]).read()}))' "$here/storage.rules" \
  | curl -fsS -X PUT -H "Authorization: Bearer $ARUMA_CLI_TOKEN" -H "Content-Type: application/json" --data-binary @- \
    "$ARUMA_URL/console/v1/projects/$ARUMA_PROJECT_ID/storage/rules" >/dev/null
port=${ARUMA_URL##*:}
storage_url="http://LOCALHOST:$port"
echo "Aruma at $ARUMA_URL, project $ARUMA_PROJECT_ID; ArumaURL $storage_url"

: >"$gen/sockets.txt"
watch_sockets &
watcher=$!

log "4. FirebaseStorage ($mode) + FirebaseAuth + ArumaRedirect on simulator $sim"
status=0
# TEST_RUNNER_* reach the tests without the prefix; ARUMA_URL as a build setting becomes ArumaURL
# in the host app's Info.plist.
TEST_RUNNER_ARUMA_URL=$ARUMA_URL TEST_RUNNER_ARUMA_STORAGE_URL=$storage_url TEST_RUNNER_ARUMA_PROJECT_ID=$ARUMA_PROJECT_ID \
  xcodebuild test "${xcb[@]}" -destination "platform=iOS Simulator,id=$sim" \
  -resultBundlePath "$gen/StorageE2E.xcresult" \
  ARUMA_URL="$storage_url" "$@" >"$gen/xcodebuild.log" 2>&1 || status=$?
stop_watcher

grep -E "^Test Case|Executed [0-9]+ test|error:|ARUMA_E2E |FirebaseStorage \(Aruma build\)" "$gen/xcodebuild.log" \
  | grep -v '^ld: warning' | sed 's/^.*\(FirebaseStorage (Aruma build)\)/\1/' || true
sed -n 's/^.*ARUMA_E2E_UPDATE_METADATA //p' "$gen/xcodebuild.log" >"$gen/update-metadata.jsonl"
echo "updateMetadata requests recorded in $gen/update-metadata.jsonl:"
cat "$gen/update-metadata.jsonl"
grep -q "FirebaseStorage (Aruma build): Storage uses http://localhost:$port (ArumaURL)" "$gen/xcodebuild.log" \
  || echo "note: the patched FirebaseStorage's log line did not reach xcodebuild's output"

# The socket half of the guard: every far end of the app is this machine.
sort -u "$gen/sockets.txt" >"$gen/sockets.unique.txt"
far() { grep "^$1 " "$gen/sockets.unique.txt" | grep -- '->' | sed 's/.*->//' || true; }
outside() { far "$1" | grep -Ev '^(127\.0\.0\.1|\[::1\]|localhost):' || true; }
echo "app sockets: $(far app | grep -c . || true), to Aruma's port $port: $(far app | grep -c "^127.0.0.1:$port\$" || true)"
echo "nsurlsessiond sockets to Aruma's port $port: $(far nsurlsessiond | grep -c "^127.0.0.1:$port\$" || true); to Apple and others (its own): $(outside nsurlsessiond | sort -u | tr '\n' ' ')"
if [[ -n "$(outside app)" ]]; then
  echo "sockets of the app to outside this machine:" >&2
  outside app >&2
  status=1
fi
far app | grep -q "^127.0.0.1:$port\$" \
  || { echo "the socket watcher saw no connection of the app to Aruma: it did not watch it" >&2; status=1; }
# A 1 MB chunk on loopback takes milliseconds, so sampling may miss nsurlsessiond's connection;
# the test itself checks the chunk fetcher's background session and URL, and the server's answer.

if [[ $status -ne 0 ]]; then
  echo "failed ($status); the last lines of $gen/xcodebuild.log:" >&2
  tail -40 "$gen/xcodebuild.log" >&2
  exit "$status"
fi
log "iOS Cloud Storage e2e ($mode) passed"
