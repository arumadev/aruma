#!/usr/bin/env bash
# Measures where the build-time Storage rewrite works beyond run.sh's bundlers, with the same app,
# server and guard (ADR-022 §16.1 asks S6-W to measure these and write the result down):
#
#   test/e2e/web-storage/measure.sh [target…]
#
#   vite5 vite6 vite7 vite8  `vite build` and the dev server (its dependency optimizer: esbuild's
#                            before Vite 8, Rolldown's from 8), each in the browser
#   next                     Next.js with webpack (next build --webpack) and with Turbopack
#   expo                     an Expo app's web export through Metro, in the browser, and its
#                            Android bundle, checked for the rewritten host
#   flutter                  a Flutter web app after `aruma-web flutter-web`, in the browser
#   fallback                 no build plugin: installStorage() and useArumaStorage() at run time
#
# Default: all. The apps are copied from apps/ and installed under ARUMA_E2E_APPS_DIR (default
# <repo>/.cache/web-storage-apps), outside the repository. Results: one line per target and app,
# then the Playwright output.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../../.." && pwd)
gen=$here/.generated
apps=${ARUMA_E2E_APPS_DIR:-$root/.cache/web-storage-apps}
sdk=$root/sdk/web/aruma-web
# shellcheck source=lib.sh
. "$here/lib.sh"
trap 'stop_dev; stop_all' EXIT
dev_port=${ARUMA_E2E_DEV_PORT:-9426}
export NEXT_TELEMETRY_DISABLED=1

