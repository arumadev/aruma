#!/usr/bin/env bash
# Runs Firebase's own Cloud Storage tests, unmodified, against a throwaway Aruma, and records the
# emulator captures of test/oracle/storage again on it (see README.md):
#
#   js-node     firebase-js-sdk packages/storage test/integration in Node (vitest, project node)
#   js-browser  the same in Chromium (vitest browser mode, project browser)
#   js-compat   packages/storage-compat test/integration in Node (mocha)
#   ct          firebase-tools scripts/storage-emulator-integration/conformance/*.test.ts (mocha,
#               the Firebase JS SDK in Chromium through puppeteer, the Admin SDK and raw HTTP)
#   captures    test/oracle/storage/capture/capture.mjs, every scenario, then a comparison of
#               each step with the emulator's recording in test/oracle/storage/emulator
#
#   test/compat/storage/run.sh [js-node js-browser js-compat ct captures ...]   (default: all)
#   test/compat/storage/run.sh --prepare [suite ...]   only fetch and install, then exit (CI runs
#                                                     the suites after it with the network off)
#
# The js suites run as production code does: Firebase's own host names, which hosts.cjs (Node)
# and the browser's resolver rules send to a local TLS front (front.mjs), which hands each
# request to Aruma in the form of a runtime redirect. The ct suite and the captures run in their
# emulator mode, pointed at an adapter (adapter.mjs) that swaps the emulators' fixed stand-ins
# (fake-api-key, Bearer owner, an Admin SDK without credentials, /internal/reset) for real ones.
# Nothing reaches the network once the sources are installed: every Node process refuses any
# connection off this machine (test/e2e/netguard.cjs), and the browsers resolve no name
# but those of the front and loopback.
#
# Sources are fetched at fixed versions into $ARUMA_CACHE_DIR/upstream/storage (the main
# checkout's .cache): firebase-js-sdk at the firebase@13.0.0 commit (the version the Auth and
# RTDB suites run), firebase-tools at v15.34.0 (the emulator of the captures and the CLI of
# test/acceptance), and the npm releases js/, ct/ and test/oracle/storage/capture lock. None of
# it enters the repository. Results land in $ARUMA_CACHE_DIR/upstream/storage/results; summarize.mjs
# prints them against classified.json. Exit codes: 0 every failure classified, 1 unclassified
# failures, 2 usage, 3 setup failed.
#
# Environment:
#   ARUMA_CACHE_DIR          downloads, installs and results (default: .cache in the main checkout)
#   STORAGE_COMPAT_PORT      first of five loopback ports: Aruma, the adapter, the front, the
#                            vitest browser server, the capture proxy (default 9410)
#   ARUMA_COMPAT_DB_PREFIX   prefix of the throwaway database (default aruma_s3d_)
#   ARUMA_TEST_DATABASE_URL  Postgres admin URL (default postgres://postgres@127.0.0.1:5432/postgres)
#   ARUMA_PG_BIN             directory of psql, when it is not on PATH
#   PLAYWRIGHT_BROWSERS_PATH the browsers (default $ARUMA_CACHE_DIR/ms-playwright, Chromium 1248)
#   STORAGE_COMPAT_LIMITS    "default" keeps Aruma's per-address upload limits, which the suites'
#                            users' starts can meet (/v0 503 with Retry-After, GCS 429);
#                            otherwise they are raised for the run
#
# It needs Go, Node 24.12 or newer, curl, tar, openssl, psql and Postgres, and the network the
# first time. It is heavy (two browsers in a row, about 650 MB of installs); CI runs it in a job of
# its own (.github/workflows/ci.yml, storage-compat), with the sources cached.
set -euo pipefail

