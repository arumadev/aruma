#!/usr/bin/env bash
# Known vulnerabilities in the npm packages of the repository, from their package-lock.json
# (nothing is installed). What ships (dependencies) fails at any severity; the tools that build
# and test (devDependencies) fail from high up, and their lower findings are printed for the next
# `npm audit fix`.
#
#   test/ci/npm-audit.sh            from anywhere in the checkout
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
status=0
for dir in dashboard sdk/web/aruma-web sdk/node/admin-auth sdk/node/admin-storage; do
  echo "== $dir (dependencies)"
  (cd "$dir" && npm audit --omit=dev --audit-level=low) || status=1
  echo "== $dir (all, failing from high)"
  (cd "$dir" && npm audit --audit-level=high) || status=1
done
exit $status
