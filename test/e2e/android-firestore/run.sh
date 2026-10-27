#!/usr/bin/env bash
# Runs the Android Document Database end-to-end tests: the official firebase-firestore and
# firebase-auth in production mode (no emulator API), configured with the google-services.json a
# real Aruma server generates and the server's host, as instrumented tests on an emulator or device.
# The server runs on this machine with the Document Database and the cloud.firestore ruleset of
# firestore.rules, released through the firebaserules API (not dev_open_rules), so every read, write,
# query and listener is judged by rules; `adb reverse` gives
# the device the same address on its own loopback.
#
# The network guard has two halves: the tests record every connection the process opens to
# anything but loopback (NetworkGuard.java) and fail on one, and iptables on the device rejects
# every packet of the app's uid that does not stay on loopback, Firestore's gRPC channels included;
# the run fails if that counter moved.
#
#   test/e2e/android-firestore/run.sh [am instrument arguments, e.g. -e class com.aruma.e2e.FirestoreE2ETest#writeBatchLandsAtOnce]
#
#   ANDROID_SERIAL   the device (default: the only one connected)
#   ANDROID_HOME     the Android SDK (adb, and the platform Gradle compiles against)
#   ARUMA_E2E_DEVICE_GUARD=off  skip the iptables half (it needs `adb root`, which an emulator image
#                    without Google Play has); the in-process half still runs.
#   ARUMA_E2E_RULES  released (default) or open (dev_open_rules, no rules assertions), see below; ARUMA_E2E_LOG the server's log level
#   ARUMA_E2E_ADDR, ARUMA_E2E_DB_PREFIX  see ../server.sh
#
# It needs a JDK 17+, the local Postgres (ARUMA_TEST_DATABASE_URL), Go and python3. CI runs it on
# an emulator every week (.github/workflows/devices.yml).
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../../.." && pwd)
gen=$here/.generated
cd "$root"
# shellcheck source=../server.sh
. "$root/test/e2e/server.sh"

log() { printf '\n==> %s\n' "$*"; }

: "${ANDROID_HOME:=$HOME/Library/Android/sdk}"
export ANDROID_HOME
adb=$ANDROID_HOME/platform-tools/adb
app=com.aruma.smoke
runner=$app.test/androidx.test.runner.AndroidJUnitRunner

if [[ -z "${ANDROID_SERIAL:-}" ]]; then
  devices=$("$adb" devices | awk 'NR > 1 && $2 == "device" {print $1}')
  [[ $(printf '%s\n' "$devices" | grep -c .) -eq 1 ]] || { echo "connect one device or set ANDROID_SERIAL (found: ${devices:-none})" >&2; exit 1; }
  ANDROID_SERIAL=$devices
fi
export ANDROID_SERIAL

uid= port= logcat_pid= guarded= was_root= baseline=0
cleanup() {
  set +e
  [[ -n "$logcat_pid" ]] && kill "$logcat_pid" 2>/dev/null && wait "$logcat_pid" 2>/dev/null
  if [[ -n "$guarded" ]]; then
    "$adb" shell "for T in iptables ip6tables; do \$T -D OUTPUT -m owner --uid-owner $uid -j aruma_e2e; \$T -F aruma_e2e; \$T -X aruma_e2e; done" >/dev/null 2>&1
  fi
  [[ -n "$port" ]] && "$adb" reverse --remove "tcp:$port" >/dev/null 2>&1
  "$adb" uninstall "$app.test" >/dev/null 2>&1
  "$adb" uninstall "$app" >/dev/null 2>&1
  # Leave adbd as it was found.
  [[ "$was_root" == no ]] && "$adb" unroot >/dev/null 2>&1
  stop_server
}
trap cleanup EXIT

# ARUMA_E2E_RULES=released (the default): the cloud.firestore ruleset of firestore.rules through the
# firebaserules API, as `firebase deploy --only firestore:rules` does; every document RPC, list
# query and Listen stream is judged by it, and the rules assertions run. open is an opt-in:
# documents.dev_open_rules, which lets every client on this machine in and skips those assertions.
rules=${ARUMA_E2E_RULES:-released}
log "starting Aruma with the Document Database (rules: $rules), and generating google-services.json"
if [[ "$rules" == open ]]; then
  start_server "$gen" -documents-open-rules -log "${ARUMA_E2E_LOG:-warn}"
else
  start_server "$gen" -documents -log "${ARUMA_E2E_LOG:-warn}"
  ruleset=$(python3 -c 'import json, sys; print(json.dumps({"source": {"files": [{"name": "firestore.rules", "content": open(sys.argv[1]).read()}]}}))' "$here/firestore.rules" \
    | curl -fsS -X POST -H "Authorization: Bearer $ARUMA_CLI_TOKEN" -H "Content-Type: application/json" --data-binary @- \
      "$ARUMA_URL/v1/projects/$ARUMA_PROJECT_ID/rulesets" | python3 -c 'import json, sys; print(json.load(sys.stdin)["name"])')
  python3 -c 'import json, sys; print(json.dumps({"name": "projects/%s/releases/cloud.firestore" % sys.argv[1], "rulesetName": sys.argv[2]}))' "$ARUMA_PROJECT_ID" "$ruleset" \
    | curl -fsS -X POST -H "Authorization: Bearer $ARUMA_CLI_TOKEN" -H "Content-Type: application/json" --data-binary @- \
      "$ARUMA_URL/v1/projects/$ARUMA_PROJECT_ID/releases" >/dev/null
