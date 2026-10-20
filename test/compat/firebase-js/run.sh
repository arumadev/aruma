#!/usr/bin/env bash
# Runs Firebase's own open-source Auth integration tests, written for the Auth emulator, against
# a throwaway Aruma server through the test-only adapter (adapter/, see README.md):
#
#   - firebase-js-sdk packages/auth test/integration/flows, in Node (mocha) and in headless
#     Chrome (karma), as `yarn test:node:integration:local` and
#     `yarn test:browser:integration:local` run them;
#   - firebase-admin-node test/integration/auth.spec.ts, in its emulator mode.
#
#   test/compat/firebase-js/run.sh [js-node|js-browser|admin ...]   (default: all three)
#
# The upstream code is cloned at fixed tags into $ARUMA_UPSTREAM_DIR (default .cache/upstream at
# the repository root, which is git-ignored) and run from there; none of it enters this
# repository. Results land in $ARUMA_UPSTREAM_DIR/results/<suite>.json and .log.
#
# Environment:
#   ARUMA_UPSTREAM_DIR      where upstream is cloned (default <root>/.cache/upstream)
#   ARUMA_TEST_DATABASE_URL Postgres admin URL (default postgres://postgres@127.0.0.1:5432/postgres)
#   ENUMERATION_PROTECTION  on (Aruma's default) or off (the emulator's default)
#
# It needs network access the first time, Go, Node 24, git, psql, openssl and Chrome. It is
# heavy (a yarn install of the whole firebase-js-sdk monorepo) and is not part of CI.
set -euo pipefail

