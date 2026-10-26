#!/usr/bin/env bash
# Runs the official firebase-firestore (TransportTest) against the transport spike server on an
# emulator or device, once per listener: h2c to connect and to grpc-go (ssl=false, required), then
# TLS to each with the spike's CA as a trust anchor of the app (network_security_config.xml),
# reported but not required. The server runs on this machine; `adb reverse` gives the device the
# same ports on its own loopback. CI only (.github/workflows/documents-spike.yml): never on a
# laptop that cannot spare an emulator.
#
# The network guard is test/e2e/android-rtdb's: NetworkGuard records every connection off loopback
# (the HTTP stack's and grpc-okhttp's), and iptables on the device rejects every packet of the
# app's uid that leaves loopback; the run fails if that counter moved.
#
#   ANDROID_SERIAL, ANDROID_HOME   as in test/e2e/android-rtdb/run.sh
#   ARUMA_E2E_DEVICE_GUARD=off     skip the iptables half
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
spike=$(cd "$here/../.." && pwd)
spike_root=$(cd "$spike/../../../.." && pwd)
gen=$spike/.generated
# shellcheck source=../../server.sh
. "$spike/server.sh"

log() { printf '\n==> %s\n' "$*"; }

: "${ANDROID_HOME:=$HOME/Library/Android/sdk}"
export ANDROID_HOME
adb=$ANDROID_HOME/platform-tools/adb
app=com.aruma.spike
runner=$app.test/androidx.test.runner.AndroidJUnitRunner
ports=(9100 9101 9102 9103)

if [[ -z "${ANDROID_SERIAL:-}" ]]; then
  devices=$("$adb" devices | awk 'NR > 1 && $2 == "device" {print $1}')
  [[ $(printf '%s\n' "$devices" | grep -c .) -eq 1 ]] || { echo "connect one device or set ANDROID_SERIAL (found: ${devices:-none})" >&2; exit 1; }
  ANDROID_SERIAL=$devices
fi
export ANDROID_SERIAL

uid= logcat_pid= guarded= baseline=0
cleanup() {
  set +e
  [[ -n "$logcat_pid" ]] && kill "$logcat_pid" 2>/dev/null && wait "$logcat_pid" 2>/dev/null
  if [[ -n "$guarded" ]]; then
    "$adb" shell "for T in iptables ip6tables; do \$T -D OUTPUT -m owner --uid-owner $uid -j aruma_spike; \$T -F aruma_spike; \$T -X aruma_spike; done" >/dev/null 2>&1
  fi
  for p in "${ports[@]}"; do "$adb" reverse --remove "tcp:$p" >/dev/null 2>&1; done
  "$adb" uninstall "$app.test" >/dev/null 2>&1
  "$adb" uninstall "$app" >/dev/null 2>&1
  stop_spike
}
trap cleanup EXIT

log "starting the spike server"
start_spike "$gen" -later "${SPIKE_LATER:-65s}"
mkdir -p "$here/app/src/main/res/raw"
cp "$gen/ca.pem" "$here/app/src/main/res/raw/spike_ca.pem"

log "building the app and the tests (firebase-firestore)"
(cd "$here" && ./gradlew --no-daemon -q :app:assembleDebug :app:assembleDebugAndroidTest)

if [[ "${ARUMA_E2E_DEVICE_GUARD:-on}" != off ]]; then
  "$adb" root >/dev/null
  "$adb" wait-for-device
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

rejected() {
  "$adb" shell 'for T in iptables ip6tables; do $T -L aruma_spike -v -n -x; done' | tr -d '\r' \
    | awk '/REJECT/ {s += $1} END {print s + 0}'
}

if [[ "${ARUMA_E2E_DEVICE_GUARD:-on}" != off ]]; then
  log "device guard: iptables rejects every packet of uid $uid that leaves loopback"
  "$adb" shell "for T in iptables ip6tables; do \$T -N aruma_spike 2>/dev/null; \$T -D OUTPUT -m owner --uid-owner $uid -j aruma_spike 2>/dev/null; \$T -F aruma_spike && \$T -A aruma_spike -o lo -j RETURN && \$T -A aruma_spike -j REJECT && \$T -I OUTPUT 1 -m owner --uid-owner $uid -j aruma_spike || exit 1; done" \
    || { echo "cannot install the device guard (it needs adb root; set ARUMA_E2E_DEVICE_GUARD=off to skip it)" >&2; exit 1; }
  guarded=yes
  "$adb" shell "su $uid toybox nc -w 2 192.0.2.1 80 </dev/null" >/dev/null 2>&1 || true
  baseline=$(rejected)
  [[ "$baseline" -gt 0 ]] || { echo "the device guard did not reject a probe from uid $uid" >&2; exit 1; }
  echo "probe from uid $uid to 192.0.2.1 rejected ($baseline packets)"
fi

for p in "${ports[@]}"; do "$adb" reverse "tcp:$p" "tcp:$p" >/dev/null; done
"$adb" logcat -c
"$adb" logcat -v tag 'ArumaSpike:I' 'TestRunner:I' 'AndroidRuntime:E' 'Firestore:*' 'GrpcCallProvider:*' '*:S' >"$gen/android-logcat.txt" 2>&1 &
logcat_pid=$!

status=0
# run LABEL PORT SSL REQUIRED(yes|no)
run() {
  local label=$1 port=$2 ssl=$3 required=$4
  log "android $label (127.0.0.1:$port, ssl=$ssl)"
  if "$adb" shell am instrument -w -e addr "127.0.0.1:$port" -e ssl "$ssl" -e label "$label" \
    -e hold "${SPIKE_HOLD:-60}" -e listenFor 100 "$runner" | tr -d '\r' >"$gen/android-$label.txt" \
    && grep -q '^OK (' "$gen/android-$label.txt"; then
    echo "PASS android-$label"
  elif [[ $required == yes ]]; then
    echo "FAIL android-$label (required)"
    tail -30 "$gen/android-$label.txt"
    status=1
  else
    echo "FAIL android-$label (reported, not required)"
    grep -E 'Exception|Error|FAILURES' "$gen/android-$label.txt" | head -10 || true
  fi
}
run h2c-connect 9100 false yes
run h2c-grpcgo 9101 false yes
run tls-connect 9102 true no
run tls-grpcgo 9103 true no
sleep 1

echo
echo "== results (logcat) =="
sed -n 's/^I\/ArumaSpike: RESULT //p' "$gen/android-logcat.txt"
if [[ -n "$guarded" ]]; then
  n=$(( $(rejected) - baseline ))
  echo "packets of the app rejected by the device guard during the tests: $n"
  [[ "$n" == 0 ]] || status=1
fi
stop_spike
echo
echo "== what firebase-firestore sent (the server's record; credentials by shape only) =="
python3 -I "$spike/summarize.py" "$gen/record.jsonl"
echo
echo "== server log: TLS errors =="
grep -i 'tls' "$gen/server.log" | tail -10 || true
exit "$status"
