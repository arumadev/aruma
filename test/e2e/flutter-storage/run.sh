#!/usr/bin/env bash
# Runs the Flutter Cloud Storage end-to-end test on one platform: the official firebase_storage and
# firebase_auth (pinned in app/pubspec.yaml) in production mode (no useStorageEmulator, no
# useAuthEmulator), configured only with the firebase_options.dart a real Aruma server generates,
# on the production path of ADR-022 §16.4: the native SDKs built for Aruma.
#
#   android  firebase-storage built with the Gradle plugin dev.aruma.storage (sdk/android/aruma-storage-gradle),
#            Auth through aruma-redirect; a debug build and a release build (R8), each on the device.
#   ios      FirebaseStorage from the patched mirror (sdk/ios/firebase-storage-patch), reached
#            through `swift package config set-mirror` (FlutterFire names the official URL with an
#            exact version), Auth through ArumaRedirect; a debug build on the simulator.
#
# In both, ArumaStorageRedirect is false: the run-time fallback is off, so the build-time rewrite
# is the only way Storage can reach Aruma. The server listens on 127.0.0.1 (on Android the device
# gets the same address through `adb reverse`), and the native SDKs are given http://127.0.0.1:<port>:
# a host firebase_storage's Dart parser does not take as Firebase's (it takes localhost and
# 10.0.2.2 as the emulator's), as a production host would not be.
#
# The app (app/lib) runs its scenario at launch: the operations, Image.network and dart:io on the
# download URL, names that round-trip through refFromURL(fromArumaUrl(link)), names fromArumaUrl
# must refuse, and the guards. This script reads its result lines.
#
# The network guard, by layer:
#   Dart      lib/guard.dart: IOOverrides refuses and records every socket Dart opens off this
#             device (dart:io's HttpClient, so Image.network); it never sees the native SDKs.
#   Android   the e2e plugin records every HttpURLConnection off loopback (ProxySelector), and
#             iptables on the device rejects every packet of the app's uid that leaves loopback,
#             Dart's sockets included: the run fails if that counter moved.
#   iOS       the e2e plugin's URLProtocol refuses every URLSession request off this machine that
#             ArumaRedirect does not take (Auth only: a Storage one fails the run), and this script
#             samples the app's sockets with lsof every 200 ms (a second look, not proof).
#
#   test/e2e/flutter-storage/run.sh android|ios
#
#   ANDROID_SERIAL      the device (default: the only one connected)
#   ANDROID_HOME        the Android SDK (default /Volumes/Work/android-sdk)
#   ARUMA_E2E_NDK_DIR   an installed NDK of Flutter's version (default: found under ANDROID_HOME or
#                       ~/Library/Android/sdk), so that Gradle does not download one
#   ARUMA_E2E_BUILD_TYPES  Android build types (default "debug release")
#   ARUMA_E2E_TARGET_PLATFORM  the device's ABI for flutter build apk (default android-arm64)
#   ARUMA_MIRROR        the firebase-ios-sdk mirror with the tag 12.19.3 (default: a local one under
#                       the cache, built with make-mirror.sh)
#   ARUMA_FLUTTER_STORAGE_CACHE  the mirror, SwiftPM checkouts and caches, DerivedData (default
#                       <repo>/.cache/flutter-storage)
#   ARUMA_IOS_SIMULATOR simulator UDID (default: a booted iPhone, else the first available one)
#   ARUMA_E2E_ADDR, ARUMA_E2E_DB_PREFIX  see ../server.sh
#
# It needs Flutter (PUB_CACHE where you want it), the local Postgres (ARUMA_TEST_DATABASE_URL), Go,
# curl and python3; for Android a JDK 17+ and adb; for iOS Xcode and git.
set -euo pipefail

platform=${1:-}
[[ "$platform" == android || "$platform" == ios ]] || { echo "usage: $0 android|ios" >&2; exit 2; }
here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../../.." && pwd)
app=$here/app
gen=$here/.generated/$platform
cache=${ARUMA_FLUTTER_STORAGE_CACHE:-$root/.cache/flutter-storage}
version=12.19.3 # firebase-ios-sdk, as firebase_core 4.15.2 asks for it
cd "$root"
# shellcheck source=../server.sh
. "$root/test/e2e/server.sh"

