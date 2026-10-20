#!/usr/bin/env bash
# Runs Firebase's own open-source Auth integration tests, written for the Auth emulator, against
# a throwaway Aruma server through the test-only adapter (adapter/, see README.md):
#
#   js-node       firebase-js-sdk packages/auth test/integration/flows in Node (mocha), as
#                 `yarn test:node:integration:local` runs them;
#   js-browser    the same flows in headless Chrome (karma), as
#                 `yarn test:browser:integration:local` runs them;
#   js-webdriver  test/integration/webdriver in Chrome through Selenium, as `yarn test:webdriver`
#                 runs them;
#   admin         firebase-admin-node test/integration/auth.spec.ts in its emulator mode.
#
#   test/compat/firebase-js/run.sh [js-node|js-browser|js-webdriver|admin ...]   (default: all)
#
# The upstream code is cloned at fixed tags into $ARUMA_CACHE_DIR/upstream and run from there;
# none of it enters this repository. Every download (git, npm, yarn, the chromedriver that
# matches the local Chrome) happens in the prepare step. The tests themselves run with every
# connection off this machine refused: the Node processes through test/experiments/netguard.cjs,
# Chrome through a host resolver rule that resolves nothing but loopback. Results land in
# $ARUMA_CACHE_DIR/upstream/results: <suite>.json and .log per suite, versions.txt, the Aruma,
# adapter and netguard logs.
#
# Environment:
#   ARUMA_CACHE_DIR          clones and caches (default: .cache in the main checkout)
#   ARUMA_COMPAT_PORT        first of three loopback ports: Aruma, the adapter, karma (default 8200)
#   ARUMA_COMPAT_DB_PREFIX   prefix of the throwaway database's name (default aruma_t23_)
#   ARUMA_TEST_DATABASE_URL  Postgres admin URL (default postgres://postgres@127.0.0.1:5432/postgres)
#   ARUMA_PG_BIN             directory of psql, when it is not on PATH
#   CHROME_BIN               Chrome binary (default: the macOS application)
#   ENUMERATION_PROTECTION   off (default: the emulator's default, which the tests assume) or on
#                            (Aruma's and new Firebase projects' default)
#
# It needs Go, Node 24.12 or newer, git, openssl, psql and Chrome, and network access the first
# time. It is heavy (a yarn install of the whole firebase-js-sdk monorepo) and is not part of CI.
set -euo pipefail

