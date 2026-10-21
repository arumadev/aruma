#!/usr/bin/env bash
# The production path of docs/guides/rtdb-operator.md (ADR-018), end to end on one machine:
# Aruma behind Caddy, every database on a host of its own under a public wildcard DNS name, and
# a certificate per host issued on demand once Aruma's /rtdb/tls-ask vouches for it.
#
#   test/experiments/rtdb-caddy.sh [--skip-suites]
#
# What stands in for the real thing, and nothing else:
#   - DNS: nip.io's public wildcard, so *.rtdb.127.0.0.1.nip.io resolves to 127.0.0.1 by the
#     real DNS (no hosts file, no resolver patching). rtdb.domain is rtdb.127.0.0.1.nip.io.
#   - Certificates: Caddy's local CA (tls internal) instead of an ACME CA, with on_demand and
#     the ask at Aruma's private metrics_listen. Each client trusts that CA itself
#     (NODE_EXTRA_CA_CERTS, a Go cert pool, Chromium's SPKI list); it is never installed in the
#     system keychain (skip_install_trust), and its keys live in the run's directory only.
#   - Ports: Caddy serves https on 8843, not 443, so databaseURL carries the port:
#     https://<project>-default-rtdb.rtdb.127.0.0.1.nip.io:8843.
#
# Checks, in order:
#    1. /rtdb/tls-ask answers 200 for an existing project's database host and 404 otherwise.
#    2. The first connection to a new project's host gets a certificate on demand.
#    3. An unknown name gets none: the ask refuses it and the handshake fails.
#    4. The database is named by the Host header the proxy kept.
#    5. A WebSocket stays open through Caddy for RTDB_CADDY_IDLE seconds of silence both ways.
#    6. Long polling (the web SDK in Chromium, WebSockets refused) survives the same idle time.
#    7. A client recovers when Aruma restarts behind Caddy, and no new certificate is needed.
#    8. Google's RTDB suites in production mode through Caddy (run.sh rtdb --target external).
#    9. The Admin Node and Admin Go smoke tests through Caddy.
#
# Every Node process runs under test/experiments/netguard.cjs, which lets *.127.0.0.1.nip.io
# through only while the real DNS resolves it to loopback; the Go test does the same itself.
# The logs, the Caddyfile and the reports are kept in $ARUMA_CACHE_DIR/rtdb-caddy.
#
# Environment:
#   RTDB_CADDY_PORT         Aruma's port; metrics_listen and the test page use the next two
#                           (default 8820)
#   RTDB_CADDY_FRONT_PORT   Caddy's https port; its http port and admin API use the next two
#                           (default 8843)
#   RTDB_CADDY_IDLE         seconds of idle time checked (default 90; it must stay below
#                           Aruma's 120 s idle close)
#   RTDB_CADDY_DB_PREFIX    prefix of the throwaway database (default aruma_rtdbcaddy_)
#   ARUMA_CACHE_DIR         installs and results (default .cache in the main checkout)
#   ARUMA_TEST_DATABASE_URL Postgres admin URL; ARUMA_PG_BIN the directory of psql
#
# Needs Caddy 2 (brew install caddy), Go, Node 24, curl, openssl, psql, the local Postgres, the
# public DNS for nip.io, and once the network for npm ci and the compat harness's install.
set -euo pipefail

