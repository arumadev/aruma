# Sourced by the transport spike's run scripts: starts and stops the spike server (server/).
#
#   start_spike DIR [server flags]   builds and starts it with -dir DIR, waits for its listeners
#   stop_spike                       stops it (SIGTERM: the drained shutdown)
#
# The ports are the server's defaults: 9100 h2c-connect, 9101 h2c-grpcgo, 9102 tls-connect,
# 9103 tls-grpcgo, 9109 the refusing guard proxy.

spike_pid=

start_spike() {
  local dir=$1
  shift
  mkdir -p "$dir/bin"
  (cd "$spike_root" && go build -o "$dir/bin/spike-server" ./test/spike/documents/transport/server)
  : >"$dir/record.jsonl"
  "$dir/bin/spike-server" -dir "$dir" "$@" >"$dir/server.log" 2>&1 &
  spike_pid=$!
  local port
  for port in 9100 9101 9102 9103 9109; do
    for _ in $(seq 1 100); do
      (exec 3<>"/dev/tcp/127.0.0.1/$port") 2>/dev/null && break
      kill -0 "$spike_pid" 2>/dev/null || { cat "$dir/server.log" >&2; return 1; }
      sleep 0.1
    done
  done
  echo "spike server up (pid $spike_pid), record in $dir/record.jsonl"
}

stop_spike() {
  if [[ -n "$spike_pid" ]]; then
    kill -TERM "$spike_pid" 2>/dev/null || true
    wait "$spike_pid" 2>/dev/null || true
    spike_pid=
  fi
}