JS_SDK_TAG=firebase@13.0.0
ADMIN_NODE_TAG=v14.5.0
YARN=yarn@1.22.22
PROJECT=compat-tests

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../../.." && pwd)
# The main checkout's .cache, also from a git worktree.
main_root=$(dirname "$(git -C "$root" rev-parse --path-format=absolute --git-common-dir)")
cache=${ARUMA_CACHE_DIR:-$main_root/.cache}
upstream=$cache/upstream
results=$upstream/results
port=${ARUMA_COMPAT_PORT:-8200}
db_prefix=${ARUMA_COMPAT_DB_PREFIX:-aruma_t23_}
admin_url=${ARUMA_TEST_DATABASE_URL:-postgres://postgres@127.0.0.1:5432/postgres?sslmode=disable}
psql=${ARUMA_PG_BIN:+$ARUMA_PG_BIN/}psql
chrome=${CHROME_BIN:-/Applications/Google Chrome.app/Contents/MacOS/Google Chrome}
protection=${ENUMERATION_PROTECTION:-off}
suites=("$@")
[[ ${#suites[@]} -gt 0 ]] || suites=(js-node js-browser js-webdriver admin)

# Every cache under $cache, none on the system disk.
mkdir -p "$cache/tmp"
export GOCACHE=${GOCACHE:-$cache/go-build} GOMODCACHE=${GOMODCACHE:-$cache/gomod}
export TMPDIR=${TMPDIR:-$cache/tmp} npm_config_cache=${npm_config_cache:-$cache/npm}
export YARN_CACHE_FOLDER=${YARN_CACHE_FOLDER:-$cache/yarn} SE_CACHE_PATH=$cache/selenium
export PUPPETEER_SKIP_DOWNLOAD=1 CHROMEDRIVER_SKIP_DOWNLOAD=true

log() { printf '\n==> %s\n' "$*"; }

# --- upstream (network) -----------------------------------------------------------------------

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
    (cd "$dir" && npx -y "$YARN" install --frozen-lockfile --network-timeout 600000)
    (cd "$dir/packages/auth" && npx -y "$YARN" build:deps)
    touch "$dir/.aruma-ready"
  fi
  # The test settings require this file even in emulator mode (tools/config.js writes it for
  # a real project); its values are unused when FIREBASE_AUTH_EMULATOR_HOST is set.
  [[ -f $dir/config/project.json ]] ||
    echo '{"projectId": "unused", "authDomain": "unused", "apiKey": "unused"}' >"$dir/config/project.json"
}

prepare_webdriver() {
  local dir=$upstream/firebase-js-sdk/packages/auth
  log "bundling the WebDriver test page and fetching the chromedriver for $("$chrome" --version)"
  (cd "$dir" && ../../node_modules/.bin/rollup -c test/integration/webdriver/static/rollup.config.js) >/dev/null
  # Selenium Manager downloads the driver matching this Chrome into $SE_CACHE_PATH; the run is
  # offline (SE_OFFLINE) and finds it there.
  "$dir/../../node_modules/selenium-webdriver/bin/macos/selenium-manager" --browser chrome \
    --browser-path "$chrome" --cache-path "$SE_CACHE_PATH" --skip-browser-in-path >/dev/null
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

# --- Aruma (offline from here) ----------------------------------------------------------------

work=$(mktemp -d "$cache/tmp/aruma-compat.XXXXXX")
db=$db_prefix$(openssl rand -hex 4)
pids=()
launcher=
cleanup() {
  # The server and adapter logs help explain failures.
  cp "$work"/aruma.log "$work"/adapter.log "$results"/ 2>/dev/null || true
  for pid in ${pids[@]+"${pids[@]}"}; do
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  done
  "$psql" -q "$admin_url" -c "DROP DATABASE IF EXISTS $db WITH (FORCE)" >/dev/null 2>&1 || true
  rm -rf "$work" ${launcher:+"$(dirname "$launcher")"}
}
trap cleanup EXIT

# json prints one field of a JSON document read from stdin.
json() { node -e 'let s = ""; process.stdin.on("data", d => s += d).on("end", () => console.log(JSON.parse(s)[process.argv[1]]))' "$1"; }

start_aruma() {
  log "building Aruma and the adapter"
  (cd "$root" && go build -o "$work/aruma" ./cmd/aruma && go build -o "$work/adapter" ./test/compat/firebase-js/adapter)

  local base=http://127.0.0.1:$port password
  password=$(openssl rand -hex 16)
  "$psql" -q "$admin_url" -c "CREATE DATABASE $db" >/dev/null
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

  ARUMA_CLI_TOKEN=$token "$work/adapter" -aruma "$base" -project "$PROJECT" -listen "127.0.0.1:$((port + 1))" \
    >"$work/adapter.out" 2>"$work/adapter.log" &
  pids+=($!)
  for _ in $(seq 1 40); do
    grep -q ADAPTER_ADDR "$work/adapter.out" 2>/dev/null && break
    sleep 0.25
  done
  ADAPTER_ADDR=$(sed -n 's/^ADAPTER_ADDR=//p' "$work/adapter.out")
  [[ -n $ADAPTER_ADDR ]] || { cat "$work/adapter.log" >&2; exit 1; }
  log "Aruma at $base, adapter at $ADAPTER_ADDR, enumeration protection $protection"
}

# offline_chrome writes a Chrome launcher that resolves no host but loopback, so the page under
# test cannot reach Google (gapi, reCAPTCHA) or anything else, and that runs headless when the
# caller (Selenium) does not ask for it. It lives on the system disk: macOS refuses chromedriver
# to run a script from an external volume ("Operation not permitted").
offline_chrome() {
  launcher=$(mktemp -d /tmp/aruma-compat-chrome.XXXXXX)/chrome
  {
    echo '#!/usr/bin/env bash'
    printf 'chrome=%q\n' "$chrome"
    cat <<'EOF'
[[ $* == --version ]] && exec "$chrome" --version
headless=--headless=new
[[ $* == *--headless* ]] && headless=
exec "$chrome" $headless --host-resolver-rules="MAP * ~NOTFOUND, EXCLUDE localhost, EXCLUDE 127.0.0.1" \
  --no-first-run --no-default-browser-check --disable-background-networking --disable-component-update \
  --disable-sync --disable-domain-reliability "$@"
EOF
  } >"$launcher"
  chmod +x "$launcher"
}

# --- suites -----------------------------------------------------------------------------------

# Each Node suite writes mocha's JSON report and a log; the browser suite only a log (karma has
# no JSON reporter installed upstream). A failing test does not stop the run.
guard="--require $root/test/experiments/netguard.cjs"
export ARUMA_NETGUARD_LOG=$results/netguard.log

run_js_node() {
  local dir=$upstream/firebase-js-sdk/packages/auth
  log "firebase-js-sdk auth integration tests in Node"
  (cd "$dir" && NODE_OPTIONS="--no-strip-types $guard" \
    TS_NODE_COMPILER_OPTIONS='{"module":"commonjs","target":"es6"}' \
    FIREBASE_AUTH_EMULATOR_HOST=$ADAPTER_ADDR GCLOUD_PROJECT=$PROJECT \
    ../../node_modules/.bin/mocha test/integration/flows/{email,anonymous,firebaseserverapp}.test.ts \
    test/integration/flows/*.local.test.ts --config ../../config/mocharc.node.js \
    --reporter json --reporter-option "output=$results/js-node.json") >"$results/js-node.log" 2>&1 || true
}

run_js_browser() {
  local dir=$upstream/firebase-js-sdk/packages/auth
  log "firebase-js-sdk auth integration tests in headless Chrome"
  (cd "$dir" && NODE_OPTIONS="$guard" CHROME_BIN=$launcher \
    FIREBASE_AUTH_EMULATOR_HOST=$ADAPTER_ADDR GCLOUD_PROJECT=$PROJECT \
    ../../node_modules/.bin/karma start --integration --local --single-run --port $((port + 2)) \
    --reporters mocha) >"$results/js-browser.log" 2>&1 || true
}

# The WebDriver files run one mocha process each, as separate suites: all of them share one
# browser session upstream, and a hook that times out (the popup tests wait for the emulator's
# IdP widget) would otherwise abort every file after it.
run_js_webdriver() {
  local dir=$upstream/firebase-js-sdk/packages/auth f name
  log "firebase-js-sdk auth WebDriver tests in Chrome (test page on port 4100, fixed upstream)"
  : >"$results/js-webdriver.log"
  for f in "$dir"/test/integration/webdriver/*.test.ts; do
    name=$(basename "$f" .test.ts)
    (cd "$dir" && NODE_OPTIONS="--no-strip-types $guard" SE_OFFLINE=true SE_BROWSER_PATH=$launcher \
      TS_NODE_COMPILER_OPTIONS='{"module":"commonjs","target":"es6"}' \
      FIREBASE_AUTH_EMULATOR_HOST=$ADAPTER_ADDR GCLOUD_PROJECT=$PROJECT \
      ../../node_modules/.bin/mocha "test/integration/webdriver/$name.test.ts" --delay \
      --config ../../config/mocharc.node.js \
      --reporter json --reporter-option "output=$results/js-webdriver.$name.json") >>"$results/js-webdriver.log" 2>&1 || true
  done
}

run_admin() {
  local dir=$upstream/firebase-admin-node
  log "firebase-admin-node auth integration tests (emulator mode)"
  # FIREBASE_EMULATOR_HUB only switches the suite to its emulator mode; nothing connects to it.
  (cd "$dir" && NODE_OPTIONS="$guard" FIREBASE_EMULATOR_HUB=127.0.0.1:1 FIREBASE_AUTH_EMULATOR_HOST=$ADAPTER_ADDR \
    GCLOUD_PROJECT=$PROJECT node_modules/.bin/mocha test/integration/setup.ts test/integration/auth.spec.ts \
    --slow 5000 --timeout 20000 --require ts-node/register \
    --reporter json --reporter-option "output=$results/admin.json") >"$results/admin.log" 2>&1 || true
}

mkdir -p "$upstream" "$results"
for s in "${suites[@]}"; do
  case $s in
    js-node | js-browser) prepare_js_sdk ;;
    js-webdriver) prepare_js_sdk && prepare_webdriver ;;
    admin) prepare_admin_node ;;
    *) echo "unknown suite $s (js-node, js-browser, js-webdriver, admin)" >&2; exit 2 ;;
  esac
done
for s in "${suites[@]}"; do rm -f "$results/$s".*json "$results/$s.log"; done
rm -f "$results/netguard.log"
{
  echo "date: $(date -u +%FT%TZ)"
  echo "aruma: $(git -C "$root" rev-parse --short HEAD)$(git -C "$root" diff --quiet HEAD || echo '+dirty')"
  echo "firebase-js-sdk: $JS_SDK_TAG (@firebase/auth $(node -p "require('$upstream/firebase-js-sdk/packages/auth/package.json').version" 2>/dev/null))"
  echo "firebase-admin-node: $ADMIN_NODE_TAG"
  echo "node: $(node --version), go: $(go env GOVERSION), chrome: $("$chrome" --version)"
  echo "enumeration protection: $protection"
} >"$results/versions.txt"
start_aruma
offline_chrome
for s in "${suites[@]}"; do
  case $s in
    js-node) run_js_node ;;
    js-browser) run_js_browser ;;
    js-webdriver) run_js_webdriver ;;
    admin) run_admin ;;
  esac
done

log "results ($results)"
cat "$results/versions.txt"
node "$here/summarize.mjs" "$results" "${suites[@]}"
