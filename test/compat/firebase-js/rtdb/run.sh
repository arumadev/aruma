#!/usr/bin/env bash
# Runs Firebase's own Realtime Database test suites, unmodified, against a server: the official
# emulator (the baseline), a throwaway Aruma, or a server already running behind its own TLS
# front (external).
#
#   test/compat/firebase-js/run.sh rtdb --target emulator|aruma|external --mode emulator|production
#                                       [--suite all|compat|exp] [--update-baseline]
#
# The suites (firebase-js-sdk at $SDK_COMMIT, run from source in Node with mocha and ts-node, as
# the packages' own test:node scripts do):
#
#   compat  packages/database-compat/test/*.test.ts
#   exp     packages/database/test/exp/integration.test.ts
#
# packages/database-compat/test/browser/crawler_support.test.ts is browser-only and skipped.
#
# Modes, as the suites' helpers (test/helpers/util.ts) pick them:
#
#   emulator    RTDB_EMULATOR_PORT and RTDB_EMULATOR_NAMESPACE: the tests use
#               http://localhost:<port>?ns=<ns>.
#   production  config/project.json's databaseURL, https://<ns>.rtdb.aruma.test: a four-label host
#               (ADR-018) behind TLS. A per-run CA signs the server certificate, and Node trusts it
#               through NODE_EXTRA_CA_CERTS. hosts.cjs resolves *.aruma.test to 127.0.0.1 inside
#               the Node processes only, and tls-proxy.mjs terminates TLS in front of the target
#               and keeps the Host header.
#
# The external target is production mode only, and nothing is started: RTDB_EXTERNAL_URL is
# the databaseURL as a deployment gives it (https://<ns>.<rtdb.domain>[:port], the namespace its
# first label), RTDB_EXTERNAL_CA the CA Node trusts for it (NODE_EXTRA_CA_CERTS; unset, the
# system's), and RTDB_ADMIN_AUTH or RTDB_RULES_HOOK loads the rules. Its name must resolve, by
# the real DNS, to this machine: the network guard lets that one name through only when every
# address it resolves to is loopback (ARUMA_NETGUARD_LOOPBACK_NAMES). test/experiments/
# rtdb-caddy.sh runs it behind Caddy.
#
# Before the tests, the target gets the suites' open rules (config/database.rules.json, as the
# upstream emulator runner loads them) through PUT /.settings/rules.json with the target's admin
# credential; then a WebSocket handshake checks that the target speaks RTDB at all. The results
# are compared with the committed emulator baseline of the same mode: a test that passes there
# and fails here is a regression unless classified.json explains it.
#
# Exit codes: 0 no regression; 1 regressions (listed); 2 usage; 3 setup failed (download,
# install, server, rules, handshake, or a suite that left no report).
#
# Environment:
#   ARUMA_CACHE_DIR          downloads and installs (default: .cache in the main checkout)
#   RTDB_COMPAT_PORT         first of two loopback ports: the target, the TLS front (default 9470)
#   RTDB_COMPAT_NAMESPACE    the database name (default compat-tests-default-rtdb, ADR-018's
#                            <project_id>-default-rtdb for the project compat-tests)
#   RTDB_EMULATOR_JAR        the emulator (default ~/.cache/firebase/emulators/
#                            firebase-database-emulator-v4.11.2.jar, as firebase-tools stores it)
#   RTDB_ADMIN_AUTH          Authorization header for the rules (default: "Bearer owner" for the
#                            emulator; for Aruma a CLI token exchanged for an access token;
#                            none for external)
#   RTDB_EXTERNAL_URL        the external target's databaseURL
#   RTDB_EXTERNAL_CA         the CA file Node trusts for the external target (optional)
#   RTDB_EXTERNAL_VERSION    what the results record as the external target's version
#   RTDB_RULES_HOOK          a command that loads the rules instead; it is called as
#                            <hook> <rules-url> <rules-file>, with RTDB_ADMIN_AUTH and the Node
#                            settings of the run in its environment. "none" skips the step.
#   ARUMA_COMPAT_DB_PREFIX   prefix of Aruma's throwaway database (default aruma_rtdbc_)
#   ARUMA_TEST_DATABASE_URL  Postgres admin URL (default postgres://postgres@127.0.0.1:5432/postgres)
#   ARUMA_PG_BIN             directory of psql, when it is not on PATH
#
# It needs Node 24.12 or newer, curl, tar, openssl, and the network the first time; Java for the
# emulator; Go, psql and Postgres for Aruma. Nothing is interactive.
set -euo pipefail