targets=("$@")
[[ ${#targets[@]} -gt 0 ]] || targets=(vite5 vite6 vite7 vite8 next expo flutter fallback)

start_aruma
cd "$here"
[[ -d node_modules ]] || npm ci --no-audit --no-fund --silent
mkdir -p "$apps"

results=()
note() {
  results+=("$*")
  printf '  %s\n' "$*"
}

# playwright <apps> [roots]: the bundlers spec on those apps.
playwright() {
  if ARUMA_E2E_APPS=$1 ARUMA_E2E_ROOTS=${2:-} npx playwright test tests/bundlers.spec.mjs >"$gen/pw-$3.log" 2>&1; then
    note "ok      $3"
  else
    note "FAILED  $3 (see $gen/pw-$3.log)"
    grep -E '✗|⚠|Error|expect\(' "$gen/pw-$3.log" | head -20 | sed 's/^/          /'
  fi
}

# package <dir> <json> [fields]: writes dir/package.json from a JSON fragment of dependencies (and
# of other fields).
package() {
  node -e '
    const [dir, deps, sdk, fields] = process.argv.slice(1);
    const d = JSON.parse(deps);
    d["@aruma/web"] = "file:" + sdk;
    const p = { name: "aruma-measure", private: true, type: "module", ...JSON.parse(fields || "{}"), dependencies: d };
    require("fs").writeFileSync(dir + "/package.json", JSON.stringify(p, null, 2));
  ' "$1" "$2" "$sdk" "${3:-}"
}

# install_app <dir>: npm install, with @aruma/web copied in (not linked: Turbopack and Metro do not
# follow a link out of the project) fresh from sdk/web/aruma-web.
install_app() {
  rm -rf "$1/node_modules/@aruma"
  (cd "$1" && npm install --no-audit --no-fund --silent --install-links)
}

vite_app() {
  local major=$1 name=vite$1 dir=$apps/vite$1
  log "Vite $major: build and dev server"
  rm -rf "$dir/src" "$dir/dist"
  mkdir -p "$dir/src"
  package "$dir" "{\"firebase\": \"^13\", \"vite\": \"^$major\"}"
  cp index.html "$dir/"
  cp src/main.js src/ops.js "$dir/src/"
  cp apps/vite/vite.config.mjs "$dir/"
  install_app "$dir" && rm -rf "$dir/node_modules/.vite"
  note "vite $major: $(cd "$dir" && node -p 'require("vite/package.json").version')"
  if (cd "$dir" && ARUMA_E2E_NAME=$name npx vite build >"$gen/build-$name.log" 2>&1); then
    playwright "$name" "$name=$dir/dist" "$name-build"
  else
    note "FAILED  $name-build: vite build failed (see $gen/build-$name.log)"
  fi
  (cd "$dir" && ARUMA_E2E_NAME=$name exec npx vite --port "$dev_port") >"$gen/dev-$name.log" 2>&1 &
  dev_pid=$!
  wait_http "http://127.0.0.1:$dev_port/$name/"
  playwright "$name@http://127.0.0.1:$dev_port/$name/" "" "$name-dev"
  stop_dev
}

# stop_dev stops the dev server and what it started.
dev_pid=
stop_dev() {
  [[ -n "$dev_pid" ]] || return 0
  pkill -P "$dev_pid" 2>/dev/null || true
  kill "$dev_pid" 2>/dev/null || true
  wait "$dev_pid" 2>/dev/null || true
  dev_pid=
  # What the server started further down (next start's own server) still holds the port.
  lsof -ti "tcp:$dev_port" -sTCP:LISTEN 2>/dev/null | xargs kill 2>/dev/null || true
}

# netguarded <log> <command…>: runs a Node server under test/experiments/netguard.cjs.
netguarded() {
  local logf=$1
  shift
  ARUMA_NETGUARD_LOG=$logf NODE_OPTIONS="--require $root/test/experiments/netguard.cjs" "$@"
}

# guard_clean <log> <what>: a note when the netguard blocked anything.
guard_clean() {
  if grep -q BLOCK "$1" 2>/dev/null; then
    note "FAILED  $2: the server tried to leave the machine: $(grep BLOCK "$1" | sort -u | head -3 | tr '\n' ' ')"
  else
    note "ok      $2: the server connected to $(grep -c ALLOW "$1" 2>/dev/null || echo 0) local addresses and nothing else"
  fi
}

next_app() {
  local dir=$apps/next
  log "Next.js: webpack and Turbopack"
  rm -rf "$dir/app" "$dir"/.next*
  mkdir -p "$dir/app"
  package "$dir" '{"firebase": "^13", "next": "^16", "react": "^19", "react-dom": "^19"}'
  cp apps/next/next.config.mjs "$dir/"
  cp apps/next/app/*.js "$dir/app/"
  cp src/ops.js "$dir/app/ops.js"
  install_app "$dir"
  note "next: $(cd "$dir" && node -p 'require("next/package.json").version')"
  local b flag
  for b in webpack turbopack; do
    flag=
    [[ $b == webpack ]] && flag=--webpack
    if ! (cd "$dir" && ARUMA_E2E_DIST=.next-$b npx next build $flag >"$gen/build-next-$b.log" 2>&1); then
      note "FAILED  next-$b: next build failed (see $gen/build-next-$b.log)"
      grep -m5 -i "aruma\|error" "$gen/build-next-$b.log" | sed 's/^/          /'
      continue
    fi
    # What the build left: the host literal must be gone from every chunk.
    note "        next-$b: chunks still naming Google's host: $(grep -rl '"firebasestorage.googleapis.com"' "$dir/.next-$b" --include='*.js' 2>/dev/null | wc -l | tr -d ' ')"
    (cd "$dir" && ARUMA_E2E_DIST=.next-$b ARUMA_E2E_NAME=next-$b netguarded "$gen/netguard-next-$b.log" \
      exec npx next start -p "$dev_port" -H 127.0.0.1) >"$gen/start-next-$b.log" 2>&1 &
    dev_pid=$!
    wait_http "http://127.0.0.1:$dev_port/"
    playwright "next-$b@http://127.0.0.1:$dev_port/" "" "next-$b"
    stop_dev
    guard_clean "$gen/netguard-next-$b.log" "next-$b server"
  done
}

# literals <files…>: "<n> with Google's host literal, <m> with Aruma's" over the given files.
literals() {
  local google aruma
  google=$(grep -l -a -E "[\"']firebasestorage\.googleapis\.com[\"']" "$@" 2>/dev/null | wc -l | tr -d ' ')
  aruma=$(grep -l -a -E "[\"']${addr//./\\.}[\"']" "$@" 2>/dev/null | wc -l | tr -d ' ')
  echo "$google with Google's host literal, $aruma with Aruma's"
}

expo_app() {
  local dir=$apps/expo
  log "Expo: Metro, the web export in the browser, the Android bundle"
  rm -rf "$dir/dist" "$dir/dist-android" "$dir/.expo"
  mkdir -p "$dir"
  package "$dir" '{"expo": "^58", "firebase": "^13"}' '{"main": "index.js", "type": "commonjs"}'
  cp apps/expo/metro.config.js apps/expo/app.json apps/expo/index.js "$dir/"
  cp src/ops.js "$dir/ops.js"
  install_app "$dir"
  # React, React Native and the web packages at the versions this Expo bundles (what `expo install`
  # would pick, without asking Expo's servers).
  local peers
  peers=$(cd "$dir" && node -p 'const b = require("expo/bundledNativeModules.json"); ["react", "react-dom", "react-native", "react-native-web", "@expo/metro-runtime"].map((k) => `${k}@${b[k]}`).join(" ")')
  # shellcheck disable=SC2086
  (cd "$dir" && npm install --no-audit --no-fund --silent --install-links $peers)
  note "expo: $(cd "$dir" && node -p 'require("expo/package.json").version'), react-native $(cd "$dir" && node -p 'require("react-native/package.json").version'), metro $(cd "$dir" && node -p 'require("metro/package.json").version')"
  if (cd "$dir" && EXPO_OFFLINE=1 EXPO_NO_TELEMETRY=1 CI=1 npx expo export -p web --output-dir dist --clear >"$gen/build-expo-web.log" 2>&1); then
    note "        expo web bundle: $(literals $(find "$dir/dist" -name '*.js'))"
    playwright expo "expo=$dir/dist" expo-web
  else
    note "FAILED  expo-web: expo export failed (see $gen/build-expo-web.log)"
    grep -m5 -i "aruma\|error" "$gen/build-expo-web.log" | sed 's/^/          /'
  fi
  if (cd "$dir" && EXPO_OFFLINE=1 EXPO_NO_TELEMETRY=1 CI=1 npx expo export -p android --output-dir dist-android --no-bytecode --clear >"$gen/build-expo-android.log" 2>&1); then
    note "ok      expo-android bundle: $(literals $(find "$dir/dist-android" -name '*.js' -o -name '*.hbc'))"
  else
    note "FAILED  expo-android: expo export failed (see $gen/build-expo-android.log)"
    grep -m5 -i "aruma\|error" "$gen/build-expo-android.log" | sed 's/^/          /'
  fi
}

for t in "${targets[@]}"; do
  case $t in
    expo) expo_app ;;
    vite5 | vite6 | vite7 | vite8) vite_app "${t#vite}" ;;
    next) next_app ;;
    *) echo "unknown target $t" >&2; exit 2 ;;
  esac
done

log "summary"
printf '  %s\n' "${results[@]}"
