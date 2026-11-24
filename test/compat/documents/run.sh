#!/usr/bin/env bash
# Runs Firebase's own Firestore integration tests (firebase-js-sdk packages/firestore, test/integration
# and test/lite, in Node over gRPC), written for the Firestore emulator, unmodified, against a throwaway
# Aruma Document Database, or against the official emulator to record the baseline.
#
#   test/compat/documents/run.sh [--target aruma|emulator] [--update-baseline] [suite ...]
#   test/compat/documents/run.sh --prepare     only fetch and install, then exit (CI runs the
#                                              suites after it with the network off)
#
# Suites: integration (vitest --project=node, test/integration), persistence (the same with
# USE_MOCK_PERSISTENCE=YES, the CI's `test:node:persistence`) and lite (TEST_PLATFORM=node_lite,
# test/lite). Default: all three. The package's `test:node:emulator` script runs the same commands
# against the emulator with FIRESTORE_TARGET_BACKEND=emulator, FIRESTORE_EMULATOR_PORT and
# FIRESTORE_EMULATOR_PROJECT_ID, which is how this script points them at Aruma.
#
# Every test that passes on the emulator (baseline-emulator.json) and does not pass on Aruma must be
# listed in classified.json with its reason; summarize.mjs prints the others as UNEXPLAINED, and a
# listed test that passes again as STALE. Either fails the run. Exit codes: 0 no unexplained
# difference; 1 unexplained differences; 2 usage; 3 setup failed.
#
# The upstream code is cloned at the tag the Auth runner uses (firebase@13.0.0) into
# $ARUMA_CACHE_DIR/upstream/firebase-js-sdk, shared with test/compat/firebase-js/run.sh, and installed
# from its lockfile. The tests run with every connection off this machine refused (test/e2e/netguard.cjs);
# the auth.test.ts cases that call Google fail on the emulator too and are not compared.
#
# Environment:
#   ARUMA_CACHE_DIR           clones and caches (default: .cache in the main checkout)
#   DOCS_COMPAT_PORT          loopback port of the target (default 8280)
#   DOCS_COMPAT_DB_PREFIX     prefix of Aruma's throwaway database (default aruma_docs_compat_)
#   FIRESTORE_EMULATOR_JAR    the emulator for --target emulator (default
#                             ~/.cache/firebase/emulators/cloud-firestore-emulator-v1.22.0.jar, which
#                             `firebase setup:emulators:firestore` fetches)
#   ARUMA_TEST_DATABASE_URL   Postgres admin URL for the throwaway database
#
# It needs Go, Node 24.12 or newer, git, Postgres, Java for the emulator, and the network the first
# time. The install is heavy (a yarn install of the whole monorepo, about 1.5 GB); CI runs it in a
# job of its own (.github/workflows/ci.yml, documents-compat), with the install cached.
set -euo pipefail

JS_SDK_TAG=firebase@13.0.0
YARN=yarn@1.22.22

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../../.." && pwd)
main_root=$(dirname "$(git -C "$root" rev-parse --path-format=absolute --git-common-dir)")
cache=${ARUMA_CACHE_DIR:-$main_root/.cache}
sdk=$cache/upstream/firebase-js-sdk
pkg=$sdk/packages/firestore
results=$cache/upstream/results/documents
port=${DOCS_COMPAT_PORT:-8280}
jar=${FIRESTORE_EMULATOR_JAR:-$HOME/.cache/firebase/emulators/cloud-firestore-emulator-v1.22.0.jar}

target=aruma
update=
prepare_only=
suites=()
while [[ $# -gt 0 ]]; do
  case $1 in
    --prepare) prepare_only=1; shift ;;
    --target) target=$2; shift 2 ;;
    --update-baseline) update=--update-baseline; shift ;;
    integration | persistence | lite) suites+=("$1"); shift ;;
    *) echo "usage: $0 [--prepare] [--target aruma|emulator] [--update-baseline] [integration|persistence|lite ...]" >&2; exit 2 ;;
  esac
