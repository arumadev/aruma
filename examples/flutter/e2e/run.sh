#!/usr/bin/env bash
# The end-to-end test of the Flutter example on the web: the app as a developer builds it
# (tool/web.sh, then `flutter build web`), against a real Aruma with Auth, the Realtime Database,
# Cloud Storage and the Document Database (test/smoke/genconfig -rtdb -storage -documents), with
# the three rules files of rules/ deployed, driven in Chromium as two users at once, with every
# request off this machine refused.
#
#   examples/flutter/e2e/run.sh [--install] [--runs N] [--screenshots] [-- playwright test args]
#
#   --install      copy and install the app and the test's packages, then exit (CI runs it before
#                  turning the egress guard on; under the guard nothing is installed)
#   --runs N       run the scenario N times in a row (default 1); it stops at the first failure
#   --screenshots  also write the README's screenshots to docs/ (412x915, device scale 2)
#
#   ARUMA_E2E_APP_DIR    where the app is copied and built (default <repo>/.cache/example-flutter)
#   ARUMA_E2E_PAGE_PORT  the page's port on localhost (default 9431)
#   ARUMA_E2E_ADDR, ARUMA_E2E_DB_PREFIX  see test/e2e/server.sh
#   PW_CHROMIUM          a Chromium to use instead of Playwright's own
#   ARUMA_E2E_SPEC       the spec to run (default tests/team_board.spec.mjs)
#
# The app is copied, so this checkout is never changed: the copy gets the server's
# firebase_options.dart (from the Management API, as the dashboard's Flutter card downloads it)
# and an aruma.json with the server's URL; pubspec_overrides.yaml and package.json point it back
# at this repository's aruma_storage and @aruma/web.
#
# It needs Flutter (the version of .github/workflows/ci.yml), Go, Node, python3 and the local
# Postgres (ARUMA_TEST_DATABASE_URL).
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
example=$(cd "$here/.." && pwd)
root=$(cd "$example/../.." && pwd)
gen=$here/.generated
app=${ARUMA_E2E_APP_DIR:-$root/.cache/example-flutter}
page_port=${ARUMA_E2E_PAGE_PORT:-9431}
guarded=${ARUMA_EGRESS_GUARD:-}

install_only= runs=1 screenshots=
while [[ $# -gt 0 ]]; do
  case $1 in
    --install) install_only=1 ;;
    --runs) runs=$2; shift ;;
    --screenshots) screenshots=1 ;;
    --) shift; break ;;
    *) echo "unknown argument $1" >&2; exit 2 ;;
  esac
  shift
done
if [[ -n $install_only && -n $guarded ]]; then
  echo "run.sh --install under the egress guard: install before turning it on" >&2
  exit 2
fi

log() { printf '\n==> %s\n' "$*"; }
not_installed() {
  echo "$1 is not installed: run examples/flutter/e2e/run.sh --install before the egress guard" >&2
  exit 1
}

# The app's copy: the example's files, without what a build or an install leaves behind.
copy_app() {
  log "copying the app to $app"
  mkdir -p "$app"
  # Everything but what the install left (kept for a run under the egress guard).
  find "$app" -mindepth 1 -maxdepth 1 ! -name .dart_tool ! -name node_modules ! -name build -exec rm -rf {} +
  tar -C "$example" -cf - \
    --exclude ./build --exclude ./.dart_tool --exclude ./node_modules --exclude ./web/aruma \
    --exclude ./e2e --exclude ./docs --exclude ./pubspec_overrides.yaml \
    --exclude ./lib/firebase_options.dart --exclude ./aruma.json . | tar -C "$app" -xf -
  printf 'dependency_overrides:\n  aruma_storage:\n    path: %s\n' "$root/sdk/flutter/aruma_storage" >"$app/pubspec_overrides.yaml"
  node -e '
    const fs = require("fs");
    const [file, sdk] = process.argv.slice(1);
    const p = JSON.parse(fs.readFileSync(file, "utf8"));
    p.devDependencies["@aruma/web"] = "file:" + sdk;
    fs.writeFileSync(file, JSON.stringify(p, null, 2) + "\n");
  ' "$app/package.json" "$root/sdk/web/aruma-web"
  # The placeholder until the server's file is written: pub get needs one.
  cp "$example/lib/firebase_options.dart" "$app/lib/"
  cp "$example/aruma.json" "$app/"
}