SDK_COMMIT=0c331cc7fd9c4671a33baca9bd20c02a08899820 # firebase@13.0.0
TOOLS_TAG=v15.34.0
TOOLS_COMMIT=48f511a2381b0a0892dd5e93e390dbb6bdda2a20
CDN_VERSIONS=(9.16.0 9.9.1) # the gstatic builds firebase-js-sdk.test.ts and persistence.test.ts add
CHROMIUM=1248               # the Chromium revision of Playwright 1.64.1
JS_PROJECT=compat-tests
CT_PROJECT=fake-project-id    # env.ts's default project; its bucket is <project>.appspot.com
CAPTURE_PROJECT=demo-aruma-ref # test/oracle/storage/capture/emulator/config.json
API_HOSTS=firebasestorage.googleapis.com,identitytoolkit.googleapis.com,securetoken.googleapis.com

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../../.." && pwd)
main_root=$(dirname "$(git -C "$root" rev-parse --path-format=absolute --git-common-dir)")
cache=${ARUMA_CACHE_DIR:-$main_root/.cache}
base=$cache/upstream/storage
sdk=$base/firebase-js-sdk
tools=$base/firebase-tools
capture=$base/capture
results=$base/results
port=${STORAGE_COMPAT_PORT:-9410}
adapter_port=$((port + 1)) front_port=$((port + 2)) browser_port=$((port + 3)) proxy_port=$((port + 4))
db_prefix=${ARUMA_COMPAT_DB_PREFIX:-aruma_s3d_}
admin_url=${ARUMA_TEST_DATABASE_URL:-postgres://postgres@127.0.0.1:5432/postgres?sslmode=disable}
psql=${ARUMA_PG_BIN:+$ARUMA_PG_BIN/}psql
all=(js-node js-browser js-compat ct captures)
prepare_only=
[[ ${1:-} == --prepare ]] && prepare_only=1 && shift
suites=("$@")
[[ ${#suites[@]} -gt 0 ]] || suites=("${all[@]}")
for s in "${suites[@]}"; do
  [[ " ${all[*]} " == *" $s "* ]] || { echo "unknown suite $s (${all[*]})" >&2; exit 2; }
done

mkdir -p "$cache/tmp" "$base" "$results"
export GOCACHE=${GOCACHE:-$cache/go-build} GOMODCACHE=${GOMODCACHE:-$cache/gomod}
export TMPDIR=${TMPDIR:-$cache/tmp} npm_config_cache=${npm_config_cache:-$cache/npm}
export PLAYWRIGHT_BROWSERS_PATH=${PLAYWRIGHT_BROWSERS_PATH:-$cache/ms-playwright}
export PUPPETEER_SKIP_DOWNLOAD=1 PUPPETEER_SKIP_CHROMIUM_DOWNLOAD=1

log() { printf '\n==> %s\n' "$*"; }
fail() {
  printf 'setup failed: %s\n' "$*" >&2
  exit 3
}
has() { [[ " ${suites[*]} " == *" $1"* ]]; }

# --- sources and tools (network) ---------------------------------------------------------------

# stamp_ok tells whether dir's stamp names exactly what is wanted; a change installs again.
# Under test/ci/egress-guard.sh (ARUMA_EGRESS_GUARD) nothing is fetched or installed: a stamp that
# does not match ends the run (the job runs --prepare before the guard).
stamp_ok() {
  [[ -f $1/.aruma-ready && $(cat "$1/.aruma-ready") == "$2" ]] && return 0
  [[ -z ${ARUMA_EGRESS_GUARD:-} ]] || fail "$1 is not prepared for this run: run.sh --prepare before the egress guard"
  return 1
}
lockhash() { openssl dgst -sha256 -r "$1" | cut -d' ' -f1; }

# prepare_sdk keeps from the firebase@13.0.0 archive what the Storage suites read: the two
# packages, the sources of @firebase/component (js/vitest.config.mjs says why), config/, the
# Playwright check the vitest config imports, and the licence. The other @firebase packages
# they import come from npm at the versions that release ships (js/).
prepare_sdk() {
  local want top=firebase-js-sdk-$SDK_COMMIT
  want="$SDK_COMMIT component $(lockhash "$here/js/package-lock.json")"
  if ! stamp_ok "$sdk" "$want"; then
    log "fetching firebase-js-sdk at $SDK_COMMIT (firebase@13.0.0) and installing js/"
    rm -rf "$sdk" "$base/sdk.tar.gz"
    curl -fsSL --retry 3 -o "$base/sdk.tar.gz" "https://codeload.github.com/firebase/firebase-js-sdk/tar.gz/$SDK_COMMIT" ||
      fail "download of firebase-js-sdk"
    mkdir -p "$sdk"
    tar -xzf "$base/sdk.tar.gz" -C "$sdk" --strip-components 1 "$top/packages/storage" "$top/packages/storage-compat" \
      "$top/packages/component/src" "$top/config" "$top/scripts/ensure_playwright.js" "$top/LICENSE" ||
      fail "extracting firebase-js-sdk"
    rm "$base/sdk.tar.gz"
    cp "$here/js/package.json" "$here/js/package-lock.json" "$sdk/"
    (cd "$sdk" && npm ci --ignore-scripts --no-audit --no-fund --loglevel=error) || fail "npm ci (js)"
    echo "$want" >"$sdk/.aruma-ready"
  fi
  cp "$here/js/component-index.ts" "$sdk/packages/component/aruma-index.ts"
  cp "$here/js/vitest.config.mjs" "$sdk/aruma.vitest.config.mjs"
}

# prepare_tools keeps from the firebase-tools v15.34.0 archive the conformance tests, the files
# they read beside them, the fixture constant they import, and the licence; installs ct/; and
# unpacks the gstatic builds the browser page adds from the npm releases of those versions.
prepare_tools() {
  local want
  want="$TOOLS_COMMIT tsconfig $(lockhash "$here/ct/package-lock.json") ${CDN_VERSIONS[*]}"
  stamp_ok "$tools" "$want" || install_tools
  # The preload resolves chai and puppeteer from the install, so it runs from there.
  cp "$here/ct/preload.cjs" "$tools/aruma-preload.cjs"
}

install_tools() {
  local top=firebase-tools-$TOOLS_COMMIT v
  log "fetching firebase-tools $TOOLS_TAG ($TOOLS_COMMIT) and installing ct/"
  rm -rf "$tools" "$base/tools.tar.gz"
  curl -fsSL --retry 3 -o "$base/tools.tar.gz" "https://codeload.github.com/firebase/firebase-tools/tar.gz/$TOOLS_COMMIT" ||
    fail "download of firebase-tools"
  mkdir -p "$tools"
  tar -xzf "$base/tools.tar.gz" -C "$tools" --strip-components 1 "$top/scripts/storage-emulator-integration" \
    "$top/src/emulator/testing/fixtures.ts" "$top/tsconfig.json" "$top/LICENSE" || fail "extracting firebase-tools"
  rm "$base/tools.tar.gz"
  node -e '
    const s = require("fs").readFileSync(process.argv[1], "utf8");
    const m = /export const IMAGE_FILE_BASE64\s*=\s*"([^"]+)"/.exec(s);
    if (!m) process.exit(1);
    console.log(JSON.stringify({ IMAGE_FILE_BASE64: m[1] }));
  ' "$tools/src/emulator/testing/fixtures.ts" >"$tools/fixtures.json" || fail "IMAGE_FILE_BASE64 not found in fixtures.ts"
  cp "$here/ct/package.json" "$here/ct/package-lock.json" "$tools/"
  (cd "$tools" && npm ci --ignore-scripts --no-audit --no-fund --loglevel=error) || fail "npm ci (ct)"
  for v in "${CDN_VERSIONS[@]}"; do
    mkdir -p "$tools/cdn/$v"
    (cd "$tools/cdn/$v" && npm pack --silent "firebase@$v" >/dev/null &&
      tar -xzf "firebase-$v.tgz" --strip-components 1 package/firebase-app-compat.js \
        package/firebase-auth-compat.js package/firebase-storage-compat.js && rm "firebase-$v.tgz") ||
      fail "the gstatic builds of firebase@$v"
  done
  echo "$want" >"$tools/.aruma-ready"
}

# prepare_capture copies the capture script of test/oracle/storage beside its own install, and
# builds its Go Admin client, so the checkout gets no node_modules.
prepare_capture() {
  local src=$root/test/oracle/storage/capture want
  want="$(lockhash "$src/package-lock.json") $(lockhash "$src/go-admin/go.sum")"
  if ! stamp_ok "$capture" "$want"; then
    log "installing test/oracle/storage/capture"
    rm -rf "$capture"
    mkdir -p "$capture"
    cp "$src/package.json" "$src/package-lock.json" "$capture/"
    (cd "$capture" && npm ci --ignore-scripts --no-audit --no-fund --loglevel=error) || fail "npm ci (capture)"
    echo "$want" >"$capture/.aruma-ready"
  fi
  cp "$src/capture.mjs" "$capture/"
  (cd "$src/go-admin" && go build -o "$capture/go-admin" .) || fail "go build go-admin"
}

for s in "${suites[@]}"; do
  case $s in
    js-*) prepare_sdk ;;
    ct) prepare_tools ;;
    captures) prepare_capture ;;
  esac