JS_SDK_TAG=firebase@13.0.0
ADMIN_NODE_TAG=v14.5.0
PROJECT=compat-tests

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../../.." && pwd)
upstream=${ARUMA_UPSTREAM_DIR:-$root/.cache/upstream}
results=$upstream/results
admin_url=${ARUMA_TEST_DATABASE_URL:-postgres://postgres@127.0.0.1:5432/postgres?sslmode=disable}
protection=${ENUMERATION_PROTECTION:-on}
suites=("$@")
[[ ${#suites[@]} -gt 0 ]] || suites=(js-node js-browser admin)

log() { printf '\n==> %s\n' "$*"; }

# --- upstream ---------------------------------------------------------------------------------

# fetch clones a tag once; a stamp file marks a finished install so reruns skip it.
fetch() {
  local repo=$1 tag=$2 dir=$3
  if [[ ! -d $dir/.git ]]; then
    log "cloning $repo at $tag"
    git clone -q --depth 1 --branch "$tag" -c advice.detachedHead=false "https://github.com/firebase/$repo" "$dir"
  fi
  git -C "$dir" tag --points-at HEAD | grep -qxF "$tag" || {
    echo "$dir is not at $tag; remove it to fetch again" >&2
    exit 1
  }
}

prepare_js_sdk() {
  local dir=$upstream/firebase-js-sdk
  fetch firebase-js-sdk "$JS_SDK_TAG" "$dir"
  if [[ ! -f $dir/.aruma-ready ]]; then
    log "installing and building firebase-js-sdk (auth and its dependencies)"
    (cd "$dir" && PUPPETEER_SKIP_DOWNLOAD=1 CHROMEDRIVER_SKIP_DOWNLOAD=true \
      npx -y yarn@1.22.22 install --frozen-lockfile --network-timeout 600000)
    (cd "$dir/packages/auth" && npx -y yarn@1.22.22 build:deps)
    touch "$dir/.aruma-ready"
  fi
  # The test settings require this file even in emulator mode (tools/config.js writes it for
  # a real project); its values are unused when FIREBASE_AUTH_EMULATOR_HOST is set.
  [[ -f $dir/config/project.json ]] ||
    echo '{"projectId": "unused", "authDomain": "unused", "apiKey": "unused"}' >"$dir/config/project.json"
}

prepare_admin_node() {
  local dir=$upstream/firebase-admin-node
  fetch firebase-admin-node "$ADMIN_NODE_TAG" "$dir"
  if [[ ! -f $dir/.aruma-ready ]]; then
    log "installing and building firebase-admin-node"
    (cd "$dir" && npm ci --no-audit --no-fund && npm run build)
    touch "$dir/.aruma-ready"
  fi
}

# --- Aruma ------------------------------------------------------------------------------------

work=$(mktemp -d "${TMPDIR:-/tmp}/aruma-compat.XXXXXX")
db=aruma_compat_$(openssl rand -hex 4)
pids=()
cleanup() {
  # The server and adapter logs help explain failures.
  cp "$work"/aruma.log "$work"/adapter.log "$results"/ 2>/dev/null || true
  for pid in ${pids[@]+"${pids[@]}"}; do
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  done
  psql -q "$admin_url" -c "DROP DATABASE IF EXISTS $db WITH (FORCE)" >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT

free_port() {
  node -e 'const s = require("net").createServer().listen(0, "127.0.0.1", () => { console.log(s.address().port); s.close(); })'
}

# json prints one field of a JSON document read from stdin.
json() { node -e 'let s = ""; process.stdin.on("data", d => s += d).on("end", () => console.log(JSON.parse(s)[process.argv[1]]))' "$1"; }

start_aruma() {
  log "building Aruma and the adapter"
  (cd "$root" && go build -o "$work/aruma" ./cmd/aruma && go build -o "$work/adapter" ./test/compat/firebase-js/adapter)

  local port base password=compat-owner-password
  port=$(free_port)
  base=http://127.0.0.1:$port
  psql -q "$admin_url" -c "CREATE DATABASE $db" >/dev/null
  local db_url=${admin_url%%\?*}
  db_url="${db_url%/*}/$db?sslmode=disable"
  export ARUMA_DATABASE_URL=$db_url ARUMA_PUBLIC_URL=$base ARUMA_LISTEN=127.0.0.1:$port \
    ARUMA_MASTER_KEY=$(openssl rand -base64 32) ARUMA_METRICS_LISTEN=- \
    ARUMA_TRUSTED_PROXIES=127.0.0.1/32 ARUMA_MAIL_DEV_MAILBOX=true ARUMA_LOG_LEVEL=warn
  "$work/aruma" migrate up >/dev/null
  printf '%s\n' "$password" | "$work/aruma" accounts create --email owner@compat.test >/dev/null
  "$work/aruma" serve >"$work/aruma.log" 2>&1 &
  pids+=($!)
  for _ in $(seq 1 120); do
    curl -fsS "$base/console/v1/status" >/dev/null 2>&1 && break
    sleep 0.25
  done

  # A CLI token, as the dashboard issues one: sign in, then create it with the password.
  local login cookie csrf token
  login=$(curl -fsS -D "$work/login.headers" -H 'Content-Type: application/json' \
    -d "{\"email\":\"owner@compat.test\",\"password\":\"$password\"}" "$base/console/v1/auth:login")
  cookie=$(grep -i '^set-cookie:' "$work/login.headers" | head -1 | sed -E 's/^[^:]*: *//; s/;.*//' | tr -d '\r')
  csrf=$(json csrfToken <<<"$login")
  token=$(curl -fsS -H "Cookie: $cookie" -H "X-Aruma-CSRF: $csrf" -H 'Content-Type: application/json' \
    -d "{\"name\":\"compat\",\"currentPassword\":\"$password\"}" "$base/console/v1/me/tokens" | json token)

  api() {
    curl -fsS -X "$1" -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
      ${3:+-d "$3"} "$base/console/v1$2"
  }
  api POST /projects "{\"projectId\":\"$PROJECT\",\"displayName\":\"Compat tests\"}" >/dev/null
  api POST "/projects/$PROJECT/apps" '{"platform":"WEB","displayName":"Compat web"}' >/dev/null
  local enumeration=true
  [[ $protection == off ]] && enumeration=false
  api PATCH "/projects/$PROJECT/auth/config" \
    "{\"anonymousEnabled\":true,\"enumerationProtection\":$enumeration}" >/dev/null

  ARUMA_CLI_TOKEN=$token "$work/adapter" -aruma "$base" -project "$PROJECT" >"$work/adapter.out" 2>"$work/adapter.log" &
  pids+=($!)
  for _ in $(seq 1 40); do
    grep -q ADAPTER_ADDR "$work/adapter.out" 2>/dev/null && break
    sleep 0.25
  done
  ADAPTER_ADDR=$(sed -n 's/^ADAPTER_ADDR=//p' "$work/adapter.out")
  [[ -n $ADAPTER_ADDR ]] || { cat "$work/adapter.log" >&2; exit 1; }
  log "Aruma at $base, adapter at $ADAPTER_ADDR, enumeration protection $protection"
}

# --- suites -----------------------------------------------------------------------------------

# Each suite writes a log, and the Node ones mocha's JSON report too (karma has no JSON reporter
# installed upstream, so the browser log is parsed); a failing test does not stop the run.
run_js_node() {
  local dir=$upstream/firebase-js-sdk/packages/auth
  log "firebase-js-sdk auth integration tests in Node"
  (cd "$dir" && NODE_OPTIONS='--no-strip-types' \
    TS_NODE_COMPILER_OPTIONS='{"module":"commonjs","target":"es6"}' \
    FIREBASE_AUTH_EMULATOR_HOST=$ADAPTER_ADDR GCLOUD_PROJECT=$PROJECT \
    ../../node_modules/.bin/mocha test/integration/flows/{email,anonymous,firebaseserverapp}.test.ts \
    test/integration/flows/*.local.test.ts --config ../../config/mocharc.node.js \
    --reporter json --reporter-option "output=$results/js-node.json") >"$results/js-node.log" 2>&1 || true
}

run_js_browser() {
  local dir=$upstream/firebase-js-sdk/packages/auth
  log "firebase-js-sdk auth integration tests in headless Chrome"
  (cd "$dir" && FIREBASE_AUTH_EMULATOR_HOST=$ADAPTER_ADDR GCLOUD_PROJECT=$PROJECT \
    npx karma start --integration --local --single-run --reporters mocha) >"$results/js-browser.log" 2>&1 || true
}

run_admin() {
  local dir=$upstream/firebase-admin-node
  log "firebase-admin-node auth integration tests (emulator mode)"
  # FIREBASE_EMULATOR_HUB only switches the suite to its emulator mode; nothing connects to it.
  (cd "$dir" && FIREBASE_EMULATOR_HUB=127.0.0.1:1 FIREBASE_AUTH_EMULATOR_HOST=$ADAPTER_ADDR \
    GCLOUD_PROJECT=$PROJECT node_modules/.bin/mocha test/integration/setup.ts test/integration/auth.spec.ts \
    --slow 5000 --timeout 20000 --require ts-node/register \
    --reporter json --reporter-option "output=$results/admin.json") >"$results/admin.log" 2>&1 || true
}

mkdir -p "$upstream" "$results"
for s in "${suites[@]}"; do
  case $s in
    js-node | js-browser) prepare_js_sdk ;;
    admin) prepare_admin_node ;;
    *) echo "unknown suite $s (js-node, js-browser, admin)" >&2; exit 2 ;;
  esac
done
start_aruma
for s in "${suites[@]}"; do
  case $s in
    js-node) run_js_node ;;
    js-browser) run_js_browser ;;
    admin) run_admin ;;
  esac
done

log "results"
node "$here/summarize.mjs" "$results" "${suites[@]}"
