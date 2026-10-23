#!/usr/bin/env bash
# S0-B, Flutter on Android: firebase_storage (with firebase_core 5) in a debug app on an emulator or
# device, in emulator mode and in production mode with aruma-redirect and its storage spike
# (installed by flutter/MainActivity.kt before the engine starts). The Dart side also opens the
# download URL with dart:io's HttpClient. One app launch per mode (force-stopped in between).
#
#   test/experiments/storage-path/flutter.sh [mode…]     (emulator, prod-spike)
#
#   ANDROID_SERIAL, ANDROID_HOME   as in android.sh; JAVA_HOME a JDK 17+
#   S0PATH_PORT                    first stub port (default 8820)
#   S0PATH_NDK_DIR, S0PATH_CMAKE_DIR  an installed NDK (Flutter's version) and CMake (3.22.1) for
#                                  the build, so that Gradle does not download them into ANDROID_HOME
#
# The platform folders are made by `flutter create` on every run (not in git). The guard is the same
# iptables rule as android.sh.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
. "$here/lib.sh"
gen=$here/.generated/flutter
rm -rf "$gen"
mkdir -p "$gen"

: "${ANDROID_HOME:=/Volumes/Work/android-sdk}"
export ANDROID_HOME
adb=$ANDROID_HOME/platform-tools/adb
app=com.aruma.s0path_flutter
if [[ -z "${ANDROID_SERIAL:-}" ]]; then
  devices=$("$adb" devices | awk 'NR > 1 && $2 == "device" {print $1}')
  [[ $(printf '%s\n' "$devices" | grep -c .) -eq 1 ]] || { echo "connect one device or set ANDROID_SERIAL" >&2; exit 1; }
  ANDROID_SERIAL=$devices