done
headless=
for f in "$PLAYWRIGHT_BROWSERS_PATH/chromium_headless_shell-$CHROMIUM"/*/{chrome-headless-shell,headless_shell}; do
  [[ -x $f ]] && headless=$f && break
done
if has js-browser || has ct; then [[ -n $headless ]] || fail "no Chromium headless shell $CHROMIUM under $PLAYWRIGHT_BROWSERS_PATH"; fi
if [[ -n $prepare_only ]]; then
  log "prepared ${suites[*]} in $base"
  exit 0
fi

# --- Aruma (offline from here) -----------------------------------------------------------------

work=$(mktemp -d "$cache/tmp/storage-compat.XXXXXX")
db=
pids=()
adapter_pid=
cleanup() {
  for pid in $adapter_pid ${pids[@]+"${pids[@]}"} $serve_pid; do
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  done
  cp "$work"/*.log "$results"/ 2>/dev/null || true
  [[ -n $db ]] && "$psql" -q "$admin_url" -c "DROP DATABASE IF EXISTS $db WITH (FORCE)" >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

wait_for() {
  for _ in $(seq 1 120); do
    curl -s -o /dev/null "$1" && return 0
    sleep 0.25
  done
  return 1
}
json() { node -e 'let s = ""; process.stdin.on("data", d => s += d).on("end", () => console.log(JSON.parse(s)[process.argv[1]]))' "$1"; }

# start_aruma builds this checkout and serves it on a new database, with Storage on and buckets
# named <project>.appspot.com (ARUMA_PROJECT_DOMAIN), the names the tests and captures use, and
# an owner with a CLI token.
start_aruma() {
  log "building Aruma"
  (cd "$root" && go build -o "$work/aruma" ./cmd/aruma) || fail "go build"
  local url=http://127.0.0.1:$port password login cookie csrf
  password=$(openssl rand -hex 16)
  db=$db_prefix$(openssl rand -hex 4)
  "$psql" -q "$admin_url" -c "CREATE DATABASE $db" >/dev/null || fail "creating database $db"
  local db_url=${admin_url%%\?*}
  db_url="${db_url%/*}/$db?sslmode=disable"
  mkdir -p "$work/storage"
  export ARUMA_DATABASE_URL=$db_url ARUMA_PUBLIC_URL=http://127.0.0.1:$adapter_port ARUMA_LISTEN=127.0.0.1:$port \
    ARUMA_MASTER_KEY=$(openssl rand -base64 32) ARUMA_METRICS_LISTEN=- ARUMA_PROJECT_DOMAIN=appspot.com \
    ARUMA_TRUSTED_PROXIES=127.0.0.1/32 ARUMA_MAIL_DEV_MAILBOX=true ARUMA_LOG_LEVEL=warn \
    ARUMA_STORAGE_ENABLED=true ARUMA_STORAGE_DATA_DIR=$work/storage
  # Every test comes from 127.0.0.1, a few hundred uploads in a minute or two. The per-address
  # limits (60 starts a minute for the clients, 100 live sessions) are ADR-023 §3-3 doing its
  # job, not what the suites test, so they are raised; STORAGE_COMPAT_LIMITS=default keeps them
  # (/v0 503 with Retry-After, GCS 429 past them).
  if [[ ${STORAGE_COMPAT_LIMITS:-} != default ]]; then
    export ARUMA_STORAGE_UPLOAD_STARTS_PER_MINUTE_PER_IP=100000 ARUMA_STORAGE_UPLOAD_SESSIONS_PER_IP=100000 \
      ARUMA_STORAGE_UPLOAD_SESSIONS_PER_USER=100000 ARUMA_STORAGE_UPLOAD_SESSIONS_PER_PROJECT=100000
  fi
  "$work/aruma" migrate up >/dev/null || fail "aruma migrate up"
  printf '%s\n' "$password" | "$work/aruma" accounts create --email owner@compat.test >/dev/null
  serve "$ARUMA_PUBLIC_URL"
  login=$(curl -fsS -D "$work/login.headers" -H 'Content-Type: application/json' \
    -d "{\"email\":\"owner@compat.test\",\"password\":\"$password\"}" "$url/console/v1/auth:login") || fail "console login"
  cookie=$(grep -i '^set-cookie:' "$work/login.headers" | head -1 | sed -E 's/^[^:]*: *//; s/;.*//' | tr -d '\r')
  csrf=$(json csrfToken <<<"$login")
  cli_token=$(curl -fsS -H "Cookie: $cookie" -H "X-Aruma-CSRF: $csrf" -H 'Content-Type: application/json' \
    -d "{\"name\":\"storage-compat\",\"currentPassword\":\"$password\"}" "$url/console/v1/me/tokens" | json token) ||
    fail "creating a CLI token"
  aruma_version="aruma $(git -C "$root" rev-parse --short HEAD)$(git -C "$root" diff --quiet HEAD -- . || echo '+dirty')"
}