if [[ -z $guarded ]]; then
  copy_app
  log "installing the app's packages and the test's"
  (cd "$app" && flutter pub get >/dev/null && npm install --no-audit --no-fund --silent)
  (cd "$here" && npm ci --no-audit --no-fund --silent)
else
  [[ -f $app/.dart_tool/package_config.json && -d $app/node_modules/firebase ]] || not_installed "$app"
  [[ -d $here/node_modules/@playwright/test ]] || not_installed "$here/node_modules"
  copy_app
fi
[[ -z $install_only ]] || exit 0

# shellcheck source=../../../test/e2e/server.sh
. "$root/test/e2e/server.sh"
page_pid=
cleanup() {
  [[ -z $page_pid ]] || { kill "$page_pid" 2>/dev/null || true; wait "$page_pid" 2>/dev/null || true; }
  stop_server
}
trap cleanup EXIT

log "starting Aruma (Auth, Realtime Database, Cloud Storage, Document Database)"
start_server "$gen/server" -rtdb -storage -documents -log "${ARUMA_E2E_LOG:-warn}"
echo "Aruma on $ARUMA_URL, project $ARUMA_PROJECT_ID"

log "deploying rules/ with tool/deploy_rules.sh, and guest sign-in"
ARUMA_TOKEN=$ARUMA_CLI_TOKEN "$example/tool/deploy_rules.sh" "$ARUMA_PROJECT_ID" --anonymous

log "configuring the app: firebase_options.dart from the server, aruma.json"
# The dashboard's Flutter card downloads the same file (GET .../flutterOptions).
q=$(python3 -c 'import json, sys; e = json.load(open(sys.argv[1])); print("web=%s&android=%s&ios=%s" % (e["web"]["appId"], e["android"]["appId"], e["ios"]["appId"]))' "$gen/server/expected.json")
curl -fsS -H "Authorization: Bearer $ARUMA_CLI_TOKEN" \
  "$ARUMA_URL/console/v1/projects/$ARUMA_PROJECT_ID/flutterOptions?$q" >"$app/lib/firebase_options.dart"
printf '{"useAruma": true, "arumaUrl": "%s"}\n' "$ARUMA_URL" >"$app/aruma.json"

log "building the web app (tool/web.sh, flutter build web)"
(cd "$app" && TOOL_WEB_NO_INSTALL=${guarded:+1} tool/web.sh)
(cd "$app" && flutter build web --no-web-resources-cdn --no-pub >"$gen/build-web.log" 2>&1) \
  || { tail -30 "$gen/build-web.log" >&2; exit 1; }

log "serving build/web on http://localhost:$page_port"
node "$here/serve.mjs" "$page_port" "$app/build/web" >"$gen/serve.log" 2>&1 &
page_pid=$!
for _ in $(seq 1 100); do curl -fsS -o /dev/null "http://localhost:$page_port/" 2>/dev/null && break; sleep 0.1; done

export ARUMA_URL ARUMA_E2E_PAGE="http://localhost:$page_port" ARUMA_E2E_FIREBASE="$app/node_modules/firebase"
status=0
for run in $(seq 1 "$runs"); do
  log "scenario, run $run of $runs"
  if ! (cd "$here" && npx playwright test "${ARUMA_E2E_SPEC:-tests/team_board.spec.mjs}" "$@"); then
    status=1
    echo "run $run FAILED" >&2
    break
  fi
done
if [[ $status -eq 0 && -n $screenshots ]]; then
  log "screenshots to docs/"
  (cd "$here" && ARUMA_E2E_SHOTS="$example/docs" npx playwright test tests/screenshots.spec.mjs "$@") || status=1
fi
[[ $status -ne 0 ]] || echo "all $runs run(s) passed"
exit $status