fi
export ANDROID_SERIAL
modes=("$@")
[[ ${#modes[@]} -gt 0 ]] || modes=(emulator prod-spike)

uid= guarded=
cleanup() {
  set +e
  if [[ -n "$guarded" ]]; then
    "$adb" shell "for T in iptables ip6tables; do \$T -D OUTPUT -m owner --uid-owner $uid -j s0path; \$T -F s0path; \$T -X s0path; done" >/dev/null 2>&1
  fi
  "$adb" reverse --remove-all >/dev/null 2>&1
  "$adb" uninstall "$app" >/dev/null 2>&1
  stop_stub
}
trap cleanup EXIT

start_stub "$gen"
port=${STUB_HTTP##*:}

echo "==> publishing aruma-redirect (with the spike) to $gen/m2"
(cd "$s0_root/sdk/android/aruma-redirect" && ./gradlew --no-daemon -q -Dmaven.repo.local="$gen/m2" :aruma-redirect:publishToMavenLocal)

echo "==> flutter create and build"
cd "$here/flutter"
flutter create --platforms=android --org com.aruma --project-name s0path_flutter . >"$gen/create.log"
cp MainActivity.kt android/app/src/main/kotlin/com/aruma/s0path_flutter/MainActivity.kt
python3 - "$gen/m2" <<'EOF'
import os, re, sys
m2 = sys.argv[1]
p = 'android/build.gradle.kts'
s = open(p).read()
if 'aruma-s0path-m2' not in s:
    s = s.replace('mavenCentral()', 'mavenCentral()\n        maven { url = uri("file://%s") } // aruma-s0path-m2' % m2, 1)
    open(p, 'w').write(s)
p = 'android/app/build.gradle.kts'
s = open(p).read()
# The Flutter Gradle plugin wants an NDK (only to strip libflutter.so) and downloads one into
# ANDROID_HOME when it is missing; S0PATH_NDK_DIR points at one already on disk instead.
ndk = os.environ.get('S0PATH_NDK_DIR', '')
if ndk:
    s = re.sub(r'\n\s*(ndkVersion = flutter\.ndkVersion|ndkPath = "[^"]*")', '', s)
    s = s.replace('android {', 'android {\n    ndkPath = "%s"' % ndk, 1)
# The same for CMake (S0PATH_CMAKE_DIR), through local.properties.
cmake = os.environ.get('S0PATH_CMAKE_DIR', '')
if cmake:
    lp = 'android/local.properties'
    old = open(lp).read() if os.path.exists(lp) else ''
    props = [l for l in old.splitlines() if not l.startswith('cmake.dir=')]
    open(lp, 'w').write('\n'.join(props + ['cmake.dir=' + cmake]) + '\n')
if 'dev.aruma:aruma-redirect' not in s:
    s += '\ndependencies {\n    implementation("dev.aruma:aruma-redirect:0.1.0")\n}\n'
open(p, 'w').write(s)
p = 'android/app/src/main/AndroidManifest.xml'
s = open(p).read()
if 'usesCleartextTraffic' not in s:
    s = s.replace('<application', '<application android:usesCleartextTraffic="true"', 1)
    open(p, 'w').write(s)
EOF
flutter pub get >"$gen/pub.log"
flutter build apk --debug >"$gen/build.log" 2>&1 || { tail -30 "$gen/build.log" >&2; exit 1; }

"$adb" root >/dev/null
for _ in $(seq 30); do
  [[ $("$adb" shell id -u 2>/dev/null | tr -d '\r') == 0 ]] && break
  sleep 1
done
"$adb" install -r -t build/app/outputs/flutter-apk/app-debug.apk >/dev/null
uid=$("$adb" shell pm list packages -U "$app" | tr -d '\r' | sed -n "s/^package:$app uid:\([0-9]*\).*/\1/p")
rejected() {
  "$adb" shell 'for T in iptables ip6tables; do $T -L s0path -v -n -x; done' | tr -d '\r' \
    | awk '/REJECT/ {s += $1} END {print s + 0}'
}
"$adb" shell "for T in iptables ip6tables; do \$T -N s0path 2>/dev/null; \$T -D OUTPUT -m owner --uid-owner $uid -j s0path 2>/dev/null; \$T -F s0path && \$T -A s0path -o lo -j RETURN && \$T -A s0path -j REJECT && \$T -I OUTPUT 1 -m owner --uid-owner $uid -j s0path || exit 1; done"
guarded=yes
"$adb" reverse "tcp:$port" "tcp:$port" >/dev/null

: >"$gen/results.jsonl"
for mode in "${modes[@]}"; do
  echo "==> $mode"
  echo "{\"marker\":\"flutter-android-$mode\"}" >>"$STUB_LOG"
  before=$(rejected)
  "$adb" shell am force-stop "$app"
  "$adb" logcat -c
  "$adb" shell am start -n "$app/.MainActivity" --es mode "$mode" --es stub "$STUB_HTTP" >/dev/null
  for _ in $(seq 300); do
    "$adb" logcat -d -s flutter:I | grep -q 'S0PATH_END' && break
    sleep 1
  done
  "$adb" logcat -d -v tag 'ArumaRedirect:I' 'flutter:I' '*:S' | tr -d '\r' >"$gen/logcat-$mode.txt" 2>&1 || true
  n=$(($(rejected) - before))
  python3 - "$gen/logcat-$mode.txt" "flutter-android-$mode" "$n" >>"$gen/results.jsonl" <<'EOF'
import json, sys
log, scenario, rejected = sys.argv[1], sys.argv[2], sys.argv[3]
ops = [json.loads(l.split('S0PATH_OP ', 1)[1]) for l in open(log) if 'S0PATH_OP ' in l]
done = any('S0PATH_END' in l for l in open(log))
if not done:
    ops.append({'op': 'launch', 'ok': False, 'detail': 'no S0PATH_END in logcat'})
print(json.dumps({'scenario': scenario, 'results': ops, 'blocked': [f'iptables rejected {rejected} packets']}))
EOF
done
"$adb" shell am force-stop "$app"
python3 -c '
import re
s = open("pubspec.lock").read()
for n in ("firebase_core", "firebase_storage"):
    m = re.search(r"\n  %s:\n(?:    .*\n)*?    version: \"([^\"]+)\"" % n, s)
    print("%s %s" % (n, m.group(1) if m else "?"))'
python3 "$here/summarize_results.py" "$gen/results.jsonl" "$STUB_LOG" --paths