# serve (re)starts the server with a public URL, the address its upload session URLs, links and
# key files name. The suites give it the adapter's, so a session leads back through it; the
# captures the recording proxy's, so the recording sees every chunk, as it saw the emulator's,
# which builds its session URLs from Host.
serve_pid=
serve() {
  if [[ -n $serve_pid ]]; then
    kill "$serve_pid" 2>/dev/null || true
    wait "$serve_pid" 2>/dev/null || true
  fi
  log "starting Aruma on http://127.0.0.1:$port, public URL $1 (database $db)"
  ARUMA_PUBLIC_URL=$1 "$work/aruma" serve >>"$work/aruma.log" 2>&1 &
  serve_pid=$!
  wait_for "http://127.0.0.1:$port/console/v1/status" || fail "Aruma did not start (see aruma.log)"
}

# project sets up one project (setup.mjs) and leaves its details in $work/<project>.json.
project() {
  local p=$1 rules=$2 cors=${3:-}
  ARUMA_CLI_TOKEN=$cli_token node "$here/setup.mjs" --base "http://127.0.0.1:$port" --project "$p" --rules "$rules" \
    ${cors:+--cors "$cors"} --out "$work" >"$work/setup-$p.out" 2>&1 || { cat "$work/setup-$p.out" >&2; fail "setting up project $p"; }
}
field() { node -p "require('$work/$1.json').$2"; }

