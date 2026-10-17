#!/usr/bin/env bash
# T23: firebase-tools and the FlutterFire CLI against Aruma, configured only through the
# environment. Starts a throwaway Aruma (test/smoke/genconfig -serve), puts a TLS proxy in front
# of it (firebase-tools sends its credential only to https origins), runs the CLI commands with
# every non-loopback connection refused and recorded, and checks the results.
#
#   test/experiments/firebase-cli.sh            # firebase-tools, phone-home probe, FlutterFire
#   SKIP_FLUTTERFIRE=1 test/experiments/firebase-cli.sh
#
# Needs Go, Node 20+, openssl, the local Postgres and, for FlutterFire, Flutter/Dart on PATH.
# On macOS every CLI also runs under sandbox-exec with outbound network limited to loopback.
# Results are written up in docs/research/experiments.md.
set -euo pipefail

exp=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$exp/../.." && pwd)
[[ -f "$root/.cache/env.sh" ]] && source "$root/.cache/env.sh"
source "$exp/server.sh"

work=$(mktemp -d "${TMPDIR:-/tmp}/aruma-t23.XXXXXX")
proxy_pid=""
cleanup() {
  [[ -n "$proxy_pid" ]] && kill "$proxy_pid" 2>/dev/null || true
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
# js <file> <expression over `r` (the parsed JSON) and `x` (expected.json)>: prints the value.
js() {
  node -e '
    const fs = require("fs");
    const s = fs.readFileSync(process.argv[1], "utf8");
    const r = JSON.parse(s.slice(s.indexOf("{"), s.lastIndexOf("}") + 1));
    const x = JSON.parse(fs.readFileSync(process.argv[2], "utf8"));
    const v = eval(process.argv[3]);
    process.stdout.write(typeof v === "string" ? v : JSON.stringify(v));
  ' "$1" "$ARUMA_GEN/expected.json" "$2"
}
is() { [[ "$(js "$1" "$2")" == "$3" ]]; }

log "starting Aruma"
start_aruma "$work"
echo "ARUMA_URL=$ARUMA_URL  token=$(echo "$ARUMA_CLI_TOKEN" | redact)"

log "TLS proxy in front of Aruma"
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj /CN=localhost \
  -addext "subjectAltName=IP:127.0.0.1,DNS:localhost" \
  -keyout "$work/tls-key.pem" -out "$work/tls-cert.pem" 2>/dev/null
node "$exp/tls-proxy.mjs" "$work/tls-cert.pem" "$work/tls-key.pem" "$ARUMA_URL" >"$work/proxy.out" &
proxy_pid=$!
for _ in $(seq 1 50); do [[ -s "$work/proxy.out" ]] && break; sleep 0.1; done
ARUMA_HTTPS=$(head -1 "$work/proxy.out")
echo "ARUMA_HTTPS=$ARUMA_HTTPS"

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
mkdir -p "$work/config" "$work/run"

# The configuration a developer needs: the two URLs, the token, and the two opt-outs. The rest
# (sandbox, netguard, XDG_CONFIG_HOME, NODE_EXTRA_CA_CERTS for the self-signed proxy cert) is
# test scaffolding.
aruma_env=(
  FIREBASE_API_URL="$ARUMA_HTTPS"
  FIREBASE_TOKEN_URL="$ARUMA_HTTPS"
  FIREBASE_TOKEN="$ARUMA_CLI_TOKEN"
  CI=true
  NO_UPDATE_NOTIFIER=1
)
scaffold_env=(
  PATH="$fb_bin:$PATH"
  XDG_CONFIG_HOME="$work/config"
  NODE_EXTRA_CA_CERTS="$work/tls-cert.pem"
  NODE_OPTIONS="--require $exp/netguard.cjs"
  ARUMA_NETGUARD_LOG="$guard_log"
)
# fb <name> <args...>: runs firebase, stdout to $work/<name>.json, stderr to $work/<name>.err.
fb() {
  local name=$1; shift
  (cd "$work/run" && ${sandbox[@]+"${sandbox[@]}"} env "${scaffold_env[@]}" "${aruma_env[@]}" firebase "$@" >"$work/$name.json" 2>"$work/$name.err")
}
show() { # show <name> <args...>: prints the command and its redacted output.
  local name=$1; shift
  echo "\$ firebase $*"
  redact <"$work/$name.json"
  echo
}

fb projects projects:list --json
show projects projects:list --json
check "projects:list lists smoke-app with its number" is "$work/projects.json" \
  'r.status + " " + r.result.map(p => p.projectId + "/" + (p.projectNumber === x.projectNumber)).join()' "success smoke-app/true"

fb apps apps:list --project smoke-app --json
show apps apps:list --project smoke-app --json
check "apps:list shows the three genconfig apps" is "$work/apps.json" \
  'r.result.map(a => a.platform + ":" + a.appId).sort().join()' \
  "$(js "$ARUMA_GEN/expected.json" '["ANDROID:"+r.android.appId, "IOS:"+r.ios.appId, "WEB:"+r.web.appId].sort().join()')"

fb webapp apps:create web "CLI Web" --project smoke-app --json --non-interactive
show webapp apps:create web '"CLI Web"' --project smoke-app --json --non-interactive
check "apps:create web returns a web app id" is "$work/webapp.json" \
  '/^1:\d{12}:web:[0-9a-f]{16}$/.test(r.result.appId) && r.result.displayName' "CLI Web"
web_id=$(js "$work/webapp.json" 'r.result.appId')

fb androidapp apps:create android "CLI Android" --package-name com.aruma.cli --project smoke-app --json --non-interactive
show androidapp apps:create android '"CLI Android"' --package-name com.aruma.cli --project smoke-app --json --non-interactive
check "apps:create android keeps the package name" is "$work/androidapp.json" \
  '/^1:\d{12}:android:[0-9a-f]{16}$/.test(r.result.appId) && r.result.packageName' "com.aruma.cli"
android_id=$(js "$work/androidapp.json" 'r.result.appId')

fb websdk apps:sdkconfig web "$web_id" --project smoke-app --json
show websdk apps:sdkconfig web "$web_id" --project smoke-app --json
check "apps:sdkconfig web returns the new app with the shared web key" is "$work/websdk.json" \
  '[r.result.sdkConfig.appId === "'"$web_id"'", r.result.sdkConfig.apiKey === x.web.apiKey, r.result.sdkConfig.projectId].join()' "true,true,smoke-app"

fb androidsdk apps:sdkconfig android "$android_id" --project smoke-app --json
show androidsdk apps:sdkconfig android "$android_id" --project smoke-app --json
check "apps:sdkconfig android returns google-services.json with com.aruma.cli" is "$work/androidsdk.json" \
  'r.result.fileName + " " + JSON.parse(r.result.fileContents).client.some(c => c.client_info.android_client_info.package_name === "com.aruma.cli")' \
  "google-services.json true"

log "network attempts during the firebase-tools commands"
sed -E 's/ pid=[0-9]+//' "$guard_log" | sort | uniq -c
check "no connection left loopback" bash -c "! grep -q '^BLOCK' '$guard_log'"
check "the CLI did reach Aruma" grep -q '^ALLOW 127.0.0.1' "$guard_log"

log "what the opt-outs prevent (usage tracking opted in, update check due, no CI/NO_UPDATE_NOTIFIER)"
probe_log=$work/probe.log
: >"$probe_log"
mkdir -p "$work/probe-config/configstore"
echo '{"usage": true}' >"$work/probe-config/configstore/firebase-tools.json"
echo '{"optOut": false, "lastUpdateCheck": 0}' >"$work/probe-config/configstore/update-notifier-firebase-tools.json"
(cd "$work/run" && ${sandbox[@]+"${sandbox[@]}"} env "${scaffold_env[@]}" XDG_CONFIG_HOME="$work/probe-config" ARUMA_NETGUARD_LOG="$probe_log" \
  FIREBASE_API_URL="$ARUMA_HTTPS" FIREBASE_TOKEN_URL="$ARUMA_HTTPS" FIREBASE_TOKEN="$ARUMA_CLI_TOKEN" \
  firebase projects:list --json >/dev/null 2>&1) || true
sleep 3 # the update check runs in a detached child process
sed -E 's/ pid=[0-9]+//' "$probe_log" | sort | uniq -c
check "without CI: MOTD fetch from firebase-public.firebaseio.com" grep -q '^BLOCK firebase-public.firebaseio.com:443' "$probe_log"
check "with usage opted in: GA4 to www.google-analytics.com" grep -q '^BLOCK www.google-analytics.com:443' "$probe_log"
check "without NO_UPDATE_NOTIFIER: update check to registry.npmjs.org" grep -q '^BLOCK registry.npmjs.org:443' "$probe_log"

log "plain http: firebase-tools sends 'Bearer owner' instead of the token"
(cd "$work/run" && ${sandbox[@]+"${sandbox[@]}"} env "${scaffold_env[@]}" "${aruma_env[@]}" FIREBASE_API_URL="$ARUMA_URL" \
  firebase projects:list --json >"$work/http.json" 2>/dev/null) || true
redact <"$work/http.json"; echo
check "projects:list over http fails" is "$work/http.json" 'r.status' "error"

log "known Aruma issue: /oauth2/v3/token ignores multipart/form-data (firebase-tools sends multipart)"
for enc in --data-urlencode -F; do
  code=$(curl -s -o "$work/refresh.out" -w '%{http_code}' -X POST "$ARUMA_URL/oauth2/v3/token" \
    $enc grant_type=refresh_token $enc "refresh_token=$ARUMA_CLI_TOKEN")
  echo "refresh grant with ${enc}: HTTP $code $(redact <"$work/refresh.out")"
done

if [[ -n "${SKIP_FLUTTERFIRE:-}" ]]; then
  log "FlutterFire skipped (SKIP_FLUTTERFIRE)"
else
  log "FlutterFire CLI"
  # The copy in PUB_CACHE (what `dart pub global activate` installs), not whatever is on PATH.
  flutterfire=${PUB_CACHE:-$HOME/.pub-cache}/bin/flutterfire
  if [[ ! -x "$flutterfire" ]]; then
    dart pub global activate flutterfire_cli >/dev/null
  fi
  echo "flutterfire_cli $(CI=true "$flutterfire" --version)"
  app=$exp/flutterfire
  # Platform folders and FlutterFire's outputs are generated, not committed: start clean.
  rm -rf "$app/android" "$app/ios" "$app/web" "$app/lib/firebase_options.dart" "$app/firebase.json"
  (cd "$app" && flutter create --empty --platforms android,ios,web --org com.aruma --project-name aruma_flutterfire . >/dev/null)
  : >"$guard_log"
  ff_status=0
  (cd "$app" && ${sandbox[@]+"${sandbox[@]}"} env "${scaffold_env[@]}" "${aruma_env[@]}" "$flutterfire" configure \
    --project=smoke-app --platforms=android,ios,web \
    --android-package-name=com.aruma.smoke --ios-bundle-id=com.aruma.smoke --yes \
    >"$work/flutterfire.out" 2>&1) || ff_status=$?
  # Drop the spinner frames.
  tr '\r' '\n' <"$work/flutterfire.out" | sed -E 's/\x1b\[[0-9;?]*[A-Za-z]//g' | grep -v '^[⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏]' | grep -v '^\s*$' | redact
  check "flutterfire configure exits 0" test "$ff_status" -eq 0
  opts=$app/lib/firebase_options.dart
  check "lib/firebase_options.dart written" test -f "$opts"
  if [[ -f "$opts" ]]; then
    for p in android ios; do
      check "firebase_options.dart has the $p app id and key" bash -c \
        "grep -q \"appId: '$(js "$ARUMA_GEN/expected.json" "r.$p.appId")'\" '$opts' && grep -q \"apiKey: '$(js "$ARUMA_GEN/expected.json" "r.$p.apiKey")'\" '$opts'"
    done
    check "firebase_options.dart has a smoke-app web app and the web key" bash -c \
      "grep -Eq \"appId: '1:$(js "$ARUMA_GEN/expected.json" r.projectNumber):web:[0-9a-f]{16}'\" '$opts' && grep -q \"apiKey: '$(js "$ARUMA_GEN/expected.json" r.web.apiKey)'\" '$opts'"
  fi
  check "android/app/google-services.json written" test -f "$app/android/app/google-services.json"
  check "ios/Runner/GoogleService-Info.plist written" test -f "$app/ios/Runner/GoogleService-Info.plist"
  sed -E 's/ pid=[0-9]+//' "$guard_log" | sort | uniq -c
  check "flutterfire: no connection left loopback" bash -c "! grep -q '^BLOCK' '$guard_log'"
fi

log "summary"
if ((failures > 0)); then
  echo "$failures check(s) failed"
  exit 1
fi
echo "all checks passed"