log() { printf '\n==> %s\n' "$*"; }
notice=META-INF/aruma/firebase-storage-MODIFIED.txt
: "${ANDROID_HOME:=/Volumes/Work/android-sdk}"
export ANDROID_HOME
adb=$ANDROID_HOME/platform-tools/adb
app_id=com.aruma.smoke

uid= port= guarded= was_root= watcher= booted_here= sim=
cleanup() {
  set +e
  if [[ -n "$watcher" ]]; then kill "$watcher" 2>/dev/null; wait "$watcher" 2>/dev/null; fi
  if [[ $platform == android && -n "${ANDROID_SERIAL:-}" ]]; then
    if [[ -n "$guarded" ]]; then
      "$adb" shell "for T in iptables ip6tables; do \$T -D OUTPUT -m owner --uid-owner $uid -j aruma_flutter; \$T -F aruma_flutter; \$T -X aruma_flutter; done" >/dev/null 2>&1
    fi
    [[ -n "$port" ]] && "$adb" reverse --remove "tcp:$port" >/dev/null 2>&1
    "$adb" uninstall "$app_id" >/dev/null 2>&1
    [[ "$was_root" == no ]] && "$adb" unroot >/dev/null 2>&1
  fi
  if [[ $platform == ios && -n "$sim" ]]; then
    xcrun simctl terminate "$sim" "$app_id" >/dev/null 2>&1
    xcrun simctl uninstall "$sim" "$app_id" >/dev/null 2>&1
    [[ -n "$booted_here" ]] && xcrun simctl shutdown "$booted_here" >/dev/null 2>&1
  fi
  [[ -x "$app/android/gradlew" ]] && (cd "$app/android" && ./gradlew --stop >/dev/null 2>&1)
  stop_server
}
trap cleanup EXIT

# report <results.json>: prints the app's result lines and fails unless every step passed.
report() {
  python3 - "$1" <<'EOF'
import json, sys
r = json.load(open(sys.argv[1]))
for group, lines in r["groups"].items():
    print(f"  {group}:")
    for l in lines:
        print(f"    {l}")
if r.get("fatal"):
    print("  fatal: " + r["fatal"])
print(f"  {r['mode']}: {r['passed']}/{r['total']} steps passed")
sys.exit(0 if r["ok"] else 1)
EOF
}

rm -rf "$gen"
mkdir -p "$gen" "$cache"

if [[ $platform == android ]]; then
  if [[ -z "${ANDROID_SERIAL:-}" ]]; then
    devices=$("$adb" devices | awk 'NR > 1 && $2 == "device" {print $1}')
    [[ $(printf '%s\n' "$devices" | grep -c .) -eq 1 ]] || { echo "connect one device or set ANDROID_SERIAL (found: ${devices:-none})" >&2; exit 1; }
    ANDROID_SERIAL=$devices
  fi
  export ANDROID_SERIAL
  ndk=${ARUMA_E2E_NDK_DIR:-}
  if [[ -z "$ndk" ]]; then
    want=$(sed -n 's/.*val ndkVersion: String = "\(.*\)".*/\1/p' "$(dirname "$(dirname "$(command -v flutter)")")/packages/flutter_tools/gradle/src/main/kotlin/FlutterExtension.kt" || true)
    for d in "$ANDROID_HOME/ndk/$want" "$HOME/Library/Android/sdk/ndk/$want"; do
      [[ -n "$want" && -d "$d" ]] && { ndk=$d; break; }
    done
  fi