done
[[ ${#suites[@]} -gt 0 ]] || suites=(integration persistence lite)
[[ $target == aruma || $target == emulator ]] || { echo "unknown target $target" >&2; exit 2; }
[[ -z $update || $target == emulator ]] || { echo "--update-baseline records the emulator: use --target emulator" >&2; exit 2; }

mkdir -p "$cache/tmp" "$results"
export GOCACHE=${GOCACHE:-$cache/go-build} GOMODCACHE=${GOMODCACHE:-$cache/gomod}
export TMPDIR=${TMPDIR:-$cache/tmp} YARN_CACHE_FOLDER=${YARN_CACHE_FOLDER:-$cache/yarn} npm_config_cache=${npm_config_cache:-$cache/npm}

log() { printf '\n==> %s\n' "$*"; }
fail() {
  printf 'setup failed: %s\n' "$*" >&2
  exit 3
}

# --- upstream (network) -------------------------------------------------------------------------

# Under test/ci/egress-guard.sh (ARUMA_EGRESS_GUARD) nothing is fetched or installed: a missing
# install ends the run (the job runs --prepare before the guard).
prepare() {
  local stamp="$JS_SDK_TAG docs"
  if [[ ! -f $sdk/.aruma-docs-ready || $(cat "$sdk/.aruma-docs-ready") != "$stamp" ]]; then
    [[ -z ${ARUMA_EGRESS_GUARD:-} ]] || fail "$sdk is not prepared for this run: run.sh --prepare before the egress guard"
    if [[ ! -d $sdk/.git ]]; then
      log "cloning firebase-js-sdk at $JS_SDK_TAG"
      git clone -q --depth 1 --branch "$JS_SDK_TAG" -c advice.detachedHead=false https://github.com/firebase/firebase-js-sdk "$sdk" ||
        fail "clone"
    fi
    log "installing and building firebase-js-sdk (firestore and its dependencies)"
    (cd "$sdk" && npx -y "$YARN" install --frozen-lockfile --network-timeout 600000) || fail "yarn install"
    # @firebase/app, component, logger and util, as the Auth runner builds them, and the
    # webchannel wrapper the firestore sources import.
    (cd "$sdk/packages/auth" && npx -y "$YARN" build:deps) || fail "build:deps"
    (cd "$sdk/packages/webchannel-wrapper" && npx -y "$YARN" build) || fail "webchannel-wrapper build"
    echo "$stamp" >"$sdk/.aruma-docs-ready"
  fi
  [[ -f $sdk/config/project.json ]] ||
    echo '{"projectId": "unused", "authDomain": "unused", "apiKey": "unused"}' >"$sdk/config/project.json"
  # The vitest config checks that Playwright's Chromium is installed, though the node project never
  # starts a browser. An empty stand-in satisfies the check without a download.
  export PLAYWRIGHT_BROWSERS_PATH=$cache/pw-stub
  local exe
  exe=$(cd "$pkg" && node -e 'console.log(require(require.resolve("playwright", {paths: [process.cwd()]})).chromium.executablePath())')
  if [[ ! -e $exe ]]; then
    local base rev
    base=$(dirname "$(dirname "$(dirname "$exe")")")
    rev=$(basename "$(dirname "$(dirname "$exe")")" | sed 's/^chromium-//')
    mkdir -p "$(dirname "$exe")" "$base/chromium_headless_shell-$rev/chrome-linux"
    : >"$exe"
    : >"$base/chromium_headless_shell-$rev/chrome-linux/headless_shell"
    chmod +x "$exe" "$base/chromium_headless_shell-$rev/chrome-linux/headless_shell"
  fi
}

# --- the target (offline from here) -------------------------------------------------------------

work=$(mktemp -d "$cache/tmp/docs-compat.XXXXXX")
pids=()
cleanup() {
  for pid in ${pids[@]+"${pids[@]}"}; do
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  done
  cp "$work"/*.log "$results"/ 2>/dev/null || true
  rm -rf "$work"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

start_aruma() {
  log "building and starting Aruma on 127.0.0.1:$port"
  (cd "$root" && go build -o "$work/genconfig" ./test/smoke/genconfig) || fail "go build"
  "$work/genconfig" -out "$work/gen" -serve -documents-dev-owner -documents-open-rules \
    -addr "127.0.0.1:$port" -db-prefix "${DOCS_COMPAT_DB_PREFIX:-aruma_docs_compat_}" -log warn \
    >"$work/server.env" 2>"$work/aruma.log" &
  pids+=($!)
  for _ in $(seq 1 240); do
    grep -q ARUMA_CLI_TOKEN "$work/server.env" 2>/dev/null && break
    kill -0 "${pids[0]}" 2>/dev/null || { cat "$work/aruma.log" >&2; fail "Aruma did not start"; }
    sleep 0.25
  done
  grep -q ARUMA_CLI_TOKEN "$work/server.env" || fail "Aruma did not start"
  project=$(node -e 'console.log(JSON.parse(require("fs").readFileSync(process.argv[1])).projectId)' "$work/gen/expected.json")
  log "creating the composite indexes of the tests"
  node "$here/indexes.mjs" "$pkg/firestore_composite_index_config.tf" "http://127.0.0.1:$port" "$project" ||
    fail "indexes: one was refused that indexes.mjs does not expect"
  # Aruma reads the indexes it was just given at the next query; give the builds a moment.
  sleep 5
  version="aruma $(git -C "$root" rev-parse --short HEAD)"
}

start_emulator() {
  [[ -f $jar ]] || fail "no emulator at $jar (firebase setup:emulators:firestore fetches it)"
  log "starting the Firestore emulator on 127.0.0.1:$port"
  java -jar "$jar" --host 127.0.0.1 --port "$port" >"$work/emulator.log" 2>&1 &
  pids+=($!)
  for _ in $(seq 1 120); do
    curl -s -o /dev/null "http://127.0.0.1:$port" && break
    sleep 0.5
  done
  curl -s -o /dev/null "http://127.0.0.1:$port" || fail "the emulator did not start"
  project=test-emulator
  version="emulator $(basename "$jar")"
}

# --- suites -------------------------------------------------------------------------------------

run_suite() {
  local suite=$1 args=() env=()
  case $suite in
    integration) args=(test/integration) ;;
    persistence) args=(test/integration); env=(USE_MOCK_PERSISTENCE=YES) ;;
    lite) args=(test/lite); env=(TEST_PLATFORM=node_lite) ;;
  esac
  log "suite $suite on $version"
  (cd "$pkg" && env -u HTTP_PROXY -u HTTPS_PROXY -u http_proxy -u https_proxy -u ALL_PROXY -u all_proxy \
    NODE_OPTIONS="--require $root/test/e2e/netguard.cjs" ARUMA_NETGUARD_LOG="$results/netguard.log" \
    FIRESTORE_TARGET_BACKEND=emulator FIRESTORE_EMULATOR_PORT=$port FIRESTORE_EMULATOR_PROJECT_ID=$project \
    ${env[@]+"${env[@]}"} ../../node_modules/.bin/vitest run --project=node "${args[@]}" \
    --reporter=json --outputFile="$results/$target-$suite.json") >"$results/$target-$suite.log" 2>&1 || true
  [[ -s $results/$target-$suite.json ]] || fail "suite $suite left no report (see $results/$target-$suite.log)"
}

prepare
[[ -z $prepare_only ]] || exit 0
rm -f "$results/netguard.log"
if [[ $target == aruma ]]; then start_aruma; else start_emulator; fi
status=0
for s in "${suites[@]}"; do
  run_suite "$s"
  node "$here/summarize.mjs" "$results/$target-$s.json" "$here/baseline-emulator.json" "$here/classified.json" "$s" $update || status=1
done
echo
if [[ -f $results/netguard.log ]]; then echo "network guard: $(grep -c '^BLOCK' "$results/netguard.log" || true) connection(s) off this machine refused"; fi
exit $status