# adapter (re)starts the adapter for one project on $adapter_port.
adapter() {
  local p=$1
  if [[ -n $adapter_pid ]]; then
    kill "$adapter_pid" 2>/dev/null || true
    wait "$adapter_pid" 2>/dev/null || true
  fi
  # The last adapter's ADAPTER_READY must not pass for this one's: the redirection below empties
  # the file only once the background process is under way.
  rm -f "$work/adapter.out"
  node "$here/adapter.mjs" --aruma "http://127.0.0.1:$port" --listen "$adapter_port" --bucket "$(field "$p" bucket)" \
    --api-key "$(field "$p" apiKey)" --service-account "$work/$p-key.json" >"$work/adapter.out" 2>>"$work/adapter.log" &
  adapter_pid=$!
  for _ in $(seq 1 40); do
    grep -q ADAPTER_READY "$work/adapter.out" 2>/dev/null && return 0
    sleep 0.25
  done
  fail "the adapter did not start (see adapter.log)"
}

# start_front writes a CA and a certificate for the front's names that it signs, valid for a day,
# and starts the front. ARUMA_FRONT_SPKI is the certificate key's SPKI hash, the one thing the
# browsers trust.
start_front() {
  local names=$API_HOSTS,example.com,www.gstatic.com san
  san=$(tr ',' '\n' <<<"$names" | sed 's/^/DNS:/' | paste -sd, -)
  (
    cd "$work"
    openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -keyout ca.key -out ca.pem -days 1 \
      -subj '/CN=Aruma Storage compat test CA' -addext 'basicConstraints=critical,CA:TRUE' -addext 'keyUsage=critical,keyCertSign'
    openssl req -new -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -keyout front.key -out front.csr -subj '/CN=Aruma compat front'
    printf 'subjectAltName=%s\nbasicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature\nextendedKeyUsage=serverAuth\n' "$san" >front.ext
    openssl x509 -req -in front.csr -CA ca.pem -CAkey ca.key -CAcreateserial -days 1 -extfile front.ext -out front.pem
  ) >"$work/openssl.out" 2>&1 || fail "openssl (see $work/openssl.out)"
  ARUMA_FRONT_SPKI=$(openssl x509 -in "$work/front.pem" -pubkey -noout | openssl pkey -pubin -outform der |
    openssl dgst -sha256 -binary | openssl base64)
  export ARUMA_FRONT_SPKI ARUMA_FRONT_PORT=$front_port
  node "$here/front.mjs" --cert "$work/front.pem" --key "$work/front.key" --port "$front_port" \
    --upstream "http://127.0.0.1:$adapter_port" --api "$API_HOSTS" --cdn "$tools/cdn" >"$work/front.out" 2>"$work/front.log" &
  pids+=($!)
  for _ in $(seq 1 40); do
    grep -q READY "$work/front.out" 2>/dev/null && return 0
    sleep 0.25
  done
  fail "the front did not start (see front.log)"
}

