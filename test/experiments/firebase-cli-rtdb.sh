#!/usr/bin/env bash
# R5: firebase-tools' Realtime Database commands against Aruma, in production form. Starts a
# throwaway Aruma with -rtdb-domain rtdb.aruma.test and its TLS front (test/smoke/genconfig), so
# the database is https://smoke-app-default-rtdb.rtdb.aruma.test as in a real deployment; puts
# an https front before the main host for the management APIs (firebase-cli-rtdb/api-front.mjs);
# runs database:set/get/update/push/remove and deploy --only database with every non-loopback
# connection refused and recorded, and checks each result against Aruma's REST API.
#
#   test/experiments/firebase-cli-rtdb.sh
#   DB_PREFIX=aruma_r5_ ARUMA_ADDR=127.0.0.1:8755 RTDB_TLS_ADDR=127.0.0.1:8756 API_PORT=8757 \
#     test/experiments/firebase-cli-rtdb.sh
#
# firebase-tools finds the database through two management calls Aruma answers: the project's
# resources.realtimeDatabaseInstance, and the instance's databaseUrl from the Realtime Database
# management API (FIREBASE_RTDB_MANAGEMENT_URL).
#
# Inside the CLI's Node process *.aruma.test resolves to the TLS front (hosts.cjs from the
# firebase-js RTDB suite); nothing on the machine changes. Needs Go, Node 20+, openssl, curl and
# the local Postgres. On macOS the CLI also runs under sandbox-exec with outbound network limited
# to loopback. Results are written up in docs/research/experiments.md.
set -euo pipefail

exp=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$exp/../.." && pwd)
[[ -f "$root/.cache/env.sh" ]] && source "$root/.cache/env.sh"
source "$exp/server.sh"

domain=rtdb.aruma.test
ns=smoke-app-default-rtdb
work=$(mktemp -d "${TMPDIR:-/tmp}/aruma-cli-rtdb.XXXXXX")
front_pid=""
cleanup() {
  [[ -n "$front_pid" ]] && kill "$front_pid" 2>/dev/null || true
  stop_aruma
  rm -rf "$work"
}
trap cleanup EXIT INT TERM

