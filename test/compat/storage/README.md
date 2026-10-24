# Firebase's own Cloud Storage tests, against a throwaway Aruma

`run.sh` runs the Storage tests Firebase publishes, unmodified, against a fresh Aruma built
from this checkout, and records the emulator captures of `test/oracle/storage` again on it.
Every difference is classified in `classified.json`, and the classes and numbers are in
[docs/compat/storage-v0.3.md](../../../docs/compat/storage-v0.3.md) ("اختبارات Firebase الرسمية").

```sh
test/compat/storage/run.sh                       # every suite, then the captures
test/compat/storage/run.sh js-node js-browser    # some of them
STORAGE_COMPAT_LIMITS=default test/compat/storage/run.sh ct   # Aruma's default per-address limits
```

The script's header lists every setting. It needs Go, Node 24.12 or newer, curl, tar, openssl,
psql and the local Postgres, the Chromium of `.cache/ms-playwright` (revision 1248, Playwright
1.64.1), and the network the first time.

## What runs

| Suite | Upstream | Tests |
|---|---|---|
| `js-node` | firebase-js-sdk `firebase@13.0.0` (`0c331cc`), `packages/storage/test/integration`, vitest project `node`, as `test:node` | 13 |
| `js-browser` | the same file, vitest project `browser` in Chromium, as `test:browser:integration` | 13 |
| `js-compat` | `packages/storage-compat/test/integration`, mocha in Node, as `test:node` | 8 |
| `ct` | firebase-tools `v15.34.0` (`48f511a`), `scripts/storage-emulator-integration/conformance/*.test.ts`, mocha, as its `run.sh` | 123: 119 counted, and 4 `emulatorOnly` that run but are reported apart |
| `captures` | `test/oracle/storage/capture/capture.mjs`, its 13 scenarios, then `captures.mjs` compares each step with `test/oracle/storage/emulator` | 188 steps |

Not run: the packages' unit tests (no server), `packages/storage/test/{node,browser}` (unit
tests of the platform layer), and `storage-compat` in a browser: upstream runs it with karma
and the monorepo's webpack build, and its 8 tests are the Node file's.

The versions are the repository's: `firebase@13.0.0` is what the Auth suites run (the RTDB
suites run its sources 14 commits later, unchanged for these packages), and firebase-tools
15.34.0 is the emulator of the captures and the CLI of `test/acceptance`.

## Sources and installs

Into `.cache/upstream/storage` of the main checkout (`ARUMA_CACHE_DIR`), never the repository:

