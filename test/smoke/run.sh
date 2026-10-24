#!/usr/bin/env bash
# Runs the Phase 1 smoke tests (AC-22..26): official SDKs load the configuration files a real
# Aruma server generates, with all network access blocked.
#
#   test/smoke/run.sh [web|admin-node|admin-go|admin-auth-node|admin-auth-go|tamper-libs|android|ios|flutter|flutter-ios
#                      |admin-node-rtdb|admin-go-rtdb|admin-node-storage|admin-go-storage ...]
#
# admin-auth-node and admin-auth-go run the Aruma verification libraries' suites (sdk/), which
# start their own live server and use the official firebase-admin beside them (AC-A5, AC-A10..14,
# AC-A20). tamper-libs puts the tamper suite's forged ID tokens to both libraries too (T24).
#
# The -rtdb targets use the Admin SDKs' Realtime Database against a live Aruma in production
# form: each starts genconfig -serve -rtdb-domain rtdb.aruma.test with its TLS front, so
# databaseURL is https://smoke-app-default-rtdb.rtdb.aruma.test, and refuses every connection
# beyond loopback. firebase-admin (Node) speaks the WebSocket protocol, firebase-admin-go REST.
# ARUMA_SMOKE_ADDR and ARUMA_SMOKE_TLS_ADDR set the server's and the front's addresses (default:
# any free loopback port), ARUMA_SMOKE_DB_PREFIX the throwaway database's prefix. The Python
# Admin SDK is not covered: see docs/compat/rtdb-v0.2.md.
#
# The -storage targets run the Aruma Admin Storage packages (sdk/node/admin-storage,
# sdk/go/adminstorage): their own suites, then the official @google-cloud/storage and
# cloud.google.com/go/storage through them against a live Aruma with Cloud Storage, once on
# loopback and once behind genconfig's TLS front at https://storage.aruma.test, with every
# connection beyond loopback refused. ARUMA_SMOKE_ADDR, ARUMA_SMOKE_TLS_ADDR and
# ARUMA_SMOKE_DB_PREFIX apply to them as well.
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
  (cd "$smoke/admin-go" && go test -count=1 .)
}

run_admin_node_rtdb() {
  log "admin-node-rtdb: firebase-admin Realtime Database (WebSocket) through TLS"
  (cd "$smoke/admin-node" && npm ci --no-audit --no-fund --silent && npm run --silent test:rtdb)
}

run_admin_go_rtdb() {
  log "admin-go-rtdb: firebase-admin-go Realtime Database (REST) through TLS"
  (cd "$smoke/admin-go" && go test -count=1 ./rtdb/)
}

run_admin_node_storage() {
  log "admin-node-storage: @aruma/admin-storage with @google-cloud/storage, loopback and TLS"
  (cd "$root/sdk/node/admin-storage" && npm ci --no-audit --no-fund --silent && npm test)
  (cd "$smoke/admin-node" && npm ci --no-audit --no-fund --silent && npm run --silent test:storage)
}

run_admin_go_storage() {
  log "admin-go-storage: adminstorage with cloud.google.com/go/storage, loopback and TLS"
  (cd "$root/sdk/go/adminstorage" && go test -count=1 ./...)
  (cd "$smoke/admin-go" && go test -count=1 ./storage/)
}

run_admin_auth_node() {
  log "admin-auth-node: @aruma/admin-auth with firebase-admin"
  (cd "$root/sdk/node/admin-auth" && npm ci --no-audit --no-fund --silent && npm test)
}

run_admin_auth_go() {
  log "admin-auth-go: adminauth with firebase-admin-go"
  (cd "$root/sdk/go/adminauth" && go test -count=1 ./...)
}

run_tamper_libs() {
  log "tamper-libs: forged ID tokens against @aruma/admin-auth and adminauth"
  (cd "$root/sdk/node/admin-auth" && npm ci --no-audit --no-fund --silent)
  (cd "$root/sdk/go/adminauth" && go build -o "$gen/tamperverify" ./internal/tamperverify)
  ARUMA_VERIFY_GO="$gen/tamperverify" ARUMA_VERIFY_NODE="node $root/sdk/node/admin-auth/test/tamper-verify.js" \
    go test -count=1 -run 'TestAuthTamper' ./test/acceptance/
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
  targets=(web admin-node admin-go admin-auth-node admin-auth-go tamper-libs admin-node-rtdb admin-go-rtdb
    admin-node-storage admin-go-storage)
  command -v java >/dev/null && [[ -d "${ANDROID_HOME:-$HOME/Library/Android/sdk}" ]] && targets+=(android)
  command -v xcodebuild >/dev/null && targets+=(ios)
  command -v flutter >/dev/null && targets+=(flutter)
  command -v flutter >/dev/null && command -v xcodebuild >/dev/null && targets+=(flutter-ios)
fi

# The -rtdb and -storage targets start servers of their own.
for t in "${targets[@]}"; do
  if [[ $t != *-rtdb && $t != *-storage ]]; then
    generate
    break
  fi
done
for t in "${targets[@]}"; do
  "run_${t//-/_}"
done
log "smoke tests passed: ${targets[*]}"