else
  sim=${ARUMA_IOS_SIMULATOR:-$(xcrun simctl list devices available -j | python3 -c '
import json, sys
devices = [d for rt, ds in json.load(sys.stdin)["devices"].items() if "iOS" in rt for d in ds if d["name"].startswith("iPhone")]
booted = [d for d in devices if d["state"] == "Booted"]
print((booted or devices)[0]["udid"] if devices else "")')}
  [[ -n "$sim" ]] || { echo "no iPhone simulator available" >&2; exit 1; }
fi

log "starting Aruma with Cloud Storage and its rules, and generating firebase_options.dart"
start_server "$gen/server" -storage -anonymous -log warn
port=${ARUMA_URL##*:}
storage_url="http://127.0.0.1:$port"
python3 -c 'import json, sys; print(json.dumps({"source": open(sys.argv[1]).read()}))' "$here/storage.rules" \
  | curl -fsS -X PUT -H "Authorization: Bearer $ARUMA_CLI_TOKEN" -H "Content-Type: application/json" --data-binary @- \
    "$ARUMA_URL/console/v1/projects/$ARUMA_PROJECT_ID/storage/rules" >/dev/null
cp "$gen/server/firebase_options.dart" "$app/lib/firebase_options.dart"
echo "Aruma at $ARUMA_URL, project $ARUMA_PROJECT_ID; the native SDKs get $storage_url"

log "flutter create --platforms $platform, then prepare.py"
rm -rf "${app:?}/$platform"
(cd "$app" && flutter create --platforms "$platform" --org com.aruma --project-name aruma_flutter_storage_e2e . >"$gen/create.log")
if [[ $platform == android ]]; then
  python3 "$here/prepare.py" android "$app" "$storage_url" "$ndk"
else
  python3 "$here/prepare.py" ios "$app" "$storage_url"
fi
(cd "$app" && flutter pub get >"$gen/pub.log")
python3 - "$app/pubspec.lock" <<'EOF'
import re, sys
s = open(sys.argv[1]).read()
print("pinned: " + ", ".join(
    "%s %s" % (n, (re.search(r"\n  %s:\n(?:    .*\n)*?    version: \"([^\"]+)\"" % n, s) or [None, "?"])[1])
    for n in ("firebase_core", "firebase_auth", "firebase_storage")))
EOF
flutter --version | head -1

if [[ $platform == android ]]; then
  types=(${ARUMA_E2E_BUILD_TYPES:-debug release})
  for t in "${types[@]}"; do
    log "$t: flutter build apk (dev.aruma.storage url $storage_url)"
    (cd "$app" && ORG_GRADLE_PROJECT_arumaStorageUrl=$storage_url flutter build apk "--$t" \
      --target-platform "${ARUMA_E2E_TARGET_PLATFORM:-android-arm64}" \
      "--dart-define=ARUMA_STORAGE_URL=$storage_url") >"$gen/build-$t.log" 2>&1 \
      || { tail -40 "$gen/build-$t.log" >&2; exit 1; }
    cp "$app/build/app/outputs/flutter-apk/app-$t.apk" "$gen/app-$t.apk"
    unzip -p "$gen/app-$t.apk" "$notice" | grep -q "with \"$storage_url/v0\"" \
      || { echo "$notice is missing from the $t APK, or does not name $storage_url/v0" >&2; exit 1; }
    echo "notice in the $t APK: $(unzip -p "$gen/app-$t.apk" "$notice" | grep -m1 -o 'with "[^"]*"')"
  done

  [[ $("$adb" shell id -u | tr -d '\r') == 0 ]] && was_root=yes || was_root=no
  "$adb" root >/dev/null
  "$adb" wait-for-device
  for _ in $(seq 1 30); do
    [[ $("$adb" shell id -u 2>/dev/null | tr -d '\r') == 0 ]] && break
    sleep 1
  done
  # Both builds are signed with the debug key, so `install -r` keeps the app's uid.
  "$adb" install -r -t "$gen/app-${types[0]}.apk" >/dev/null
  uid=$("$adb" shell pm list packages -U "$app_id" | tr -d '\r' | sed -n "s/^package:$app_id uid:\([0-9]*\).*/\1/p")
  [[ -n "$uid" ]] || { echo "cannot find the uid of $app_id" >&2; exit 1; }
  # rejected probe|other prints how many packets of the app's uid the device guard rejected so far:
  # those to 192.0.2.1 (TEST-NET-1, RFC 5737, never routed: the guards' own probes), or all others.
  rejected() {
    "$adb" shell 'for T in iptables ip6tables; do $T -L aruma_flutter -v -n -x; done' | tr -d '\r' \
      | awk -v want="$1" '/REJECT/ && (($9 == "192.0.2.1") == (want == "probe")) {s += $1} END {print s + 0}'
  }
  log "device guard: iptables rejects every packet of uid $uid that leaves loopback"
  "$adb" shell "for T in iptables ip6tables; do \$T -N aruma_flutter 2>/dev/null; \$T -D OUTPUT -m owner --uid-owner $uid -j aruma_flutter 2>/dev/null; \$T -F aruma_flutter && \$T -A aruma_flutter -o lo -j RETURN || exit 1; done; iptables -A aruma_flutter -d 192.0.2.1 -j REJECT || exit 1; for T in iptables ip6tables; do \$T -A aruma_flutter -j REJECT && \$T -I OUTPUT 1 -m owner --uid-owner $uid -j aruma_flutter || exit 1; done" \
    || { echo "cannot install the device guard (it needs adb root)" >&2; exit 1; }
  guarded=yes
  "$adb" shell "su $uid toybox nc -w 2 192.0.2.1 80 </dev/null" >/dev/null 2>&1 || true
  [[ $(rejected probe) -gt 0 ]] || { echo "the device guard did not reject a probe from uid $uid" >&2; exit 1; }
  echo "probe from uid $uid to 192.0.2.1 rejected ($(rejected probe) packets)"
  "$adb" reverse "tcp:$port" "tcp:$port" >/dev/null

  status=0
  for t in "${types[@]}"; do
    log "$t: the app on $ANDROID_SERIAL"
    "$adb" install -r -t "$gen/app-$t.apk" >/dev/null
    "$adb" shell am force-stop "$app_id"
    "$adb" logcat -c
    probes=$(rejected probe)
    "$adb" shell am start -n "$app_id/com.aruma.aruma_flutter_storage_e2e.MainActivity" >/dev/null
    for _ in $(seq 1 600); do
      "$adb" logcat -d -s flutter:I | grep -q 'ARUMA_E2E_END' && break
      sleep 0.5
    done
    "$adb" logcat -d -v tag 'flutter:I' 'ArumaRedirect:I' 'AndroidRuntime:E' '*:S' | tr -d '\r' >"$gen/logcat-$t.txt"
    # The app writes the JSON to its own temporary directory; logcat carries the same lines.
    python3 - "$gen/logcat-$t.txt" "$gen/results-$t.json" "$t" <<'EOF'
import json, re, sys
lines = [l.rstrip("\n") for l in open(sys.argv[1], encoding="utf-8")]
ops = [l.split("ARUMA_E2E op ", 1)[1] for l in lines if "ARUMA_E2E op " in l]
end = [l.split("ARUMA_E2E_END ", 1)[1] for l in lines if "ARUMA_E2E_END " in l]
fatal = [l.split("ARUMA_E2E fatal ", 1)[1] for l in lines if "ARUMA_E2E fatal " in l]
passed = sum(1 for o in ops if o.startswith("✓"))
ok = bool(end) and json.loads(end[-1])["ok"] and not fatal
json.dump({"mode": sys.argv[3], "passed": passed, "total": len(ops), "ok": ok, "groups": {"steps": ops},
           "fatal": "; ".join(fatal) or (None if end else "no ARUMA_E2E_END in logcat")}, open(sys.argv[2], "w"))
EOF
    report "$gen/results-$t.json" || status=1
    grep -E '^I/ArumaRedirect: (installed|Firebase Storage)' "$gen/logcat-$t.txt" | sed 's/^I\/ArumaRedirect: /  aruma-redirect: /' || true
    if ! grep -q "^I/ArumaRedirect: Firebase Storage: requests and download URLs go to $storage_url/v0 (built with dev.aruma.storage)" "$gen/logcat-$t.txt"; then
      echo "aruma-redirect did not find the plugin's base in the $t build" >&2
      status=1
    fi
    if grep -q '^I/ArumaRedirect: redirected firebasestorage' "$gen/logcat-$t.txt"; then
      echo "Storage requests went through the run-time fallback" >&2
      status=1
    fi
    grep -q "AndroidRuntime" "$gen/logcat-$t.txt" && { grep AndroidRuntime "$gen/logcat-$t.txt" >&2; status=1; }
    # The native guard's self-test opens one connection to 192.0.2.1: iptables must have seen it.
    [[ $(rejected probe) -gt "$probes" ]] \
      || { echo "iptables saw no packet of the native guard's probe: it did not guard this run" >&2; status=1; }
    echo "  iptables, packets of the app rejected in the $t run: $(( $(rejected probe) - probes )) to the probe address; to anywhere else since the guard went in: $(rejected other)"
    "$adb" shell am force-stop "$app_id"
  done
  [[ $(rejected other) == 0 ]] || { echo "iptables rejected packets of the app that left loopback" >&2; status=1; }
  exit "$status"
fi

# iOS
mirror=${ARUMA_MIRROR:-$cache/mirror/firebase-ios-sdk.git}
log "the mirror ($mirror) at $version"
if ! git ls-remote --tags "$mirror" "refs/tags/$version" 2>/dev/null | grep -q .; then
  ARUMA_MIRROR=$mirror ARUMA_MIRROR_WORK=$cache/mirror "$root/sdk/ios/firebase-storage-patch/make-mirror.sh" "$version"
fi
mirror_commit=$(git ls-remote --tags "$mirror" "refs/tags/$version^{}" | cut -f1)
[[ -n "$mirror_commit" ]] || { echo "the mirror has no tag $version" >&2; exit 1; }
mirror_url=$mirror
[[ "$mirror_url" == *://* ]] || mirror_url=file://$mirror_url
echo "mirror $version is $mirror_commit"

log "swift package config set-mirror --original https://github.com/firebase/firebase-ios-sdk --mirror $mirror_url"
mkdir -p "$gen/mirror-config"
printf '// swift-tools-version:5.9\nimport PackageDescription\nlet package = Package(name: "MirrorConfig")\n' >"$gen/mirror-config/Package.swift"
swift package --package-path "$gen/mirror-config" config set-mirror \
  --original https://github.com/firebase/firebase-ios-sdk --mirror "$mirror_url"
swiftpm=$app/ios/Runner.xcworkspace/xcshareddata/swiftpm
mkdir -p "$swiftpm/configuration"
cp "$gen/mirror-config/.swiftpm/configuration/mirrors.json" "$swiftpm/configuration/mirrors.json"

log "flutter build ios --config-only, then xcodebuild with the caches under $cache"
(cd "$app" && flutter build ios --config-only --simulator --debug "--dart-define=ARUMA_STORAGE_URL=$storage_url") \
  >"$gen/config.log" 2>&1 || { tail -30 "$gen/config.log" >&2; exit 1; }
rm -f "$swiftpm/Package.resolved" "$cache/spm/workspace-state.json"
xcb=(-workspace "$app/ios/Runner.xcworkspace" -scheme Runner -configuration Debug -sdk iphonesimulator
  -destination "platform=iOS Simulator,id=$sim" -derivedDataPath "$cache/dd"
  -clonedSourcePackagesDirPath "$cache/spm" -packageCachePath "$cache/spm-cache")
xcodebuild "${xcb[@]}" build >"$gen/xcodebuild.log" 2>&1 || { grep -E "error:" "$gen/xcodebuild.log" | head -20 >&2; tail -20 "$gen/xcodebuild.log" >&2; exit 1; }
runner=$cache/dd/Build/Products/Debug-iphonesimulator/Runner.app

pin=$(python3 -c 'import json, sys
p = [p for p in json.load(open(sys.argv[1]))["pins"] if p["identity"] == "firebase-ios-sdk"][0]
print(p["location"], p["state"].get("version"), p["state"]["revision"])' "$swiftpm/Package.resolved")
echo "Package.resolved: $pin"
[[ "$pin" == "https://github.com/firebase/firebase-ios-sdk $version $mirror_commit" ]] \
  || { echo "firebase-ios-sdk was not resolved to the mirror's $version ($mirror_commit)" >&2; exit 1; }
checkout=$cache/spm/checkouts/firebase-ios-sdk
"$root/sdk/ios/firebase-storage-patch/check-firebase-storage.sh" "$cache/spm"
# The checkout carries this repository's patch exactly: it reverses cleanly.
patch -R -p1 --dry-run -s -d "$checkout" <"$root/sdk/ios/firebase-storage-patch/storage-host.patch" >/dev/null \
  || { echo "the checkout is not storage-host.patch as in this repository" >&2; exit 1; }
echo "the checkout reverses storage-host.patch cleanly"
grep -q ArumaStorageOrigin "$runner/Runner.debug.dylib" "$runner/Runner" 2>/dev/null \
  && echo "the app binary carries ArumaStorageOrigin" || echo "note: ArumaStorageOrigin not found in the app binary"

log "the app on simulator $sim"
if ! xcrun simctl list devices -j | python3 -c 'import json, sys
sys.exit(0 if any(d["udid"] == sys.argv[1] and d["state"] == "Booted" for ds in json.load(sys.stdin)["devices"].values() for d in ds) else 1)' "$sim"; then
  xcrun simctl boot "$sim"
  booted_here=$sim
fi
xcrun simctl bootstatus "$sim" >/dev/null
xcrun simctl uninstall "$sim" "$app_id" >/dev/null 2>&1 || true
xcrun simctl install "$sim" "$runner"
data=$(xcrun simctl get_app_container "$sim" "$app_id" data)
rm -f "$data/tmp/aruma-e2e.json"

: >"$gen/sockets.txt"
watch_sockets() {
  while :; do
    for pid in $(pgrep -f "$runner/Runner" || true); do
      lsof -nP -a -p "$pid" -i -F n 2>/dev/null | sed -n 's/^n/app /p' >>"$gen/sockets.txt" || true
    done
    sleep 0.2
  done
}
watch_sockets &
watcher=$!
# Right after a boot, SpringBoard can deny the launch for a while: retry.
for try in $(seq 1 10); do
  xcrun simctl launch --terminate-running-process --stdout="$gen/app-stdout.txt" --stderr="$gen/app-stderr.txt" \
    "$sim" "$app_id" >/dev/null 2>"$gen/launch.log" && break
  if [[ $try -eq 10 ]]; then
    cat "$gen/launch.log" >&2
    echo "SpringBoard's and FrontBoard's last words on it:" >&2
    xcrun simctl spawn "$sim" log show --last 3m --style compact \
      --predicate "process == \"SpringBoard\" AND eventMessage CONTAINS[c] \"$app_id\"" 2>/dev/null | tail -25 >&2
    exit 1
  fi
  sleep 3
done
for _ in $(seq 1 600); do
  [[ -s "$data/tmp/aruma-e2e.json" ]] && break
  sleep 0.5
done
sleep 1
kill "$watcher" 2>/dev/null; wait "$watcher" 2>/dev/null || true
watcher=
status=0
if [[ -s "$data/tmp/aruma-e2e.json" ]]; then
  cp "$data/tmp/aruma-e2e.json" "$gen/results.json"
  report "$gen/results.json" || status=1
else
  echo "the app wrote no results; its output:" >&2
  tail -30 "$gen/app-stdout.txt" "$gen/app-stderr.txt" >&2
  status=1
fi
grep -h -o 'FirebaseStorage (Aruma build): [^"]*\|ArumaRedirect: [^"]*' "$gen/app-stdout.txt" "$gen/app-stderr.txt" 2>/dev/null | sort -u | sed 's/^/  /' || true

sort -u "$gen/sockets.txt" >"$gen/sockets.unique.txt"
far=$(grep -- '->' "$gen/sockets.unique.txt" | sed 's/.*->//' || true)
outside=$(printf '%s\n' "$far" | grep . | grep -Ev '^(127\.0\.0\.1|\[::1\]|localhost):' || true)
echo "  app sockets seen: $(printf '%s\n' "$far" | grep -c . || true), to Aruma's port $port: $(printf '%s\n' "$far" | grep -c "^127.0.0.1:$port\$" || true)"
if [[ -n "$outside" ]]; then
  echo "sockets of the app to outside this machine:" >&2
  printf '%s\n' "$outside" >&2
  status=1
fi
printf '%s\n' "$far" | grep -q "^127.0.0.1:$port\$" \
  || { echo "the socket watcher saw no connection of the app to Aruma: it did not watch it" >&2; status=1; }
exit "$status"
