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

# package <dir> <json>: writes dir/package.json from a JSON fragment of dependencies.
package() {
  node -e '
    const [dir, deps, sdk] = process.argv.slice(1);
    const d = JSON.parse(deps);
    d["@aruma/web"] = "file:" + sdk;
    require("fs").writeFileSync(dir + "/package.json", JSON.stringify({ name: "aruma-measure", private: true, type: "module", dependencies: d }, null, 2));
  ' "$1" "$2" "$sdk"
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
  (cd "$dir" && npm install --no-audit --no-fund --silent && rm -rf node_modules/.vite)
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
}

for t in "${targets[@]}"; do
  case $t in
    vite5 | vite6 | vite7 | vite8) vite_app "${t#vite}" ;;
    *) echo "unknown target $t" >&2; exit 2 ;;
  esac
done

log "summary"
printf '  %s\n' "${results[@]}"