exp=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$exp/../.." && pwd)
main_root=$(dirname "$(git -C "$root" rev-parse --path-format=absolute --git-common-dir)")
cache=${ARUMA_CACHE_DIR:-$main_root/.cache}
psql=${ARUMA_PG_BIN:+$ARUMA_PG_BIN/}psql
admin_url=${ARUMA_TEST_DATABASE_URL:-postgres://postgres@127.0.0.1:5432/postgres?sslmode=disable}
port=${RTDB_CADDY_PORT:-8820}
metrics=127.0.0.1:$((port + 1))
page_port=$((port + 2))
https_port=${RTDB_CADDY_FRONT_PORT:-8843}
http_port=$((https_port + 1))
caddy_admin=127.0.0.1:$((https_port + 2))
idle=${RTDB_CADDY_IDLE:-90}
db=${RTDB_CADDY_DB_PREFIX:-aruma_rtdbcaddy_}$(openssl rand -hex 4)
skip_suites=
[[ ${1:-} == --skip-suites ]] && skip_suites=1

zone=127.0.0.1.nip.io
domain=rtdb.$zone
main_host=aruma.$zone
public_url=https://$main_host:$https_port
url_of() { echo "https://$1-default-rtdb.$domain:$https_port"; }

mkdir -p "$cache/tmp"
export GOCACHE=${GOCACHE:-$cache/go-build} GOMODCACHE=${GOMODCACHE:-$cache/gomod}
export TMPDIR=${TMPDIR:-$cache/tmp} npm_config_cache=${npm_config_cache:-$cache/npm}
export PLAYWRIGHT_BROWSERS_PATH=${PLAYWRIGHT_BROWSERS_PATH:-$cache/ms-playwright}
results=$cache/rtdb-caddy
work=$(mktemp -d "$cache/tmp/rtdb-caddy.XXXXXX")
aruma_pid= caddy_pid=
pids=()

cleanup() {
  for p in ${pids[@]+"${pids[@]}"} $aruma_pid $caddy_pid; do kill "$p" 2>/dev/null || true; done
  wait 2>/dev/null || true
  mkdir -p "$results"
  cp "$work"/*.log "$work"/*.json "$work/Caddyfile" "$results"/ 2>/dev/null || true
  "$psql" -q "$admin_url" -c "DROP DATABASE IF EXISTS $db WITH (FORCE)" >/dev/null 2>&1 || true
  # Caddy's CA keys and the service account key go with the directory.
  rm -rf "$work"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

log() { printf '\n==> %s\n' "$*"; }
passed=0 failed=0
check() {
  local name=$1
  shift
  if "$@"; then
    printf 'PASS  %s\n' "$name"
    passed=$((passed + 1))
  else
    printf 'FAIL  %s\n' "$name"
    failed=$((failed + 1))
  fi
}
json() { node -e 'let s = ""; process.stdin.on("data", d => s += d).on("end", () => { let v = JSON.parse(s); for (const k of process.argv[1].split(".")) v = v?.[k]; console.log(v ?? "") })' "$1"; }

command -v caddy >/dev/null || { echo "Caddy is not installed (brew install caddy)" >&2; exit 3; }
rm -rf "$results"
mkdir -p "$results"
{
  echo "date: $(date -u +%FT%TZ)"
  echo "aruma: $(git -C "$root" rev-parse --short HEAD)$(git -C "$root" diff --quiet HEAD || echo '+dirty')"
  echo "caddy: $(caddy version)"
  echo "node: $(node --version), go: $(go env GOVERSION)"
  echo "dns: $(dig +short "probe-default-rtdb.$domain" 2>/dev/null | head -1) for probe-default-rtdb.$domain"
} | tee "$work/versions.log"

# --- installs (network, first run only) -----------------------------------------------------------

modules=$cache/upstream/rtdb/firebase-js-sdk/node_modules
[[ -d $modules/@firebase/database ]] || { echo "run test/compat/firebase-js/run.sh rtdb once to install the SDK" >&2; exit 3; }
[[ -d $exp/rtdb-lp-browser/node_modules/firebase ]] || (cd "$exp/rtdb-lp-browser" && npm ci --no-audit --no-fund --silent)
(cd "$root/test/smoke/admin-node" && npm ci --no-audit --no-fund --silent)

# --- Aruma ----------------------------------------------------------------------------------------

log "building Aruma; database $db"
(cd "$root" && go build -o "$work/aruma" ./cmd/aruma)
"$psql" -q "$admin_url" -c "CREATE DATABASE $db" >/dev/null
db_url=${admin_url%%\?*}
# trusted_proxies: Caddy connects from 127.0.0.1, and without it every client would count as
# that one address for the per-address limits (the guide says so).
export ARUMA_DATABASE_URL="${db_url%/*}/$db?sslmode=disable" ARUMA_PUBLIC_URL=$public_url \
  ARUMA_LISTEN=127.0.0.1:$port ARUMA_METRICS_LISTEN=$metrics ARUMA_MASTER_KEY=$(openssl rand -base64 32) \
  ARUMA_RTDB_ENABLED=true ARUMA_RTDB_DOMAIN=$domain ARUMA_TRUSTED_PROXIES=127.0.0.1/32 ARUMA_LOG_LEVEL=info
"$work/aruma" migrate up >/dev/null
password=$(openssl rand -hex 16)
printf '%s\n' "$password" | "$work/aruma" accounts create --email owner@caddy.test >/dev/null

serve() {
  "$work/aruma" serve >>"$work/aruma.log" 2>&1 &
  aruma_pid=$!
  for _ in $(seq 1 100); do curl -fs "http://127.0.0.1:$port/healthz" >/dev/null && return; sleep 0.1; done
  echo "Aruma did not start:" >&2
  tail -20 "$work/aruma.log" >&2
  exit 3
}
serve

# --- Caddy ----------------------------------------------------------------------------------------

# The guide's Caddyfile (docs/guides/rtdb-operator.md §3), with Caddy's local CA instead of
# ACME, the ports above, and the reverse_proxy settings of §5.
cat >"$work/Caddyfile" <<EOF
{
	http_port $http_port
	https_port $https_port
	admin $caddy_admin
	skip_install_trust
	# Not in the guide: the TLS loggers at DEBUG, the only level that shows the ask's refusals.
	log tls {
		level DEBUG
		include tls
		output file $work/caddy-tls.log
	}
	on_demand_tls {
		ask http://$metrics/rtdb/tls-ask
	}
}

$main_host {
	tls internal
	reverse_proxy 127.0.0.1:$port
}

*.$domain {
	tls internal {
		on_demand
	}
	reverse_proxy 127.0.0.1:$port {
		transport http {
			dial_timeout 5s
		}
		stream_close_delay 5m
	}
}
EOF
log "starting Caddy (https $https_port, http $http_port, admin $caddy_admin)"
cat "$work/Caddyfile"
XDG_DATA_HOME=$work/caddy-data XDG_CONFIG_HOME=$work/caddy-config \
  caddy run --config "$work/Caddyfile" --adapter caddyfile >"$work/caddy.log" 2>&1 &
caddy_pid=$!
store=$work/caddy-data/caddy
ca=$store/pki/authorities/local/root.crt
for _ in $(seq 1 100); do
  [[ -s $ca ]] && curl -fs --cacert "$ca" -o /dev/null "$public_url/healthz" && break
  sleep 0.2
done
[[ -s $ca ]] || { echo "Caddy did not start:" >&2; tail -20 "$work/caddy.log" >&2; exit 3; }
https() { curl -sS --cacert "$ca" "$@"; }
certs() { find "$store/certificates" -name '*.crt' 2>/dev/null | sed 's#.*/##' | sort; }

# --- projects, through Caddy's main host ------------------------------------------------------------

log "signing in at $public_url through Caddy"
login=$(https -f -D "$work/login.headers" -H 'Content-Type: application/json' \
  -d "{\"email\":\"owner@caddy.test\",\"password\":\"$password\"}" "$public_url/console/v1/auth:login")
cookie=$(grep -i '^set-cookie:' "$work/login.headers" | head -1 | sed -E 's/^[^:]*: *//; s/;.*//' | tr -d '\r')
rm "$work/login.headers"
csrf=$(json csrfToken <<<"$login")
token=$(https -f -H "Cookie: $cookie" -H "X-Aruma-CSRF: $csrf" -H 'Content-Type: application/json' \
  -d "{\"name\":\"caddy\",\"currentPassword\":\"$password\"}" "$public_url/console/v1/me/tokens" | json token)
access=$(https -f -d grant_type=refresh_token --data-urlencode "refresh_token=$token" "$public_url/oauth2/v3/token" | json access_token)
api() { https -f -X "$1" -H "Authorization: Bearer $token" -H 'Content-Type: application/json' ${3:+-d "$3"} "$public_url/console/v1$2"; }
for p in caddy-test compat-tests smoke-app; do
  api POST /projects "{\"projectId\":\"$p\",\"displayName\":\"$p\"}" >/dev/null
done
# What Aruma itself gives out (the management API firebase-tools reads, and the config files).
generated=$(https -f -H "Authorization: Bearer $access" \
  "$public_url/v1beta/projects/caddy-test/locations/-/instances/caddy-test-default-rtdb" | json databaseUrl)
echo "Aruma's databaseUrl for caddy-test: $generated (rtdb.domain takes no port; the clients here add :$https_port)"

# --- 1. the ask -------------------------------------------------------------------------------------

log "1. /rtdb/tls-ask on metrics_listen"
ask() { curl -s -o /dev/null -w '%{http_code}' "http://$metrics/rtdb/tls-ask?domain=$1"; }
check "ask: an existing project's database host is 200" test "$(ask "caddy-test-default-rtdb.$domain")" = 200
check "ask: a database that does not exist is 404" test "$(ask "nope-default-rtdb.$domain")" = 404
check "ask: a host two labels deep is 404" test "$(ask "a.caddy-test-default-rtdb.$domain")" = 404
check "ask: the main host is 404" test "$(ask "$main_host")" = 404
check "ask: the public entry point does not serve it" test "$(https -o /dev/null -w '%{http_code}' "$public_url/rtdb/tls-ask?domain=caddy-test-default-rtdb.$domain")" != 200

# --- 2. a certificate on demand -----------------------------------------------------------------------

log "2. the first connection to caddy-test's host"
host=caddy-test-default-rtdb.$domain
before=$(certs)
echo "certificates before: $(echo $before)"
first=$(https -o /dev/null -w '%{http_code} %{time_appconnect}' "$(url_of caddy-test)/.settings/rules.json")
echo "rules.json without a credential: $first (status, seconds to the end of the TLS handshake)"
after=$(certs)
echo "certificates after: $(echo $after)"
check "no certificate for the host before its first connection" bash -c "! grep -qx '$host.crt' <<<'$before'"
check "the first connection gets one, and reaches the database (403 without a credential)" \
  bash -c "grep -qx '$host.crt' <<<'$after' && [[ '${first%% *}' == 403 ]]"
openssl s_client -connect "127.0.0.1:$https_port" -servername "$host" </dev/null 2>/dev/null |
  openssl x509 -noout -subject -issuer -ext subjectAltName -dates | tee "$work/cert.log"
check "the certificate names the host and comes from Caddy's local CA" \
  bash -c "grep -q 'DNS:$host' '$work/cert.log' && grep -q 'Caddy Local Authority' '$work/cert.log'"
grep -i "obtain\|certificate" "$work/caddy.log" | grep "$host" | head -3 | cut -c1-300 || true

# --- 3. an unknown name gets nothing --------------------------------------------------------------------

log "3. names the ask refuses"
for name in "nope-default-rtdb.$domain" "a.caddy-test-default-rtdb.$domain"; do
  code=0
  curl -sS --cacert "$ca" -o /dev/null "https://$name:$https_port/.json" 2>"$work/refused.err" || code=$?
  echo "$name: curl exit $code, $(head -c 160 "$work/refused.err")"
  found=$(find "$store/certificates" -name "$name.crt" 2>/dev/null)
  check "no certificate and no TLS session for $name" test "$code" != 0 -a -z "$found"
done
grep 'issuance denied' "$work/caddy-tls.log" | cut -c1-400 || true
check "Caddy asked Aruma about the unknown database and was refused (404)" \
  grep -q "issuance denied.*nope-default-rtdb.$domain.*permission module http://$metrics/rtdb/tls-ask - non-2xx status code 404" "$work/caddy-tls.log"

# --- 4. the Host header ------------------------------------------------------------------------------

log "4. the database is named by the Host header"
check "straight to Aruma, Host alone names the database" \
  test "$(curl -s -o /dev/null -w '%{http_code}' -H "Host: $host:$https_port" "http://127.0.0.1:$port/.settings/rules.json")" = 403
check "without it, the request names no database" \
  test "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$port/.settings/rules.json")" != 403

# Open rules for caddy-test: the idle, long-poll and restart clients are unauthenticated.
https -f -X PUT -H "Authorization: Bearer $access" -d '{"rules": {".read": true, ".write": true}}' \
  "$(url_of caddy-test)/.settings/rules.json" >/dev/null

# --- 5 and 6. idle connections ---------------------------------------------------------------------------

log "5 and 6. a WebSocket and a long-poll session idle for $idle s, side by side"
spki=$(for f in "$store"/pki/authorities/local/{root,intermediate}.crt; do
  openssl x509 -in "$f" -pubkey -noout | openssl pkey -pubin -outform der | openssl dgst -sha256 -binary | openssl base64
done | paste -sd, -)
guard=(--require "$exp/netguard.cjs")
export ARUMA_NETGUARD_LOOPBACK_NAMES=.$zone ARUMA_NETGUARD_LOG=$work/netguard.log
NODE_EXTRA_CA_CERTS=$ca RTDB_ADMIN_AUTH="Bearer $access" \
  node "${guard[@]}" "$exp/rtdb-caddy/idle-ws.mjs" "$(url_of caddy-test)" "$idle" >"$work/idle-ws.json" 2>"$work/idle-ws.log" &
ws_pid=$!
pids+=("$ws_pid")
RTDB_CA_SPKI=$spki RTDB_PAGE_PORT=$page_port \
  node "${guard[@]}" "$exp/rtdb-caddy/lp-idle.mjs" "$(url_of caddy-test)" "$idle" >"$work/lp-idle.json" 2>"$work/lp-idle.log" &
lp_pid=$!
pids+=("$lp_pid")
ws_status=0 lp_status=0
wait "$ws_pid" || ws_status=$?
wait "$lp_pid" || lp_status=$?
node -e 'for (const f of process.argv.slice(1)) { const r = JSON.parse(require("fs").readFileSync(f, "utf8")); console.log(f.replace(/.*\//, ""), JSON.stringify(r.checks, null, 1), r.error || "") }' \
  "$work/idle-ws.json" "$work/lp-idle.json" || true
check "WebSocket through Caddy, $idle s silent both ways" test "$ws_status" = 0
check "long polling through Caddy, $idle s without data" test "$lp_status" = 0

# --- 7. restart ---------------------------------------------------------------------------------------------

log "7. Aruma restarts behind Caddy"
obtained_before=$(grep -ci 'certificate obtained successfully' "$work/caddy.log" || true)
NODE_PATH=$modules NODE_EXTRA_CA_CERTS=$ca node "${guard[@]}" "$exp/rtdb-restart/client.cjs" "$(url_of caddy-test)" >"$work/restart.log" 2>&1 &
client=$!
pids+=("$client")
for _ in $(seq 1 150); do grep -q '"step":"ready"' "$work/restart.log" && break; sleep 0.1; done
kill -TERM "$aruma_pid"
wait "$aruma_pid" || true
during=$(https -o /dev/null -w '%{http_code}' "$(url_of caddy-test)/.json" || true)
sleep 3
serve
restart_status=0
wait "$client" || restart_status=$?
cat "$work/restart.log"
echo "while Aruma was down, Caddy answered $during"
obtained_after=$(grep -ci 'certificate obtained successfully' "$work/caddy.log" || true)
check "the client reconnects through Caddy and its queued write lands" test "$restart_status" = 0
check "no new certificate after the restart" test "$obtained_before" = "$obtained_after"

# --- 8. Google's suites -------------------------------------------------------------------------------------

if [[ -z $skip_suites ]]; then
  log "8. Google's RTDB suites, production mode, through Caddy"
  suites=0
  RTDB_EXTERNAL_URL=$(url_of compat-tests) RTDB_EXTERNAL_CA=$ca RTDB_ADMIN_AUTH="Bearer $access" \
    RTDB_EXTERNAL_VERSION="aruma $(git -C "$root" rev-parse --short HEAD) behind Caddy $(caddy version | cut -d' ' -f1)" \
    "$root/test/compat/firebase-js/run.sh" rtdb --target external --mode production >"$work/suites.log" 2>&1 || suites=$?
  tail -30 "$work/suites.log"
  check "Google's suites: no regression against the emulator baseline" test "$suites" = 0
fi

# --- 9. Admin SDK smoke tests ---------------------------------------------------------------------------------

log "9. Admin Node and Admin Go through Caddy"
smoke=$work/smoke
mkdir -p "$smoke"
sa=$(api POST /projects/smoke-app/serviceAccounts '{"accountId":"smoke-backend"}' | json id)
api POST "/projects/smoke-app/serviceAccounts/$sa/keys" >"$smoke/service-account.json"
chmod 600 "$smoke/service-account.json"
printf '{"projectId": "smoke-app", "databaseURL": "%s"}\n' "$(url_of smoke-app)" >"$smoke/expected.json"
echo "token_uri: $(json token_uri <"$smoke/service-account.json")"
export ARUMA_SMOKE_DIR=$smoke ARUMA_RTDB_DATABASE_URL=$(url_of smoke-app) ARUMA_RTDB_CA=$ca
node_smoke=0 go_smoke=0
(cd "$root/test/smoke/admin-node" && npm run --silent test:rtdb) >"$work/admin-node.log" 2>&1 || node_smoke=$?
grep -E '^# (tests|pass|fail)' "$work/admin-node.log" || tail -20 "$work/admin-node.log"
check "Admin Node smoke test through Caddy" test "$node_smoke" = 0
(cd "$root/test/smoke/admin-go" && go test -count=1 -v ./rtdb/) >"$work/admin-go.log" 2>&1 || go_smoke=$?
grep -E '^(--- |ok|FAIL)' "$work/admin-go.log" | head -30
check "Admin Go smoke test through Caddy" test "$go_smoke" = 0
cp "$smoke/netguard.log" "$work/netguard-admin-node.log" 2>/dev/null || true

blocked=$(cat "$work"/netguard*.log 2>/dev/null | grep -c '^BLOCK' || true)
check "no connection beyond loopback from the Node clients" test "$blocked" = 0

log "$passed passed, $failed failed (logs in $results)"
[[ $failed == 0 ]]
