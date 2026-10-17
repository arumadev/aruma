# Shared by the experiment scripts (source it, don't run it).
#
#   start_aruma <workdir>   builds test/smoke/genconfig, starts it with -serve and waits for its
#                           exports; sets ARUMA_URL, ARUMA_CLI_TOKEN and ARUMA_GEN (the directory
#                           with service-account.json, expected.json, ...).
#   stop_aruma              sends SIGINT so genconfig drops its throwaway database, and waits.
#
# Needs the local Postgres (ARUMA_TEST_DATABASE_URL or the storetest default) and Go.

ARUMA_PID=""

start_aruma() {
  local work=$1 root
  root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
  ARUMA_GEN=$work/gen
  mkdir -p "$ARUMA_GEN"
  (cd "$root" && go build -o "$work/genconfig" ./test/smoke/genconfig)
  "$work/genconfig" -out "$ARUMA_GEN" -serve >"$work/serve.out" 2>"$work/serve.err" &
  ARUMA_PID=$!
  local i
  for i in $(seq 1 120); do
    if grep -q '^export ARUMA_CLI_TOKEN=' "$work/serve.out" 2>/dev/null; then
      # shellcheck disable=SC1090
      source "$work/serve.out"
      export ARUMA_URL ARUMA_CLI_TOKEN ARUMA_GEN
      return 0
    fi
    if ! kill -0 "$ARUMA_PID" 2>/dev/null; then
      echo "genconfig exited early:" >&2
      cat "$work/serve.err" >&2
      return 1
    fi
    sleep 0.5
  done
  echo "genconfig did not print its exports within 60 s" >&2
  return 1
}

stop_aruma() {
  if [[ -n "$ARUMA_PID" ]] && kill -0 "$ARUMA_PID" 2>/dev/null; then
    kill -INT "$ARUMA_PID" 2>/dev/null || true
    wait "$ARUMA_PID" 2>/dev/null || true
  fi
  ARUMA_PID=""
}

# redact hides Aruma tokens in anything printed.
redact() { sed -E 's/aruma_([a-z]+)_[A-Za-z0-9_-]+/aruma_\1_REDACTED/g'; }
