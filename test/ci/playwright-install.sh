#!/usr/bin/env bash
# Installs a Playwright browser and its system packages on a runner, repeating a try that failed.
#
#   test/ci/playwright-install.sh <engine>      (run from the directory of the project that pins Playwright)
#
# `playwright install --with-deps` runs apt-get as root. A try that is cut off by `timeout` kills
# npx but not the apt-get below it, which keeps the dpkg lock: the next try then died at once with
# "Could not get lock /var/lib/dpkg/lock-frontend. It is held by process ... (apt-get)" and the job
# failed on a slow mirror. So each try is cut at five minutes as before, and before the next one
# whatever apt or dpkg is still running is waited for (up to ten minutes). What it finished stays
# downloaded, so the next try is shorter. apt also waits for the lock itself for a few minutes.
set -euo pipefail

engine=${1:?usage: playwright-install.sh <engine>}

busy() { pgrep -x apt-get >/dev/null || pgrep -x dpkg >/dev/null || pgrep -x apt >/dev/null; }

for attempt in 1 2 3; do
  if timeout 5m npx playwright install --with-deps "$engine"; then
    exit 0
  fi
  echo "playwright install failed or timed out (attempt $attempt of 3)" >&2
  for _ in $(seq 120); do busy || break; sleep 5; done
  if busy; then
    echo "playwright install: apt-get is still running after ten minutes" >&2
    exit 1
  fi
done
exit 1
