#!/usr/bin/env bash
# S0-B, Android: firebase-storage 23.0.0 (BoM 35.0.2) as an instrumented test on an emulator or
# device, one fresh process per mode (android/app/src/androidTest/…/StoragePathTest.java):
# emulator, prod-asis (aruma-redirect as it is), prod-spike and prod-spike-https (aruma-redirect
# with the storage spike, Aruma on http and on https).
#
#   test/experiments/storage-path/android.sh [mode…]
#
#   ANDROID_SERIAL      the device (default: the only one connected)
#   ANDROID_HOME        the Android SDK (default /Volumes/Work/android-sdk)
#   S0PATH_UPLOAD_URL   self (default) or google: the stub's X-Goog-Upload-URL
#   S0PATH_HEADER_CASE  exact (default) or canonical: how the stub spells response header names
#
# The guard: iptables on the device rejects every packet of the app's uid that does not stay on
# loopback (needs `adb root`; the counter is printed per mode), and NetworkGuard records in process.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
. "$here/lib.sh"
gen=$here/.generated/android${S0PATH_RUN_SUFFIX:-}
rm -rf "$gen"
mkdir -p "$gen"

: "${ANDROID_HOME:=/Volumes/Work/android-sdk}"
export ANDROID_HOME
adb=$ANDROID_HOME/platform-tools/adb
app=com.aruma.s0path
runner=$app.test/androidx.test.runner.AndroidJUnitRunner
if [[ -z "${ANDROID_SERIAL:-}" ]]; then
  devices=$("$adb" devices | awk 'NR > 1 && $2 == "device" {print $1}')
  [[ $(printf '%s\n' "$devices" | grep -c .) -eq 1 ]] || { echo "connect one device or set ANDROID_SERIAL (found: ${devices:-none})" >&2; exit 1; }
  ANDROID_SERIAL=$devices
fi
export ANDROID_SERIAL

modes=("$@")
[[ ${#modes[@]} -gt 0 ]] || modes=(emulator prod-asis prod-spike prod-spike-https)

uid= guarded=
cleanup() {
  set +e
  if [[ -n "$guarded" ]]; then
    "$adb" shell "for T in iptables ip6tables; do \$T -D OUTPUT -m owner --uid-owner $uid -j s0path; \$T -F s0path; \$T -X s0path; done" >/dev/null 2>&1
  fi
  "$adb" reverse --remove-all >/dev/null 2>&1
  "$adb" uninstall "$app.test" >/dev/null 2>&1
  "$adb" uninstall "$app" >/dev/null 2>&1
  stop_stub
}
trap cleanup EXIT

start_stub "$gen" -upload-url "${S0PATH_UPLOAD_URL:-self}" -header-case "${S0PATH_HEADER_CASE:-exact}"
port=${STUB_HTTP##*:}
mkdir -p "$here/android/app/src/main/res/raw"
cp "$STUB_CA" "$here/android/app/src/main/res/raw/s0path_ca.pem"

echo "==> building (firebase-storage + aruma-redirect with the spike)"
(cd "$here/android" && ./gradlew --no-daemon -q :app:assembleDebug :app:assembleDebugAndroidTest)
"$adb" root >/dev/null
for _ in $(seq 30); do
  [[ $("$adb" shell id -u 2>/dev/null | tr -d '\r') == 0 ]] && break
  sleep 1
done
"$adb" install -r -t "$here/android/app/build/outputs/apk/debug/app-debug.apk" >/dev/null
"$adb" install -r -t "$here/android/app/build/outputs/apk/androidTest/debug/app-debug-androidTest.apk" >/dev/null
uid=$("$adb" shell pm list packages -U "$app" | tr -d '\r' | sed -n "s/^package:$app uid:\([0-9]*\).*/\1/p")

rejected() {
  "$adb" shell 'for T in iptables ip6tables; do $T -L s0path -v -n -x; done' | tr -d '\r' \
    | awk '/REJECT/ {s += $1} END {print s + 0}'
}
"$adb" shell "for T in iptables ip6tables; do \$T -N s0path 2>/dev/null; \$T -D OUTPUT -m owner --uid-owner $uid -j s0path 2>/dev/null; \$T -F s0path && \$T -A s0path -o lo -j RETURN && \$T -A s0path -j REJECT && \$T -I OUTPUT 1 -m owner --uid-owner $uid -j s0path || exit 1; done"
guarded=yes
"$adb" shell "su $uid toybox nc -w 2 192.0.2.1 80 </dev/null" >/dev/null 2>&1 || true
[[ $(rejected) -gt 0 ]] || { echo "the device guard did not reject a probe from uid $uid" >&2; exit 1; }
"$adb" reverse "tcp:$port" "tcp:$port" >/dev/null
"$adb" reverse "tcp:$((port + 1))" "tcp:$((port + 1))" >/dev/null

: >"$gen/results.jsonl"
for mode in "${modes[@]}"; do
  echo "==> $mode"
  echo "{\"marker\":\"android-$mode\"}" >>"$STUB_LOG"
  before=$(rejected)
  "$adb" logcat -c
  "$adb" shell am instrument -w -e mode "$mode" -e stubHttp "$STUB_HTTP" -e stubHttps "$STUB_HTTPS" "$runner" \
    | tr -d '\r' >"$gen/instrument-$mode.txt"
  "$adb" logcat -d -v tag 'ArumaRedirect:I' 'StorageTask:*' 'NetworkRequest:*' 'UploadTask:*' '*:S' >"$gen/logcat-$mode.txt" 2>&1 || true
  line=$(sed -n 's/^.*INSTRUMENTATION_STATUS: s0path=//p' "$gen/instrument-$mode.txt")
  if [[ -z "$line" ]]; then
    tail -20 "$gen/instrument-$mode.txt" >&2
    line="{\"scenario\":\"android-$mode\",\"results\":[{\"op\":\"instrument\",\"ok\":false,\"detail\":\"no result\"}]}"
  fi
  n=$(($(rejected) - before))
  python3 -c 'import json,sys; d=json.loads(sys.argv[1]); d.setdefault("blocked",[]); d["blocked"].append(f"iptables rejected {sys.argv[2]} packets"); print(json.dumps(d))' "$line" "$n" >>"$gen/results.jsonl"
done
python3 "$here/summarize_results.py" "$gen/results.jsonl" "$STUB_LOG" --paths