- firebase-js-sdk: the commit's archive from codeload.github.com (6.2 MB), of which
  `packages/storage`, `packages/storage-compat`, `packages/component/src`, `config/`,
  `scripts/ensure_playwright.js` and `LICENSE` are kept (2.6 MB). `js/package-lock.json`
  installs what they import (189 packages, 120 MB): the `@firebase` packages at the versions
  `firebase@13.0.0` ships, the test tools at the versions its `yarn.lock` pins, and Playwright
  1.64.1 (not the monorepo's 1.55.1) so that the browser is the one already in the cache.
- firebase-tools: the tag's archive (4.3 MB), of which the conformance directory, the fixture
  file it imports, `tsconfig.json` and `LICENSE` are kept (0.3 MB). `ct/package-lock.json`
  installs the versions its `npm-shrinkwrap.json` pins (453 packages, 265 MB), with
  `--ignore-scripts`, so puppeteer downloads no browser. `npm pack firebase@9.16.0` and
  `@9.9.1` give the three `firebase-*-compat.js` builds the tests' page adds from gstatic.
- `test/oracle/storage/capture`: copied out with its lockfile (238 MB of `node_modules`), and
  its Go Admin client built there.

A stamp holds each source's commit and lockfile hash; a change installs again.

## How the suites reach Aruma

One Aruma on loopback port 9410 (`STORAGE_COMPAT_PORT`; 9411 the adapter, 9412 the front, 9413
the vitest browser server, 9414 the capture proxy), on a database `aruma_s3d_<random>` dropped
at exit, with Storage on and `ARUMA_PROJECT_DOMAIN=appspot.com`, so a project's bucket has the
name the tests and captures use (`<project>.appspot.com`). `setup.mjs` makes three projects
through the console API, as the dashboard would: `compat-tests` (js), `fake-project-id` (ct,
env.ts's default) and `demo-aruma-ref` (captures), each with a web app, anonymous and password
sign-in, its Storage rules, and a service account key.

**The js suites run as production code does.** They never call `connectStorageEmulator`, so
the SDK speaks to `firebasestorage.googleapis.com` and Auth to `identitytoolkit.googleapis.com`.
In Node, `hosts.cjs` (after `test/experiments/netguard.cjs`) resolves those names, and nothing
else, to the local TLS front; in Chromium the launch's resolver rules do it, and the front's
certificate is trusted by its SPKI hash alone. `front.mjs` hands each request to Aruma the way a
runtime redirect does, the host becoming the first path segment
(`/firebasestorage.googleapis.com/v0/...`, ADR-022 §1). `config/project.json` names the
project, its bucket and its real API key. The rules are `js/storage.rules` (read and write under
`public/` for a signed-in user: Firebase's own test project and rules are not published), and
the bucket has the CORS of `js/cors.json` (the documented `cors.json`), without which a browser
cannot `getBytes` from another origin, on Firebase as here.

**The conformance tests and the captures run in their emulator mode**, pointed at
`adapter.mjs`, which forwards every request unchanged in method, path and body, and swaps what
a deployment would carry for the emulators' fixed stand-ins: `key=fake-api-key` becomes the
project's API key, `Bearer owner` (and the capture script's fixed `ya29.fake-access-token`) a
service account's access token, a GCS request without `Authorization` (the Admin SDKs in
emulator mode send none) the same token, `/b/...` gets its `/storage/v1` back
(@google-cloud/storage 6.x takes `STORAGE_EMULATOR_HOST` as its base URL), and
`POST /internal/reset` empties the bucket. Aruma serves none of this.

- `ct/preload.cjs` stands in for the CLI's emulators (`EmulatorEndToEndTest` starts nothing),
  for the fixture module (only `IMAGE_FILE_BASE64`), and for `googleapis` (production mode
  only); it launches puppeteer on the Chromium headless shell with resolver rules that send
  `example.com` and `www.gstatic.com` to the front, which serves an empty page and the npm
  builds. The tests' `firebase.json` gets the adapter's port for both emulators. The mocha run
  is upstream's: ts-node with firebase-tools' `tsconfig.json` (transpile only), a 2 s timeout,
  chai-as-promised.
- Every test comes from one address, a few hundred uploads in a minute, so the run raises
  Aruma's per-address upload limits; with the defaults most `ct` tests meet 429
  (ADR-023 §3-3), which `STORAGE_COMPAT_LIMITS=default` shows.
- For the captures the server is restarted with the recording proxy as its public URL, so the
  session URLs it hands out lead back through the recorder, as the emulator's did (it builds
  them from `Host`). The re-recordings land in `results/captures/`, beside the emulator's in
  the repository, with `source: "emulator"` because capture.mjs only knows that target; their
  `version` says Aruma.

## Offline

Every Node process loads `test/experiments/netguard.cjs` and refuses any connection off the
machine; the browsers resolve nothing but loopback and the front's names. `summarize.mjs`
prints the refused connections (none so far). The processes of `ct` and the captures get
their own `HOME`, so `applicationDefault()` finds no credential of this machine's user.

## Results

In `.cache/upstream/storage/results`: each suite's report (`<suite>.json`) and log,
`results.json` (one entry per test, with its class), `captures/` and `captures-diff.txt`,
`versions.txt`, and the logs of Aruma, the adapter, the front and the network guard.
`summarize.mjs` prints each suite's totals and every failure with its class, and
`captures.mjs` every kind of capture difference with its count; `--verbose` lists each
difference under its rule.

Exit codes: 0 when every failure and difference is classified; 1 when one is not, or a
classified test is missing from the run; 2 usage; 3 setup failed.

## Latest run (2026-10-24, Aruma `s3-d-compat`, Node 24.21, Chromium 1248)

| Suite | Tests | Passed | Failed: a | b | c |
|---|---|---|---|---|---|
| js-node | 13 | 13 | 0 | 0 | 0 |
| js-browser | 13 | 13 | 0 | 0 | 0 |
| js-compat | 8 | 8 | 0 | 0 | 0 |
| ct (not emulatorOnly) | 119 | 108 | 0 (9 fixed) | 9 | 2 |
| captures | 188 steps | | 836 differences of 64 kinds: a 0, b 24 kinds (293), c 40 kinds (543) |

A whole run with warm caches takes about two minutes, most of it the two browsers.
