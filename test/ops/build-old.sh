#!/usr/bin/env bash
# Builds the aruma binary of an earlier release for the upgrade tests and prints its path.
#
#   test/ops/build-old.sh [tag]        default tag: v0.4.0
#
# The tag must be in this clone (git fetch origin tag <tag>). The source is exported with
# git archive into a temporary directory, so the working tree is not touched.
set -euo pipefail
tag=${1:-v0.4.0}
cd "$(git rev-parse --show-toplevel)"
git rev-parse --verify --quiet "refs/tags/$tag" >/dev/null || git fetch --quiet origin tag "$tag"
dir=$(mktemp -d "${TMPDIR:-/tmp}/aruma-old-XXXXXX")
git archive "$tag" | tar -x -C "$dir"
(cd "$dir" && go build -ldflags "-X main.version=${tag#v}" -o "$dir/aruma" ./cmd/aruma)
echo "$dir/aruma"
