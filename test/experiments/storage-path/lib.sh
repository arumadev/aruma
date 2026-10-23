# Shared by the S0-B runners: builds and starts the recording stub (stub/main.go), and stops it.
#
#   . test/experiments/storage-path/lib.sh
#   start_stub <dir> [stub flags…]   # exports STUB_HTTP, STUB_HTTPS, STUB_GUARD, STUB_LOG, STUB_CA, STUB_BIN
#   stop_stub
#
#   S0PATH_PORT  first of three ports: http, https, guard proxy (default 8820, so 8820-8822)

s0_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
s0_root=$(cd "$s0_dir/../../.." && pwd)
[[ -f "$s0_root/../../../.cache/env.sh" ]] && source "$s0_root/../../../.cache/env.sh"
[[ -f "$s0_root/.cache/env.sh" ]] && source "$s0_root/.cache/env.sh"
stub_pid=

start_stub() {
  local dir=$1
  shift
  local p=${S0PATH_PORT:-8820}
  mkdir -p "$dir"
  STUB_BIN=$dir/stub
  (cd "$s0_root" && go build -o "$STUB_BIN" ./test/experiments/storage-path/stub)
  STUB_LOG=$dir/requests.jsonl
  : >"$STUB_LOG"
  "$STUB_BIN" -http "127.0.0.1:$p" -https "127.0.0.1:$((p + 1))" -guard "127.0.0.1:$((p + 2))" \
    -certdir "$dir/tls" -log "$STUB_LOG" "$@" 2>"$dir/stub.err" &
  stub_pid=$!
  for _ in $(seq 50); do
    curl -sf "http://127.0.0.1:$p/healthz" >/dev/null && break
    sleep 0.1
  done
  curl -sf "http://127.0.0.1:$p/healthz" >/dev/null || { cat "$dir/stub.err" >&2; return 1; }
  export STUB_HTTP=http://127.0.0.1:$p STUB_HTTPS=https://localhost:$((p + 1)) STUB_GUARD=http://127.0.0.1:$((p + 2))
  export STUB_LOG STUB_CA=$dir/tls/ca.pem STUB_BIN
}

stop_stub() {
  if [[ -n "$stub_pid" ]]; then
    kill "$stub_pid" 2>/dev/null
    wait "$stub_pid" 2>/dev/null
    stub_pid=
  fi
}
