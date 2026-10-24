# Sourced by run.sh and measure.sh: starts the suite's Aruma (server/main.go) and stops what was
# started. Expects $root, $here and $gen; sets ARUMA_URL and ARUMA_E2E_DIR.
#
#   ARUMA_E2E_ADDR       Aruma's address (default 127.0.0.1:9420)
#   ARUMA_E2E_DB_PREFIX  prefix of the throwaway database (default aruma_e2e_)

addr=${ARUMA_E2E_ADDR:-127.0.0.1:9420}
export ARUMA_URL="http://$addr" ARUMA_E2E_DIR=$gen
export ARUMA_E2E_PAGE_PORT=${ARUMA_E2E_PAGE_PORT:-9421}

log() { printf '\n==> %s\n' "$*"; }

pids=()
stop_all() {
  local pid
  for pid in "${pids[@]}"; do
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  done
  pids=()
}

# start_aruma builds and starts the server on a fresh database, and waits for e2e.json.
start_aruma() {
  rm -rf "$gen"
  mkdir -p "$gen"
  (cd "$root" && go build -o "$gen/server" ./test/e2e/web-storage/server)
  "$gen/server" -addr "$addr" -db-prefix "${ARUMA_E2E_DB_PREFIX:-aruma_e2e_}" -out "$gen" >"$gen/server.out" 2>"$gen/server.log" &
  local pid=$!
  pids+=("$pid")
  for _ in $(seq 1 240); do
    grep -q ready "$gen/server.out" 2>/dev/null && return 0
    kill -0 "$pid" 2>/dev/null || { cat "$gen/server.log" >&2; return 1; }
    sleep 0.25
  done
  echo "Aruma did not start" >&2
  return 1
}

# wait_http <url>: until the URL answers.
wait_http() {
  for _ in $(seq 1 240); do
    curl -fsS -o /dev/null "$1" 2>/dev/null && return 0
    sleep 0.5
  done
  echo "$1 did not answer" >&2
  return 1
}
