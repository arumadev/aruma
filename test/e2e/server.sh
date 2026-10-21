# Sourced by the device end-to-end runners (android-auth, ios-auth and the *-rtdb suites): starts a
# fresh Aruma server on a throwaway Postgres database with test/smoke/genconfig, which also writes
# the config files the Management API serves, and stops it again (which drops the database).
#
#   ARUMA_E2E_ADDR       listen address (default 127.0.0.1:0: any free port)
#   ARUMA_E2E_DB_PREFIX  prefix of the database's name (default aruma_e2e_)
#   ARUMA_TEST_DATABASE_URL  the Postgres admin URL (default: the local server, see storetest)
#
# After start_server: ARUMA_URL, ARUMA_CLI_TOKEN and ARUMA_PROJECT_ID are set, and the files are in
# the directory given. With genconfig's -switch, ARUMA_SWITCH_URL too: the server's off switch.

server_pid=

# start_server <dir> [genconfig flags...] runs genconfig -serve in the background from the
# repository root ($root); the database suites add -rtdb -switch <addr>.
start_server() {
  local dir=$1
  shift
  rm -rf "$dir"
  mkdir -p "$dir"
  (cd "$root" && go build -o "$dir/genconfig" ./test/smoke/genconfig)
  "$dir/genconfig" -out "$dir" -serve -anonymous \
    -addr "${ARUMA_E2E_ADDR:-127.0.0.1:0}" -db-prefix "${ARUMA_E2E_DB_PREFIX:-aruma_e2e_}" "$@" \
    >"$dir/server.env" 2>"$dir/server.log" &
  server_pid=$!
  for _ in $(seq 1 240); do
    grep -q ARUMA_CLI_TOKEN "$dir/server.env" 2>/dev/null && break
    kill -0 "$server_pid" 2>/dev/null || { cat "$dir/server.log" >&2; server_pid=; return 1; }
    sleep 0.25
  done
  grep -q ARUMA_CLI_TOKEN "$dir/server.env" || { echo "Aruma did not start" >&2; return 1; }
  # shellcheck disable=SC1091
  . "$dir/server.env"
  ARUMA_PROJECT_ID=$(python3 -c 'import json, sys; print(json.load(open(sys.argv[1]))["projectId"])' "$dir/expected.json")
  export ARUMA_URL ARUMA_CLI_TOKEN ARUMA_PROJECT_ID
  if [[ -n "${ARUMA_SWITCH_URL:-}" ]]; then export ARUMA_SWITCH_URL; fi
}

# put_rtdb_rules <file> replaces the database's rules as `firebase deploy --only database` does:
# PUT /.settings/rules.json with an admin credential (the CLI token).
put_rtdb_rules() {
  curl -fsS -X PUT -H "Authorization: Bearer $ARUMA_CLI_TOKEN" --data-binary "@$1" \
    "$ARUMA_URL/.settings/rules.json?ns=$ARUMA_PROJECT_ID-default-rtdb" >/dev/null
}

# stop_server: SIGTERM makes genconfig stop the server and drop its database.
stop_server() {
  if [[ -n "$server_pid" ]]; then
    kill "$server_pid" 2>/dev/null || true
    wait "$server_pid" 2>/dev/null || true
  fi
  server_pid=
}
