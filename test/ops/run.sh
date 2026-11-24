#!/usr/bin/env bash
# Runs the operational acceptance tests: backup and restore, and the upgrade from the previous
# release. Needs a Postgres 18 (ARUMA_TEST_DATABASE_URL), the PostgreSQL 18 client tools
# (ARUMA_PG_BIN or PATH) and Go. The upgrade tests build the release named by ARUMA_OLD_TAG
# (default v0.4.0) from git; set ARUMA_OLD_BIN to use a binary you have, or ARUMA_OLD_TAG=none
# to skip them. The tests take about four minutes.
#
#   test/ops/run.sh [go test flags]
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
tag=${ARUMA_OLD_TAG:-v0.4.0}
if [[ -z "${ARUMA_OLD_BIN:-}" && "$tag" != none ]]; then
  ARUMA_OLD_BIN=$(test/ops/build-old.sh "$tag")
  export ARUMA_OLD_BIN
fi
ARUMA_OPS=1 exec go test -count=1 -timeout 30m "$@" ./test/ops/
