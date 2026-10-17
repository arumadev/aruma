#!/usr/bin/env bash
# Runs the Phase 1 smoke tests (AC-22..26): official SDKs load the configuration files a real
# Aruma server generates, with all network access blocked.
#
#   test/smoke/run.sh [web|admin-node|admin-go|android|ios|flutter|flutter-ios ...]
#
# Without arguments it runs every target this machine can run. It needs the local Postgres
# (ARUMA_TEST_DATABASE_URL), Node, Go and, per target, a JDK 17+ and the Android SDK, Xcode with an
# iOS simulator, or Flutter.
set -euo pipefail

root=$(cd "$(dirname "$0")/../.." && pwd)
smoke=$root/test/smoke
gen=$smoke/.generated
cd "$root"

log() { printf '\n==> %s\n' "$*"; }

generate() {
  log "generating config files from a fresh Aruma server"
  rm -rf "$gen"
  go run ./test/smoke/genconfig -out "$gen"
}

run_web() {
  log "web: firebase 11, 12, 13"
  (cd "$smoke/web" && npm ci --no-audit --no-fund --silent && npm test)
}

run_admin_node() {
  log "admin-node: firebase-admin"
  (cd "$smoke/admin-node" && npm ci --no-audit --no-fund --silent && npm test)
}

run_admin_go() {
  log "admin-go: firebase-admin-go"
  (cd "$smoke/admin-go" && go test -count=1 ./...)
}

run_android() {
  log "android: google-services plugin + Robolectric"
  : "${ANDROID_HOME:=$HOME/Library/Android/sdk}"
  export ANDROID_HOME
  cp "$gen/google-services.json" "$smoke/android/app/"
  (cd "$smoke/android" && ./gradlew --no-daemon -q :app:assembleDebug :app:testDebugUnitTest)
}

# simulator picks an available iPhone simulator, or uses ARUMA_IOS_SIMULATOR.
simulator() {
  if [[ -n "${ARUMA_IOS_SIMULATOR:-}" ]]; then
    echo "$ARUMA_IOS_SIMULATOR"
    return
  fi
  xcrun simctl list devices available -j | python3 -c '
import json, sys
devices = [d for rt, ds in json.load(sys.stdin)["devices"].items() if "iOS" in rt for d in ds if d["name"].startswith("iPhone")]
print(devices[0]["udid"] if devices else "")'
}

run_ios() {
  log "ios: FirebaseCore + FirebaseInstallations on the simulator"
  local sim derived=${ARUMA_XCODE_DIR:-$root/.cache/xcode}
  sim=$(simulator)
  [[ -n "$sim" ]] || { echo "no iPhone simulator available" >&2; return 1; }
  cp "$gen/GoogleService-Info.plist" "$gen/expected.json" "$smoke/ios/Tests/ArumaSmokeTests/Fixtures/"
  (cd "$smoke/ios" && xcodebuild test -scheme ArumaSmokeIOS-Package \
    -destination "platform=iOS Simulator,id=$sim,arch=arm64" \
    -clonedSourcePackagesDirPath "$derived/spm" -derivedDataPath "$derived/dd" -quiet)
}

prepare_flutter() {
  cp "$gen/firebase_options.dart" "$smoke/flutter/lib/"
  cp "$gen/expected.json" "$smoke/flutter/"
  (cd "$smoke/flutter" && flutter pub get >/dev/null)
}

run_flutter() {
  log "flutter: firebase_options.dart with firebase_core (Dart level)"
  prepare_flutter
  (cd "$smoke/flutter" && flutter analyze --no-pub && flutter test --no-pub)
}

run_flutter_ios() {
  log "flutter-ios: Firebase.initializeApp through the native iOS SDK"
  prepare_flutter
  local sim
  sim=$(simulator)
  (
    cd "$smoke/flutter"
    if [[ ! -d ios ]]; then
      flutter create --platforms=ios --org com.aruma --project-name smoke . >/dev/null
      # Drop the template files flutter create adds next to ours.
      rm -rf test/widget_test.dart README.md .idea smoke.iml analysis_options.yaml
      # firebase_core needs iOS 15. Newer Flutter releases use Swift Package Manager and write
      # no Podfile; the project setting covers both.
      if [[ -f ios/Podfile ]]; then
        sed -i '' "s/^# platform :ios, '[0-9.]*'/platform :ios, '15.0'/" ios/Podfile
      fi
      sed -i '' 's/IPHONEOS_DEPLOYMENT_TARGET = [0-9.]*;/IPHONEOS_DEPLOYMENT_TARGET = 15.0;/' ios/Runner.xcodeproj/project.pbxproj
    fi
    flutter test integration_test -d "$sim"
  )
}

targets=("$@")
if [[ ${#targets[@]} -eq 0 ]]; then
  targets=(web admin-node admin-go)
  command -v java >/dev/null && [[ -d "${ANDROID_HOME:-$HOME/Library/Android/sdk}" ]] && targets+=(android)
  command -v xcodebuild >/dev/null && targets+=(ios)
  command -v flutter >/dev/null && targets+=(flutter)
  command -v flutter >/dev/null && command -v xcodebuild >/dev/null && targets+=(flutter-ios)
fi

generate
for t in "${targets[@]}"; do
  "run_${t//-/_}"
done
log "smoke tests passed: ${targets[*]}"