fi
port=${ARUMA_URL##*:}
echo "Aruma at $ARUMA_URL, project $ARUMA_PROJECT_ID, device $ANDROID_SERIAL"
cp "$gen/google-services.json" "$here/app/"

log "building the app and the tests (firebase-firestore + firebase-auth + aruma-redirect, ArumaURL=$ARUMA_URL)"
(cd "$here" && ./gradlew --no-daemon -q :app:assembleDebug :app:assembleDebugAndroidTest -ParumaUrl="$ARUMA_URL")

if [[ "${ARUMA_E2E_DEVICE_GUARD:-on}" != off ]]; then
  [[ $("$adb" shell id -u | tr -d '\r') == 0 ]] && was_root=yes || was_root=no
  "$adb" root >/dev/null
  "$adb" wait-for-device
  # adbd restarts as root; the device can show up before it answers.
  for _ in $(seq 1 30); do
    [[ $("$adb" shell id -u 2>/dev/null | tr -d '\r') == 0 ]] && break
    sleep 1
  done
fi

log "installing"
"$adb" install -r -t "$here/app/build/outputs/apk/debug/app-debug.apk" >/dev/null
"$adb" install -r -t "$here/app/build/outputs/apk/androidTest/debug/app-debug-androidTest.apk" >/dev/null
uid=$("$adb" shell pm list packages -U "$app" | tr -d '\r' | sed -n "s/^package:$app uid:\([0-9]*\).*/\1/p")
[[ -n "$uid" ]] || { echo "cannot find the uid of $app" >&2; exit 1; }

# rejected prints how many packets of the app's uid the device guard rejected so far.
rejected() {
  "$adb" shell 'for T in iptables ip6tables; do $T -L aruma_e2e -v -n -x; done' | tr -d '\r' \
    | awk '/REJECT/ {s += $1} END {print s + 0}'
}

if [[ "${ARUMA_E2E_DEVICE_GUARD:-on}" != off ]]; then
  log "device guard: iptables rejects every packet of uid $uid that leaves loopback"
  "$adb" shell "for T in iptables ip6tables; do \$T -N aruma_e2e 2>/dev/null; \$T -D OUTPUT -m owner --uid-owner $uid -j aruma_e2e 2>/dev/null; \$T -F aruma_e2e && \$T -A aruma_e2e -o lo -j RETURN && \$T -A aruma_e2e -j REJECT && \$T -I OUTPUT 1 -m owner --uid-owner $uid -j aruma_e2e || exit 1; done" \
    || { echo "cannot install the device guard (it needs adb root; set ARUMA_E2E_DEVICE_GUARD=off to skip it)" >&2; exit 1; }
  guarded=yes
  # Prove it rejects: a connection from the app's uid to TEST-NET-1 (RFC 5737, never routed).
  "$adb" shell "su $uid toybox nc -w 2 192.0.2.1 80 </dev/null" >/dev/null 2>&1 || true
  baseline=$(rejected)
  [[ "$baseline" -gt 0 ]] || { echo "the device guard did not reject a probe from uid $uid" >&2; exit 1; }
  echo "probe from uid $uid to 192.0.2.1 rejected ($baseline packets)"
else
  echo "device guard off (ARUMA_E2E_DEVICE_GUARD=off): only the in-process recorder runs" >&2
fi

"$adb" reverse "tcp:$port" "tcp:$port" >/dev/null
"$adb" logcat -c
"$adb" logcat -v tag 'ArumaRedirect:I' 'ArumaE2E:I' 'Firestore:W' 'TestRunner:I' 'AndroidRuntime:E' '*:S' >"$gen/logcat.txt" 2>&1 &
logcat_pid=$!

log "running the instrumented tests"
"$adb" shell am instrument -w -e arumaUrl "$ARUMA_URL" -e projectId "$ARUMA_PROJECT_ID" \
  -e cliToken "$ARUMA_CLI_TOKEN" -e rules "$rules" -e ruleset "${ruleset:-}" "$@" "$runner" | tr -d '\r' >"$gen/instrument.txt"
sleep 1
grep -E '^./TestRunner: (finished|failed):' "$gen/logcat.txt" | sed 's/^.\/TestRunner: /  /' || true
status=0
if ! grep -q '^OK (' "$gen/instrument.txt"; then
  cat "$gen/instrument.txt" >&2
  status=1
fi
grep -E "^(OK \(|Tests run:)" "$gen/instrument.txt"

sed -n 's/^I\/ArumaE2E: //p' "$gen/logcat.txt"
echo "Auth requests redirected by aruma-redirect, by host:"
sed -n 's/^I\/ArumaRedirect: redirected \([^/]*\).*/\1/p' "$gen/logcat.txt" | sort | uniq -c
if [[ -n "$guarded" ]]; then
  n=$(( $(rejected) - baseline ))
  echo "packets of the app rejected by the device guard during the tests: $n"
  [[ "$n" == 0 ]] || status=1
fi
[[ $status -eq 0 ]] && log "Android Document Database e2e passed"
exit "$status"