SDK_COMMIT=d7bb0e16f2b13ba15f74b690596eb70ef145d4f6
EMULATOR_VERSION=4.11.2
PROJECT=compat-tests
SKIPPED_FILES=(packages/database-compat/test/browser/crawler_support.test.ts)

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../../../.." && pwd)

usage() {
  echo "usage: test/compat/firebase-js/run.sh rtdb --target emulator|aruma|external --mode emulator|production [--suite all|compat|exp] [--update-baseline]" >&2
  exit 2
}
[[ ${1:-} == rtdb ]] && shift
target= mode= suite=all update_baseline=
while [[ $# -gt 0 ]]; do
  case $1 in
    --target) target=${2:-}; shift 2 || usage ;;
    --mode) mode=${2:-}; shift 2 || usage ;;
    --suite) suite=${2:-}; shift 2 || usage ;;
    --update-baseline) update_baseline=1; shift ;;
    *) usage ;;
  esac
done
[[ $target == emulator || $target == aruma || $target == external ]] || usage
[[ $mode == emulator || $mode == production ]] || usage
if [[ $target == external ]]; then
  [[ $mode == production ]] || { echo "--target external runs in production mode only" >&2; exit 2; }
  [[ ${RTDB_EXTERNAL_URL:-} == https://* ]] || { echo "--target external needs RTDB_EXTERNAL_URL=https://<ns>.<rtdb.domain>[:port]" >&2; exit 2; }
  [[ -n ${RTDB_ADMIN_AUTH:-} || -n ${RTDB_RULES_HOOK:-} ]] || { echo "--target external needs RTDB_ADMIN_AUTH or RTDB_RULES_HOOK" >&2; exit 2; }
fi
[[ $suite == all || $suite == compat || $suite == exp ]] || usage
[[ -z $update_baseline || ($target == emulator && $suite == all) ]] || {
  echo "--update-baseline records the emulator with every suite; it needs --target emulator and --suite all" >&2
  exit 2
}

# The main checkout's .cache, also from a git worktree.
main_root=$(dirname "$(git -C "$root" rev-parse --path-format=absolute --git-common-dir)")
cache=${ARUMA_CACHE_DIR:-$main_root/.cache}
base=$cache/upstream/rtdb
sdk=$base/firebase-js-sdk
results=$base/results/$target-$mode
port=${RTDB_COMPAT_PORT:-9470}
tls_port=$((port + 1))
ns=${RTDB_COMPAT_NAMESPACE:-$PROJECT-default-rtdb}
host=$ns.rtdb.aruma.test
jar=${RTDB_EMULATOR_JAR:-$HOME/.cache/firebase/emulators/firebase-database-emulator-v$EMULATOR_VERSION.jar}
db_prefix=${ARUMA_COMPAT_DB_PREFIX:-aruma_rtdbc_}
admin_url=${ARUMA_TEST_DATABASE_URL:-postgres://postgres@127.0.0.1:5432/postgres?sslmode=disable}
psql=${ARUMA_PG_BIN:+$ARUMA_PG_BIN/}psql

# Every cache under $cache, none on the system disk.
mkdir -p "$cache/tmp" "$base"
export GOCACHE=${GOCACHE:-$cache/go-build} GOMODCACHE=${GOMODCACHE:-$cache/gomod}
export TMPDIR=${TMPDIR:-$cache/tmp} npm_config_cache=${npm_config_cache:-$cache/npm}

log() { printf '\n==> %s\n' "$*"; }
fail() {
  printf 'setup failed: %s\n' "$*" >&2
  exit 3
}
started=$SECONDS
suites=(compat exp)
[[ $suite == all ]] || suites=("$suite")

# --- the suites' sources and tools (network) ----------------------------------------------------

# prepare downloads the pinned commit's archive and keeps only what the suites read: the two
# packages, config/ and the licence. The @firebase packages they import come from npm at the
# versions that commit's firebase release ships (package.json here), with the test tools at the
# versions its yarn.lock pins, installed from package-lock.json without install scripts. A stamp
# names the commit and the lock, so a change to either installs again.
prepare() {
  local stamp want
  want="$SDK_COMMIT $(openssl dgst -sha256 -r "$here/package-lock.json" | cut -d' ' -f1)"
  stamp=$sdk/.aruma-ready
  [[ -f $stamp && $(cat "$stamp") == "$want" ]] && return
  log "fetching firebase-js-sdk at $SDK_COMMIT and installing the test tools"
  rm -rf "$sdk" "$base/sdk.tar.gz"
  curl -fsSL --retry 3 -o "$base/sdk.tar.gz" "https://codeload.github.com/firebase/firebase-js-sdk/tar.gz/$SDK_COMMIT" ||
    fail "download of firebase-js-sdk $SDK_COMMIT"
  mkdir -p "$sdk"
  local top=firebase-js-sdk-$SDK_COMMIT
  tar -xzf "$base/sdk.tar.gz" -C "$sdk" --strip-components 1 \
    "$top/packages/database" "$top/packages/database-compat" "$top/config" "$top/LICENSE" ||
    fail "extracting the archive"
  rm "$base/sdk.tar.gz"
  cp "$here/package.json" "$here/package-lock.json" "$sdk/"
  (cd "$sdk" && npm ci --ignore-scripts --no-audit --no-fund --loglevel=error) || fail "npm ci"
  echo "$want" >"$stamp"
}

# --- the target (offline from here) -------------------------------------------------------------

work=$(mktemp -d "$cache/tmp/rtdb-compat.XXXXXX")
db=
pids=()
cleanup() {
  for pid in ${pids[@]+"${pids[@]}"}; do
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  done
  # The target's and the TLS front's logs help explain failures.
  cp "$work"/*.log "$results"/ 2>/dev/null || true
  [[ -n $db ]] && "$psql" -q "$admin_url" -c "DROP DATABASE IF EXISTS $db WITH (FORCE)" >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

# wait_for polls a URL until it answers at all (any HTTP status).
wait_for() {
  for _ in $(seq 1 120); do
    curl -s -o /dev/null "$1" && return 0
    sleep 0.25
  done
  return 1
}

start_emulator() {
  [[ -f $jar ]] || fail "no emulator at $jar (firebase setup:emulators:database fetches it)"
  log "starting the RTDB emulator $EMULATOR_VERSION on 127.0.0.1:$port"
  java -jar "$jar" --host 127.0.0.1 --port "$port" >"$work/emulator.log" 2>&1 &
  pids+=($!)
  wait_for "http://127.0.0.1:$port/.json?ns=$ns" || fail "the emulator did not start (see emulator.log)"
  admin_auth=${RTDB_ADMIN_AUTH:-Bearer owner}
  target_version="emulator $EMULATOR_VERSION ($(basename "$jar"))"
}

# json prints one field of a JSON document read from stdin.
json() { node -e 'let s = ""; process.stdin.on("data", d => s += d).on("end", () => console.log(JSON.parse(s)[process.argv[1]]))' "$1"; }

# start_aruma builds this checkout and serves it on a new database, with one project
# ($PROJECT) and its default database ($ns) under rtdb.aruma.test (ADR-018). The rules
# credential is what firebase-tools sends: an access token from the owner's CLI token, through
# the refresh grant.
start_aruma() {
  log "building Aruma"
  (cd "$root" && go build -o "$work/aruma" ./cmd/aruma) || fail "go build"
  local base_url=http://127.0.0.1:$port password login cookie csrf token
  password=$(openssl rand -hex 16)
  db=$db_prefix$(openssl rand -hex 4)
  "$psql" -q "$admin_url" -c "CREATE DATABASE $db" >/dev/null || fail "creating database $db"
  local db_url=${admin_url%%\?*}
  db_url="${db_url%/*}/$db?sslmode=disable"
  export ARUMA_DATABASE_URL=$db_url ARUMA_PUBLIC_URL=$base_url ARUMA_LISTEN=127.0.0.1:$port \
    ARUMA_MASTER_KEY=$(openssl rand -base64 32) ARUMA_METRICS_LISTEN=- ARUMA_RTDB_DOMAIN=rtdb.aruma.test \
    ARUMA_TRUSTED_PROXIES=127.0.0.1/32 ARUMA_MAIL_DEV_MAILBOX=true ARUMA_LOG_LEVEL=warn \
    ARUMA_RTDB_ENABLED=true
  "$work/aruma" migrate up >/dev/null || fail "aruma migrate up"
  printf '%s\n' "$password" | "$work/aruma" accounts create --email owner@compat.test >/dev/null
  log "starting Aruma on $base_url (database $db)"
  "$work/aruma" serve >"$work/aruma.log" 2>&1 &
  pids+=($!)
  wait_for "$base_url/console/v1/status" || fail "Aruma did not start (see aruma.log)"

  # A CLI token, as the dashboard issues one: sign in, then create it with the password.
  login=$(curl -fsS -D "$work/login.headers" -H 'Content-Type: application/json' \
    -d "{\"email\":\"owner@compat.test\",\"password\":\"$password\"}" "$base_url/console/v1/auth:login") || fail "console login"
  cookie=$(grep -i '^set-cookie:' "$work/login.headers" | head -1 | sed -E 's/^[^:]*: *//; s/;.*//' | tr -d '\r')
  csrf=$(json csrfToken <<<"$login")
  token=$(curl -fsS -H "Cookie: $cookie" -H "X-Aruma-CSRF: $csrf" -H 'Content-Type: application/json' \
    -d "{\"name\":\"rtdb-compat\",\"currentPassword\":\"$password\"}" "$base_url/console/v1/me/tokens" | json token) ||
    fail "creating a CLI token"
  curl -fsS -X POST -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
    -d "{\"projectId\":\"$PROJECT\",\"displayName\":\"RTDB compat tests\"}" "$base_url/console/v1/projects" >/dev/null ||
    fail "creating project $PROJECT"
  if [[ -z ${RTDB_ADMIN_AUTH:-} ]]; then
    local access
    access=$(curl -fsS -d grant_type=refresh_token --data-urlencode "refresh_token=$token" "$base_url/oauth2/v3/token" |
      json access_token) || fail "exchanging the CLI token for an access token"
    admin_auth="Bearer $access"
  else
    admin_auth=$RTDB_ADMIN_AUTH
  fi
  target_version="aruma $(git -C "$root" rev-parse --short HEAD)$(git -C "$root" diff --quiet HEAD || echo '+dirty')"
}

# start_external checks the external target's URL: https, no path or query, and the namespace
# as the first label of a host of three labels or more (ADR-018).
start_external() {
  local hostport=${RTDB_EXTERNAL_URL#https://}
  [[ $hostport != */* && $hostport != *\?* ]] || fail "RTDB_EXTERNAL_URL must have no path or query: $RTDB_EXTERNAL_URL"
  external_host=${hostport%%:*}
  [[ $external_host == *.*.* ]] || fail "the host of RTDB_EXTERNAL_URL needs three labels or more: $external_host"
  ns=${external_host%%.*}
  [[ -z ${RTDB_EXTERNAL_CA:-} || -f $RTDB_EXTERNAL_CA ]] || fail "no CA file at $RTDB_EXTERNAL_CA"
  admin_auth=${RTDB_ADMIN_AUTH:-}
  target_version="external ${RTDB_EXTERNAL_VERSION:-$RTDB_EXTERNAL_URL}"
  log "external target $RTDB_EXTERNAL_URL (namespace $ns)"
}

# start_tls writes a CA and a certificate for $host that it signs, valid for a day, and puts the
# TLS front on $tls_port before the target. The key files stay in $work, removed at exit.
start_tls() {
  log "TLS front on 127.0.0.1:$tls_port for https://$host"
  (
    cd "$work"
    openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -keyout ca.key -out ca.pem -days 1 \
      -subj '/CN=Aruma RTDB compat test CA' -addext 'basicConstraints=critical,CA:TRUE' \
      -addext 'keyUsage=critical,keyCertSign'
    openssl req -new -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -keyout server.key -out server.csr \
      -subj "/CN=$host"
    printf 'subjectAltName=DNS:%s\nbasicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature\nextendedKeyUsage=serverAuth\n' \
      "$host" >server.ext
    openssl x509 -req -in server.csr -CA ca.pem -CAkey ca.key -CAcreateserial -days 1 -extfile server.ext -out server.pem
  ) >"$work/openssl.out" 2>&1 || fail "openssl (see $work/openssl.out)"
  # The emulator's hello names its plain-http bind address, where the SDK would send every later
  # connection; the proxy puts the name the client used there instead (see tls-proxy.mjs).
  # RTDB_EMULATOR_HELLO=keep leaves it, to see what that costs.
  local rewrite=()
  [[ $target == emulator && ${RTDB_EMULATOR_HELLO:-} != keep ]] && rewrite=(--rewrite-hello) && hello_rewritten=true
  node "$here/tls-proxy.mjs" ${rewrite[@]+"${rewrite[@]}"} "$work/server.pem" "$work/server.key" "$tls_port" \
    "http://127.0.0.1:$port" >"$work/proxy.out" 2>"$work/proxy.log" &
  pids+=($!)
  for _ in $(seq 1 40); do
    grep -q READY "$work/proxy.out" 2>/dev/null && return 0
    sleep 0.25
  done
  fail "the TLS front did not start (see proxy.log)"
}

# --- the run ------------------------------------------------------------------------------------

prepare
hello_rewritten=false
rm -rf "$results"
mkdir -p "$results"
case $target in
  emulator) start_emulator ;;
  aruma) start_aruma ;;
  external) start_external ;;
esac

# Every Node process from here refuses connections off this machine (netguard.cjs). In
# production mode hosts.cjs, loaded after it, sends *.aruma.test to the TLS front.
export ARUMA_NETGUARD_LOG=$results/netguard.log
node_options="--no-strip-types --require $root/test/experiments/netguard.cjs"
unset RTDB_EMULATOR_PORT RTDB_EMULATOR_NAMESPACE FIREBASE_DATABASE_EMULATOR_HOST NODE_EXTRA_CA_CERTS
if [[ $target == external ]]; then
  # The real DNS resolves the name, and the guard checks that it is this machine.
  database_url=$RTDB_EXTERNAL_URL
  export ARUMA_NETGUARD_LOOPBACK_NAMES=$external_host
  [[ -z ${RTDB_EXTERNAL_CA:-} ]] || export NODE_EXTRA_CA_CERTS=$RTDB_EXTERNAL_CA
  rules_url=$database_url/.settings/rules.json
  printf '{"projectId": "%s", "databaseURL": "%s"}\n' "${ns%-default-rtdb}" "$database_url" >"$sdk/config/project.json"
elif [[ $mode == production ]]; then
  start_tls
  database_url=https://$host
  node_options+=" --require $here/hosts.cjs"
  export NODE_EXTRA_CA_CERTS=$work/ca.pem ARUMA_TEST_TLS_PORT=$tls_port
  rules_url=$database_url/.settings/rules.json
  printf '{"projectId": "%s", "databaseURL": "%s"}\n' "$PROJECT" "$database_url" >"$sdk/config/project.json"
else
  database_url="http://localhost:$port?ns=$ns"
  export RTDB_EMULATOR_PORT=$port RTDB_EMULATOR_NAMESPACE=$ns
  rules_url="http://127.0.0.1:$port/.settings/rules.json?ns=$ns"
  # The helpers require the file in every mode; its values are unused here.
  printf '{"projectId": "%s"}\n' "$PROJECT" >"$sdk/config/project.json"
fi
export NODE_OPTIONS="$node_options"

hook=${RTDB_RULES_HOOK:-}
if [[ $hook != none ]]; then
  log "loading the suites' rules (config/database.rules.json) at $rules_url"
  if [[ -n $hook ]]; then
    RTDB_ADMIN_AUTH=$admin_auth "$hook" "$rules_url" "$sdk/config/database.rules.json" || fail "rules hook $hook"
  else
    RTDB_ADMIN_AUTH=$admin_auth node "$here/rtdb-admin.mjs" rules "$rules_url" "$sdk/config/database.rules.json" ||
      fail "loading the rules"
  fi
fi

log "checking the RTDB handshake at $database_url"
node "$here/rtdb-admin.mjs" handshake "$database_url" >"$results/handshake.json" ||
  fail "no RTDB handshake from the target at $database_url"
cat "$results/handshake.json"

{
  echo "{"
  echo "  \"date\": \"$(date -u +%FT%TZ)\","
  echo "  \"target\": \"$target\", \"mode\": \"$mode\", \"targetVersion\": \"$target_version\","
  echo "  \"databaseURL\": \"$database_url\", \"helloRewritten\": $hello_rewritten,"
  echo "  \"suites\": [$(printf '"%s"' "${suites[@]}" | sed 's/""/", "/g')],"
  echo "  \"firebaseJsSdk\": \"$SDK_COMMIT\","
  echo "  \"node\": \"$(node --version)\","
  echo "  \"skippedFiles\": [$(printf '"%s"' "${SKIPPED_FILES[@]}" | sed 's/""/", "/g')]"
  echo "}"
} >"$results/meta.json"

# run_suite runs one suite as its package's test:node script does (ts-node with the repo's mocha
# config: 5 s timeout, 5 retries), with mocha's JSON report. The paths map each @firebase
# import to the declarations the monorepo's own build would give it (the npm packages' public
# typings hide the internal API these sources use); the code that runs is the npm packages'.
run_suite() {
  local name=$1 pkg=$2 spec=$3 nm=node_modules/@firebase pid
  log "suite $name: $pkg/$spec"
  (cd "$sdk/packages/$pkg" && TS_NODE_FILES=true TS_NODE_CACHE=NO \
    TS_NODE_COMPILER_OPTIONS='{"module":"commonjs","baseUrl":"'"$sdk"'","paths":{
      "@firebase/app":["'$nm'/app/dist/app/src/index.d.ts"],
      "@firebase/app-compat":["'$nm'/app-compat/dist/src/index.d.ts"],
      "@firebase/database":["'$nm'/database/dist/src/index.d.ts"],
      "@firebase/util":["'$nm'/util/dist/util.d.ts"]}}' \
    exec "$sdk/node_modules/.bin/mocha" "$spec" --file src/index.node.ts --config ../../config/mocharc.node.js \
    --reporter json --reporter-option "output=$results/$name.mocha.json") >"$results/$name.log" 2>&1 &
  pid=$!
  pids+=("$pid")
  wait "$pid" || true
  [[ -s $results/$name.mocha.json ]] || fail "suite $name left no report (see $results/$name.log)"
}

suites_started=$SECONDS
for s in "${suites[@]}"; do
  case $s in
    compat) run_suite compat database-compat 'test/*.test.ts' ;;
    exp) run_suite exp database test/exp/integration.test.ts ;;
  esac
done
printf '{"suitesSeconds": %d, "runSeconds": %d}\n' $((SECONDS - suites_started)) $((SECONDS - started)) >"$results/timing.json"

log "results ($results)"
args=("$results" --classified "$here/classified.json")
if [[ -n $update_baseline ]]; then
  args+=(--write-baseline "$here/baseline-emulator-$mode.json")
else
  args+=(--baseline "$here/baseline-emulator-$mode.json")
fi
node "$here/summarize.mjs" "${args[@]}"