failures=0
log() { printf '\n==> %s\n' "$*"; }
pass() { printf 'PASS  %s\n' "$*"; }
fail() { printf 'FAIL  %s\n' "$*"; failures=$((failures + 1)); }
check() { # check <description> <command...>
  local what=$1; shift
  if "$@"; then pass "$what"; else fail "$what"; fi
}
# jsonis <file> <expression over r, the file's JSON> <expected>: true when the expression,
# printed as JSON (strings bare), equals expected.
jsonis() {
  [[ "$(node -e '
    const fs = require("fs");
    let r;
    try { r = JSON.parse(fs.readFileSync(process.argv[1], "utf8")); } catch (e) { r = undefined; }
    const v = eval(process.argv[2]);
    process.stdout.write(typeof v === "string" ? v : JSON.stringify(v) ?? "undefined");
  ' "$1" "$2")" == "$3" ]]
}
# same <file-a> <file-b>: the two files hold equal JSON, key order aside.
same() {
  node -e '
    const fs = require("fs");
    const canon = (v) => v && typeof v === "object" && !Array.isArray(v)
      ? Object.fromEntries(Object.keys(v).sort().map((k) => [k, canon(v[k])])) : v;
    const [a, b] = process.argv.slice(1).map((f) => canon(JSON.parse(fs.readFileSync(f, "utf8"))));
    process.exit(JSON.stringify(a) === JSON.stringify(b) ? 0 : 1);
  ' "$1" "$2"
}
# rest <name> <path> [query]: reads <path>.json from Aruma directly (development form, ?ns=,
# with the CLI token as an admin credential) into $work/<name>.rest.
rest() {
  local q=${3:+&$3}
  curl -s -o "$work/$1.rest" -H "Authorization: Bearer $ARUMA_CLI_TOKEN" "$ARUMA_URL$2.json?ns=$ns$q"
}

log "starting Aruma (rtdb.domain $domain, TLS front for the database hosts)"
start_aruma "$work" -addr "${ARUMA_ADDR:-127.0.0.1:0}" -db-prefix "${DB_PREFIX:-aruma_smoke_}" \
  -rtdb-domain "$domain" -rtdb-tls "${RTDB_TLS_ADDR:-127.0.0.1:0}"
tls_port=${ARUMA_RTDB_TLS_ADDR##*:}
echo "ARUMA_URL=$ARUMA_URL  ARUMA_RTDB_TLS_ADDR=$ARUMA_RTDB_TLS_ADDR  token=$(echo "$ARUMA_CLI_TOKEN" | redact)"
echo "databaseURL in the app configs: $(node -p 'require(process.argv[1]).databaseURL' "$ARUMA_GEN/expected.json")"

log "https front for the main host (management APIs)"
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj /CN=localhost \
  -addext "subjectAltName=IP:127.0.0.1,DNS:localhost" \
  -keyout "$work/tls-key.pem" -out "$work/tls-cert.pem" 2>/dev/null
front_log=$work/api-front.log
: >"$front_log"
node "$exp/firebase-cli-rtdb/api-front.mjs" "$work/tls-cert.pem" "$work/tls-key.pem" "$ARUMA_URL" "${API_PORT:-0}" "$front_log" >"$work/front.out" &
front_pid=$!
for _ in $(seq 1 50); do [[ -s "$work/front.out" ]] && break; sleep 0.1; done
ARUMA_HTTPS=$(head -1 "$work/front.out")
echo "ARUMA_HTTPS=$ARUMA_HTTPS"
# One bundle for both fronts: the run's RTDB CA and the main host's self-signed certificate.
cat "$ARUMA_GEN/rtdb-ca.pem" "$work/tls-cert.pem" >"$work/ca-bundle.pem"

log "firebase-tools"
if [[ ! -d "$exp/firebase-cli/node_modules" ]]; then
  (cd "$exp/firebase-cli" && npm ci --no-audit --no-fund --silent)
fi
fb_bin=$exp/firebase-cli/node_modules/.bin
echo "firebase-tools $("$fb_bin/firebase" --version 2>/dev/null)"

sandbox=()
if command -v sandbox-exec >/dev/null; then
  sandbox=(sandbox-exec -p '(version 1)(allow default)(deny network-outbound)(allow network-outbound (remote ip "localhost:*"))(allow network-outbound (remote unix-socket))')
fi
guard_log=$work/netguard.log
: >"$guard_log"
mkdir -p "$work/config" "$work/logs"

# What a developer sets: the management API origins and the token, plus the two opt-outs and
# FIREBASE_SKIP_INFORMATIONAL_IAM (otherwise every database command first asks
# cloudresourcemanager.googleapis.com for testIamPermissions; it ignores the answer, but the
# call leaves the machine). No FIREBASE_REALTIME_URL: the database host comes from the instance's
# databaseUrl, https://smoke-app-default-rtdb.rtdb.aruma.test, as in production.
aruma_env=(
  FIREBASE_API_URL="$ARUMA_HTTPS"
  FIREBASE_TOKEN_URL="$ARUMA_HTTPS"
  FIREBASE_RTDB_MANAGEMENT_URL="$ARUMA_HTTPS"
  FIREBASE_TOKEN="$ARUMA_CLI_TOKEN"
  FIREBASE_SKIP_INFORMATIONAL_IAM=1
  CI=true
  NO_UPDATE_NOTIFIER=1
)
# Test scaffolding: the sandbox, netguard, *.aruma.test resolution, the CAs, and DEBUG so
# firebase-debug.log (every request the CLI sends) is kept for each command.
scaffold_env=(
  PATH="$fb_bin:$PATH"
  XDG_CONFIG_HOME="$work/config"
  NODE_EXTRA_CA_CERTS="$work/ca-bundle.pem"
  NODE_OPTIONS="--require $exp/netguard.cjs --require $root/test/compat/firebase-js/rtdb/hosts.cjs"
  ARUMA_TEST_TLS_PORT="$tls_port"
  ARUMA_NETGUARD_LOG="$guard_log"
  DEBUG=true
)
# fb <name> <args...>: runs firebase --json in $work/run/<name> (or $FB_CWD), stdout to
# $work/<name>.json, stderr to $work/<name>.err, the debug log to $work/logs/<name>.log.
# Returns the CLI's exit status.
fb() {
  local name=$1 dir status=0; shift
  dir=${FB_CWD:-$work/run/$name}
  mkdir -p "$dir"
  (cd "$dir" && ${sandbox[@]+"${sandbox[@]}"} env "${scaffold_env[@]}" "${aruma_env[@]}" ${FB_ENV[@]+"${FB_ENV[@]}"} \
    firebase "$@" --json >"$work/$name.json" 2>"$work/$name.err") || status=$?
  [[ -f "$dir/firebase-debug.log" ]] && mv "$dir/firebase-debug.log" "$work/logs/$name.log"
  echo "\$ firebase $* --json   (exit $status)"
  return $status
}
# sent <name>: the database and management requests a command sent, with their statuses.
sent() {
  grep -E '\[apiv2\]\[(query|status)\]' "$work/logs/$1.log" 2>/dev/null | sed -E 's/^\[debug\] //' | redact | sed 's/^/    /' || true
}
show() { redact <"$work/$1.json" | head -c 2000; echo; }
out() { cat "$work/$1.out" 2>/dev/null || echo "(no output file)"; }

# The project the CLI deploys from, and the data it writes.
proj=$work/project
mkdir -p "$proj"
cat >"$proj/smoke.json" <<'EOF'
{"name": "aruma", "n": 1, "nested": {"x": true, ".priority": 7},
 "scores": {"alice": {"score": 30}, "bob": {"score": 10}, "carol": {"score": 20}}}
EOF
echo '{"database": {"rules": "database.rules.json"}}' >"$proj/firebase.json"
cat >"$proj/database.rules.json" <<'EOF'
{
  // Deployed by test/experiments/firebase-cli-rtdb.sh.
  "rules": {
    ".read": "auth != null",
    ".write": "auth != null",
    "smoke": {"scores": {".indexOn": ["score"]}}
  }
}
EOF
bad=$work/bad-project
mkdir -p "$bad"
cp "$proj/firebase.json" "$bad/"
echo '{"rules": {".read": "auth !=", ".write": false}}' >"$bad/database.rules.json"
P=(--project smoke-app)

log "database:set (default instance, data from a file)"
fb set database:set /smoke "$proj/smoke.json" "${P[@]}" --force || true
show set; sent set
check "database:set succeeds" jsonis "$work/set.json" 'r.status' success
rest set /smoke 'format=export'
check "the data is stored, priority included" jsonis "$work/set.rest" \
  '[r.name, r.n, r.nested.x, r.nested[".priority"], r.scores.bob.score].join()' "aruma,1,true,7,10"
check "the CLI addressed the production host" grep -q "PUT https://$ns.$domain/smoke.json" "$work/logs/set.log"

log "database:set --instance --data"
fb setflag database:set /smoke/flag --data true --instance "$ns" "${P[@]}" --force || true
show setflag; sent setflag
rest setflag /smoke/flag
check "database:set --instance --data stores true" jsonis "$work/setflag.rest" 'r' true

log "database:get"
fb get database:get /smoke -o "$work/get.out" "${P[@]}" || true
sent get
out get
rest get /smoke
check "database:get returns the node" same "$work/get.out" "$work/get.rest"
check "database:get leaves the priority out" jsonis "$work/get.out" '".priority" in r.nested' false

fb shallow database:get /smoke --shallow -o "$work/shallow.out" "${P[@]}" || true
sent shallow
out shallow
# Every child as true, leaves included (emulator 4.11.2, see internal/services/rtdb/api/rest.go).
check "database:get --shallow: every child as true" jsonis "$work/shallow.out" \
  'Object.keys(r).sort().map(k => k + "=" + r[k]).join()' "flag=true,n=true,name=true,nested=true,scores=true"

fb pretty database:get /smoke/nested --pretty -o "$work/pretty.out" "${P[@]}" || true
sent pretty
out pretty
check "database:get --pretty is indented" grep -q '^  "x"' "$work/pretty.out"

fb export database:get /smoke/nested --export -o "$work/export.out" "${P[@]}" || true
sent export
out export
check "database:get --export includes .priority" jsonis "$work/export.out" 'r[".priority"] + "," + r.x' "7,true"

fb noindex database:get /smoke/scores --order-by score --limit-to-first 2 -o "$work/noindex.out" "${P[@]}" && noindex=0 || noindex=$?
sent noindex
show noindex
check "database:get --order-by score without .indexOn fails" test "$noindex" -ne 0
check "... with Firebase's 'Index not defined' error" grep -q 'Index not defined' "$work/noindex.json"

fb bykey database:get /smoke/scores --order-by-key --limit-to-last 1 -o "$work/bykey.out" "${P[@]}" || true
sent bykey
out bykey
check "database:get --order-by-key --limit-to-last 1" jsonis "$work/bykey.out" 'Object.keys(r).join()' carol

log "rules before any deploy"
rest rules0 /.settings/rules
cat "$work/rules0.rest"; echo

log "deploy --only database --dry-run, rules with a syntax error"
FB_CWD=$bad fb baddry deploy --only database --dry-run "${P[@]}" && baddry=0 || baddry=$?
show baddry; sent baddry
check "deploy --dry-run of bad rules fails" test "$baddry" -ne 0
check "... with a syntax error" grep -q 'Syntax error in database rules' "$work/baddry.json"
rest rules1 /.settings/rules
check "... and the rules are unchanged" cmp -s "$work/rules0.rest" "$work/rules1.rest"

log "deploy --only database --dry-run"
FB_CWD=$proj fb dry deploy --only database --dry-run "${P[@]}" || true
show dry; sent dry
check "deploy --dry-run succeeds" jsonis "$work/dry.json" 'r.status' success
# The debug log prints a request as "<method> <url> <query>"; without a query that is empty
# or [none].
check "... and sends only a ?dryRun=true PUT" bash -c "grep -q 'PUT https://$ns.$domain/.settings/rules.json dryRun=true' '$work/logs/dry.log' && ! grep -Eq 'PUT https://[^ ]*/.settings/rules.json (\[none\])?$' '$work/logs/dry.log'"
rest rules2 /.settings/rules
check "... and leaves the rules unchanged" cmp -s "$work/rules0.rest" "$work/rules2.rest"

log "deploy --only database"
FB_CWD=$proj fb deploy deploy --only database "${P[@]}" || true
show deploy; sent deploy
check "deploy --only database succeeds" jsonis "$work/deploy.json" 'r.status' success
check "... checking with ?dryRun=true, then releasing" bash -c "grep -q 'PUT https://$ns.$domain/.settings/rules.json dryRun=true' '$work/logs/deploy.log' && grep -Eq 'PUT https://$ns.$domain/.settings/rules.json (\[none\])?$' '$work/logs/deploy.log'"
rest rules3 /.settings/rules
cat "$work/rules3.rest"; echo
check "GET /.settings/rules.json returns the deployed file, comment included" cmp -s "$proj/database.rules.json" "$work/rules3.rest"
rest rules4 /.settings/rules format=strict
check "... and ?format=strict has the index" jsonis "$work/rules4.rest" 'r.rules.smoke.scores[".indexOn"]' '["score"]'

log "database:get with a query on the deployed index"
fb byscore database:get /smoke/scores --order-by score --limit-to-first 2 -o "$work/byscore.out" "${P[@]}" || true
sent byscore
out byscore
check "database:get --order-by score --limit-to-first 2" jsonis "$work/byscore.out" 'Object.keys(r).sort().join()' "bob,carol"
fb equal database:get /smoke/scores --order-by score --equal-to 30 -o "$work/equal.out" "${P[@]}" || true
sent equal
out equal
check "database:get --order-by score --equal-to 30" jsonis "$work/equal.out" 'Object.keys(r).join()' alice

log "database:update"
fb update database:update /smoke --data '{"n": 2, "nested/y": "z"}' "${P[@]}" --force || true
show update; sent update
rest update /smoke
check "database:update merges" jsonis "$work/update.rest" '[r.n, r.nested.x, r.nested.y, r.name].join()' "2,true,z,aruma"

log "database:push"
fb push1 database:push /smoke/list --data '{"t": "first"}' "${P[@]}" || true
show push1; sent push1
fb push2 database:push /smoke/list --data '{"t": "second"}' "${P[@]}" || true
show push2
check "database:push returns a push id" jsonis "$work/push1.json" '/^-[0-9A-Za-z_-]{19}$/.test(r.result.key)' true
rest push /smoke/list
k1=$(node -p 'require(process.argv[1]).result.key' "$work/push1.json" 2>/dev/null || true)
k2=$(node -p 'require(process.argv[1]).result.key' "$work/push2.json" 2>/dev/null || true)
check "the pushed children are stored under their keys, in push order" jsonis "$work/push.rest" \
  "[r['$k1'].t, r['$k2'].t, '$k1' < '$k2'].join()" "first,second,true"

log "the listing database:remove falls back to (listRemote.ts), sent directly"
# database:remove first PATCHes null with writeSizeLimit=tiny, and lists the children only when
# that fails with 400 (write too big). Aruma accepts every writeSizeLimit, so the CLI never
# lists; this is the request it would send.
rest list /smoke 'shallow=true&limitToFirst=2'
cat "$work/list.rest"; echo
check "shallow=true&limitToFirst=2 lists the first two keys" jsonis "$work/list.rest" 'Object.keys(r).join() + "=" + Object.values(r).join()' "flag,list=true,true"

log "database:remove"
fb remove database:remove /smoke "${P[@]}" --force || true
show remove; sent remove
check "database:remove succeeds" jsonis "$work/remove.json" 'r.status' success
check "... with PATCH ?print=silent&writeSizeLimit=tiny" grep -Eq "PATCH https://$ns.$domain/smoke.json .*writeSizeLimit" "$work/logs/remove.log"
rest remove /smoke
check "/smoke is gone" jsonis "$work/remove.rest" 'r' null
fb gone database:get /smoke -o "$work/gone.out" "${P[@]}" || true
check "database:get after remove prints null" jsonis "$work/gone.out" 'r' null
rest rules5 /.settings/rules
check "the deployed rules stay" cmp -s "$proj/database.rules.json" "$work/rules5.rest"

log "management requests through the https front"
sed -E 's/\?.*( ->)/?…\1/' "$front_log" | sort | uniq -c
# Asked of Aruma itself, past the front.
curl -s -o "$work/mgmt-project.rest" -H "Authorization: Bearer $ARUMA_CLI_TOKEN" "$ARUMA_URL/v1beta1/projects/smoke-app"
curl -s -o "$work/mgmt-instance.rest" -w '%{http_code} %{content_type}\n' -H "Authorization: Bearer $ARUMA_CLI_TOKEN" \
  "$ARUMA_URL/v1beta/projects/smoke-app/locations/-/instances/$ns"
check "Aruma reports the default instance (resources.realtimeDatabaseInstance)" \
  jsonis "$work/mgmt-project.rest" 'r.resources.realtimeDatabaseInstance' "$ns"
check "Aruma serves GET /v1beta/projects/{p}/locations/-/instances/{id}" \
  jsonis "$work/mgmt-instance.rest" 'r && /\/instances\//.test(r.name) && r.databaseUrl' "https://$ns.$domain"

log "network attempts during the firebase-tools commands"
sed -E 's/ pid=[0-9]+//' "$guard_log" | sort | uniq -c
check "no connection left loopback" bash -c "! grep -q '^BLOCK' '$guard_log'"
check "the CLI reached the database through the TLS front" grep -q "^ALLOW 127.0.0.1:$tls_port" "$guard_log"

log "without FIREBASE_SKIP_INFORMATIONAL_IAM"
iam_log=$work/iam.log
: >"$iam_log"
FB_ENV=(FIREBASE_SKIP_INFORMATIONAL_IAM= ARUMA_NETGUARD_LOG="$iam_log")
fb iam database:get /smoke -o "$work/iam.out" "${P[@]}" || true
FB_ENV=()
sed -E 's/ pid=[0-9]+//' "$iam_log" | sort | uniq -c
check "testIamPermissions goes to cloudresourcemanager.googleapis.com" grep -q '^BLOCK cloudresourcemanager.googleapis.com:443' "$iam_log"
check "... and the command still succeeds when it is refused" jsonis "$work/iam.out" 'r' null

log "summary"
if ((failures > 0)); then
  echo "$failures check(s) failed"
  exit 1
fi
echo "all checks passed"