# --- suites ------------------------------------------------------------------------------------

guard="--require $root/test/e2e/netguard.cjs"
export ARUMA_NETGUARD_LOG=$results/netguard.log

# js_env points config/project.json at the project and Node at the front.
js_env() {
  printf '{"projectId": "%s", "storageBucket": "%s", "apiKey": "%s", "authDomain": "%s.appspot.com"}\n' \
    "$JS_PROJECT" "$(field $JS_PROJECT bucket)" "$(field $JS_PROJECT apiKey)" "$JS_PROJECT" >"$sdk/config/project.json"
}

# run_vitest runs packages/storage test/integration in one vitest project, as test:node and
# test:browser:integration do, with the JSON reporter.
run_vitest() {
  local name=$1 project=$2
  log "suite $name: packages/storage test/integration (vitest --project $project)"
  (cd "$sdk/packages/storage" && NODE_OPTIONS="--no-strip-types $guard --require $here/hosts.cjs" \
    NODE_EXTRA_CA_CERTS=$work/ca.pem ARUMA_TEST_HOSTS=$API_HOSTS ARUMA_TEST_TLS_PORT=$front_port \
    ARUMA_STORAGE_SDK=$sdk ARUMA_BROWSER_PORT=$browser_port VITEST_MAX_FORKS=1 \
    "$sdk/node_modules/.bin/vitest" run --config "$sdk/aruma.vitest.config.mjs" --project "$project" test/integration \
    --reporter=default --reporter=json --outputFile.json="$results/$name.json") >"$results/$name.log" 2>&1 || true
  [[ -s $results/$name.json ]] || fail "suite $name left no report (see $results/$name.log)"
}

# run_compat runs packages/storage-compat test/integration as its test:node script does (mocha,
# ts-node, the repo's mocha config). The paths give the type checker the per-file declarations
# inside the npm packages, which carry the internal API the compat sources use.
run_compat() {
  local nm=node_modules/@firebase
  log "suite js-compat: packages/storage-compat test/integration (mocha)"
  (cd "$sdk/packages/storage-compat" && NODE_OPTIONS="--no-strip-types $guard --require $here/hosts.cjs" \
    NODE_EXTRA_CA_CERTS=$work/ca.pem ARUMA_TEST_HOSTS=$API_HOSTS ARUMA_TEST_TLS_PORT=$front_port \
    TS_NODE_FILES=true TS_NODE_CACHE=NO \
    TS_NODE_COMPILER_OPTIONS='{"module":"commonjs","baseUrl":"'"$sdk"'","paths":{
      "@firebase/app":["'$nm'/app/dist/app/src/index.d.ts"],
      "@firebase/app-compat":["'$nm'/app-compat/dist/src/index.d.ts"],
      "@firebase/storage":["'$nm'/storage/dist/src/index.d.ts"],
      "@firebase/util":["'$nm'/util/dist/util.d.ts"]}}' \
    "$sdk/node_modules/.bin/mocha" test/integration/integration.test.ts --file src/index.ts \
    --config ../../config/mocharc.node.js --reporter json --reporter-option "output=$results/js-compat.json") \
    >"$results/js-compat.log" 2>&1 || true
  [[ -s $results/js-compat.json ]] || fail "suite js-compat left no report (see $results/js-compat.log)"
}

