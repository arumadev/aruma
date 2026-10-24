#!/usr/bin/env bash
# Runs the Android Cloud Storage end-to-end tests: the official firebase-storage and firebase-auth
# in production mode (no useEmulator), configured only with the google-services.json a real Aruma
# server generates. Storage reaches Aruma through the Gradle plugin dev.aruma.storage
# (sdk/android/aruma-storage-gradle, at build time; ADR-022 §16.2), Auth through aruma-redirect.
# The server runs on this machine with Cloud Storage and the rules of storage.rules; `adb reverse`
# gives the device the same address on its own loopback.
#
# For the debug build and for a release build (R8 on, not debuggable, signed with the debug key):
#   1. The plugin's check: a build that excludes NetworkRequest from the instrumentation must fail.
#   2. aruma-redirect's launch check: an app built without the plugin and with
#      ArumaStorageRedirect=false must stop at launch.
#   3. The build with the plugin, given the server's URL with its host in upper case. Its notice,
#      META-INF/aruma/firebase-storage-MODIFIED.txt, must be in the APK and in the universal APK
#      bundletool derives from its App Bundle.
#   4. On the device (StorageE2ETest, calling the app's own Scenario): the twelve operations must
#      all pass, the download URL must equal the unified form literally, with the host in lower
#      case, and names that need encoding must name the same object in every form of their URL.
#
# The network guard has two halves: the tests record every connection the process opens to
# anything but loopback (NetworkGuard.java) and fail on one, and iptables on the device rejects
# every packet of the app's uid that does not stay on loopback; the run fails if that counter moved.
# No Storage request may go through aruma-redirect's run-time fallback either.
#
#   test/e2e/android-storage/run.sh [am instrument arguments, e.g. -e class com.aruma.e2e.StorageE2ETest#twelveOperations]
#
#   ANDROID_SERIAL   the device (default: the only one connected)
#   ANDROID_HOME     the Android SDK (adb, and the platform Gradle compiles against)
#   ARUMA_E2E_DEVICE_GUARD=off  skip the iptables half (it needs `adb root`, which an emulator image
#                    without Google Play has); the in-process half still runs.
#   ARUMA_E2E_BUILD_TYPES  the build types to run (default "debug release")
#   ARUMA_E2E_ADDR, ARUMA_E2E_DB_PREFIX  see ../server.sh
#
# It needs a JDK 17+, the local Postgres (ARUMA_TEST_DATABASE_URL), Go and python3.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../../.." && pwd)
gen=$here/.generated
cd "$root"
# shellcheck source=../server.sh
. "$root/test/e2e/server.sh"

log() { printf '\n==> %s\n' "$*"; }
notice=META-INF/aruma/firebase-storage-MODIFIED.txt

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

log "starting Aruma with Cloud Storage and its rules, and generating google-services.json"
start_server "$gen" -storage -log warn
port=${ARUMA_URL##*:}
# The plugin is given the host in upper case and must write it in lower case (ADR-022 §16.2).
storage_upper="http://LOCALHOST:$port"
storage_url="http://localhost:$port"
python3 -c 'import json, sys; print(json.dumps({"source": open(sys.argv[1]).read()}))' "$here/storage.rules" \
  | curl -fsS -X PUT -H "Authorization: Bearer $ARUMA_CLI_TOKEN" -H "Content-Type: application/json" --data-binary @- \
    "$ARUMA_URL/console/v1/projects/$ARUMA_PROJECT_ID/storage/rules" >/dev/null
echo "Aruma at $ARUMA_URL, project $ARUMA_PROJECT_ID, device $ANDROID_SERIAL"
cp "$gen/google-services.json" "$here/app/"

gradle() { (cd "$here" && ./gradlew --no-daemon -q "$@" -ParumaUrl="$ARUMA_URL"); }
types=(${ARUMA_E2E_BUILD_TYPES:-debug release})
outs=$here/app/build/outputs

# Steps 1 to 3, for each build type: the outputs are kept under .generated/<type>/.
for t in "${types[@]}"; do
  T=$(printf '%s' "${t:0:1}" | tr '[:lower:]' '[:upper:]')${t:1}
  g=$gen/$t
  rm -rf "$g" && mkdir -p "$g"

  log "$t, 1. the plugin's check: NetworkRequest excluded from the instrumentation"
  if gradle ":app:assemble$T" -ParumaE2eBuildType="$t" -ParumaStorageUrl="$storage_upper" -ParumaE2eExclude=true \
    >"$g/build-excluded.txt" 2>&1; then
    echo "the $t build passed with NetworkRequest excluded" >&2
    exit 1
  fi
  grep -q "verify${T}ArumaStorage" "$g/build-excluded.txt" && grep -q "was not pointed at Aruma" "$g/build-excluded.txt" \
    || { cat "$g/build-excluded.txt" >&2; echo "the $t build failed, but not on the plugin's check" >&2; exit 1; }
  echo "verify${T}ArumaStorage failed the build: $(grep -o 'was not pointed at Aruma ([^)]*)' "$g/build-excluded.txt" | head -1)"

  log "$t, 2. building without the plugin, with ArumaStorageRedirect=false"
  gradle ":app:assemble$T" -ParumaE2eBuildType="$t" -ParumaE2eNoPlugin=true -ParumaStorageRedirect=false
  cp "$outs/apk/$t/app-$t.apk" "$g/app-without-plugin.apk"
  if unzip -l "$g/app-without-plugin.apk" | grep -q "$notice"; then
    echo "the $t build without the plugin has the notice" >&2
    exit 1
  fi

  log "$t, 3. building with the plugin (arumaStorage.url = $storage_upper), its tests and its App Bundle"
  gradle ":app:assemble$T" ":app:assemble${T}AndroidTest" ":app:universalApkFromBundle$T" -ParumaE2eBuildType="$t" \
    -ParumaStorageUrl="$storage_upper"
  cp "$outs/apk/$t/app-$t.apk" "$g/app.apk"
  cp "$outs/apk/androidTest/$t/app-$t-androidTest.apk" "$g/tests.apk"
  unzip -q -o "$outs/bundletool/app-$t.apks" universal.apk -d "$g"
  for f in app.apk universal.apk; do
    unzip -p "$g/$f" "$notice" | grep -q "with \"$storage_url/v0\"" \
      || { echo "$notice is missing from $t's $f, or does not name $storage_url/v0" >&2; exit 1; }
    echo "notice in $f$([[ $f == app.apk ]] || echo ' (from the App Bundle, bundletool build-apks --mode=universal)')"
  done
done

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

# Every build is signed with the same debug key, so `install -r` keeps the app's uid throughout.
"$adb" install -r -t "$gen/${types[0]}/app-without-plugin.apk" >/dev/null
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
status=0

for t in "${types[@]}"; do
  g=$gen/$t
  log "$t, 2. (device) the launch check stops the app built without the plugin"
  "$adb" install -r -t "$g/app-without-plugin.apk" >/dev/null
  "$adb" install -r -t "$g/tests.apk" >/dev/null
  "$adb" logcat -c
  "$adb" shell am instrument -w -e arumaUrl "$ARUMA_URL" -e storageUrl "$storage_url" \
    -e class com.aruma.e2e.LaunchE2ETest "$runner" | tr -d '\r' >"$g/instrument-without-plugin.txt" || true
  "$adb" logcat -d -v tag 'AndroidRuntime:E' 'ArumaRedirect:I' '*:S' | tr -d '\r' >"$g/logcat-without-plugin.txt"
  # The message says the field was read and is Google's: under R8 too, the consumer rule kept it.
  if grep -q "Process crashed" "$g/instrument-without-plugin.txt" \
    && grep -q "ArumaRedirectException: Firebase Storage would send its requests to Google" "$g/logcat-without-plugin.txt"; then
    grep -o "ArumaRedirectException: Firebase Storage would send its requests to Google: [^(]*" "$g/logcat-without-plugin.txt" | head -1
  else
    cat "$g/instrument-without-plugin.txt" "$g/logcat-without-plugin.txt" >&2
    echo "the $t app built without the plugin did not stop at launch" >&2
    status=1
  fi

  log "$t, 4. installing the build with the plugin and running the instrumented tests"
  "$adb" install -r -t "$g/app.apk" >/dev/null
  "$adb" logcat -c
  "$adb" logcat -v tag 'ArumaRedirect:I' 'ArumaE2E:I' 'TestRunner:I' 'AndroidRuntime:E' '*:S' >"$g/logcat.txt" 2>&1 &
  logcat_pid=$!
  "$adb" shell am instrument -w -e arumaUrl "$ARUMA_URL" -e storageUrl "$storage_url" \
    -e notClass com.aruma.e2e.LaunchE2ETest "$@" "$runner" | tr -d '\r' >"$g/instrument.txt"
  sleep 1
  kill "$logcat_pid" 2>/dev/null; wait "$logcat_pid" 2>/dev/null || true
  logcat_pid=
  grep -E '^./TestRunner: (finished|failed):' "$g/logcat.txt" | sed 's/^.\/TestRunner: /  /' || true
  if ! grep -q '^OK (' "$g/instrument.txt"; then
    cat "$g/instrument.txt" >&2
    status=1
  fi
  grep -E "^(OK \(|Tests run:)" "$g/instrument.txt"

  sed -n 's/^I\/ArumaE2E: //p' "$g/logcat.txt"
  grep -E '^I/ArumaRedirect: (installed|Firebase Storage)' "$g/logcat.txt" | sed 's/^I\/ArumaRedirect: //' || true
  # aruma-redirect read PROD_BASE_URL by reflection and found the plugin's base.
  if ! grep -q "^I/ArumaRedirect: Firebase Storage: requests and download URLs go to $storage_url/v0 (built with dev.aruma.storage)" "$g/logcat.txt"; then
    echo "aruma-redirect did not find the plugin's base in the $t build" >&2
    status=1
  fi
  echo "requests redirected by aruma-redirect, by host:"
  sed -n 's/^I\/ArumaRedirect: redirected \([^/]*\).*/\1/p' "$g/logcat.txt" | sort | uniq -c
  if grep -q '^I/ArumaRedirect: redirected firebasestorage' "$g/logcat.txt"; then
    echo "Storage requests went through the run-time fallback, not the plugin's base" >&2
    status=1
  fi
done

if [[ -n "$guarded" ]]; then
  n=$(( $(rejected) - baseline ))
  echo "packets of the app rejected by the device guard during the tests: $n"
  [[ "$n" == 0 ]] || status=1
fi
[[ $status -eq 0 ]] && log "Android Cloud Storage e2e passed (${types[*]})"
exit "$status"