# run_ct runs the conformance files as firebase-tools' run.sh does (mocha from the repository
# root), in emulator mode: firebase.json's ports name the adapter, the preload stands in for the
# CLI's emulators (ct/preload.cjs). The processes get their own HOME, so applicationDefault()
# finds no credentials of this machine's user.
run_ct() {
  log "suite ct: firebase-tools $TOOLS_TAG storage-emulator-integration/conformance (mocha)"
  node -e '
    const fs = require("fs"); const f = process.argv[1]; const c = JSON.parse(fs.readFileSync(f, "utf8"));
    c.emulators.storage.port = Number(process.argv[2]); c.emulators.auth.port = Number(process.argv[2]);
    delete c.emulators.hub; fs.writeFileSync(f, JSON.stringify(c, null, 2) + "\n");
  ' "$tools/scripts/storage-emulator-integration/firebase.json" "$adapter_port"
  mkdir -p "$work/home"
  (cd "$tools" && HOME=$work/home CLOUDSDK_CONFIG=$work/home/gcloud NODE_OPTIONS="--no-strip-types $guard" \
    ARUMA_CT_FIXTURES=$tools/fixtures.json \
    PUPPETEER_EXECUTABLE_PATH=$headless \
    node_modules/.bin/mocha --no-config --require ts-node/register --require ./aruma-preload.cjs --timeout 2000 --exit \
    'scripts/storage-emulator-integration/conformance/*.test.ts' \
    --reporter json --reporter-option "output=$results/ct.json") >"$results/ct.log" 2>&1 || true
  [[ -s $results/ct.json ]] || fail "suite ct left no report (see $results/ct.log)"
}

# run_captures records every scenario of capture.mjs on Aruma, through the adapter, into
# results/captures/, then compares each with the emulator's recording.
run_captures() {
  log "captures: test/oracle/storage/capture/capture.mjs on Aruma"
  rm -rf "$results/captures"
  printf '{"target": "emulator", "version": "%s (through test/compat/storage/adapter.mjs)", "upstream": "http://127.0.0.1:%s", "proxyPort": %s, "bucket": "%s", "projectId": "%s", "authEmulator": "127.0.0.1:%s", "run": "s3d-%s"}\n' \
    "$aruma_version" "$adapter_port" "$proxy_port" "$(field $CAPTURE_PROJECT bucket)" "$CAPTURE_PROJECT" "$adapter_port" "$(date -u +%Y%m%d)" \
    >"$work/capture.json"
  (cd "$capture" && NODE_OPTIONS="$guard" HOME=$work/home GO_ADMIN=$capture/go-admin \
    node capture.mjs "$work/capture.json" "$results/captures") >"$results/captures.log" 2>&1 || true
  node "$here/captures.mjs" "$root/test/oracle/storage/emulator" "$results/captures" "$here/classified.json" \
    --json "$results/captures-diff.json" >"$results/captures-diff.txt" || captures_unclassified=1
}
captures_unclassified=

rm -f "$results/netguard.log"
for s in "${suites[@]}"; do rm -f "$results/$s.json" "$results/$s.log"; done
start_aruma
has js- && project "$JS_PROJECT" "$here/js/storage.rules" "$here/js/cors.json"
has ct && project "$CT_PROJECT" "$tools/scripts/storage-emulator-integration/storage.rules"
mkdir -p "$work/home"
if has js-; then
  adapter "$JS_PROJECT"
  start_front
  js_env
  has js-node && run_vitest js-node node
  has js-browser && run_vitest js-browser browser
  has js-compat && run_compat
fi
if has ct; then
  adapter "$CT_PROJECT"
  [[ -n ${ARUMA_FRONT_SPKI:-} ]] || start_front
  run_ct
fi
if has captures; then
  # A key file names the public URL it was made under (token_uri), so this project comes after
  # the restart.
  serve "http://127.0.0.1:$proxy_port"
  project "$CAPTURE_PROJECT" "$root/test/oracle/storage/capture/emulator/storage.rules"
  adapter "$CAPTURE_PROJECT"
  run_captures
fi

{
  echo "date: $(date -u +%FT%TZ)"
  echo "aruma: $aruma_version"
  echo "firebase-js-sdk: $SDK_COMMIT (firebase@13.0.0, @firebase/storage $(node -p "require('$sdk/packages/storage/package.json').version" 2>/dev/null))"
  echo "firebase-tools: $TOOLS_TAG ($TOOLS_COMMIT)"
  echo "node: $(node --version), go: $(go env GOVERSION), chromium: $("$headless" --version 2>/dev/null || echo "headless shell $CHROMIUM")"
} >"$results/versions.txt"
log "results ($results)"
cat "$results/versions.txt"
rc=0
ARUMA_CT_SOURCES=$tools/scripts/storage-emulator-integration/conformance \
  node "$here/summarize.mjs" "$results" "$here/classified.json" "${suites[@]}" || rc=$?
[[ -z $captures_unclassified ]] || rc=1
exit "$rc"
