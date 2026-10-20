# Firebase's own Auth integration suites against Aruma

`run.sh` runs the Auth integration tests that Firebase publishes with its open-source SDKs,
written for the Auth emulator, against a throwaway Aruma server. Results and the
classification of every non-passing test: [docs/compat/firebase-js-suites.md](../../../docs/compat/firebase-js-suites.md).

The Realtime Database suites run from `rtdb/`, against the emulator or Aruma:
`run.sh rtdb --target emulator|aruma --mode emulator|production` (see
[rtdb/README.md](rtdb/README.md)).

## What it runs

| Suite | Upstream | Tests |
|---|---|---|
| `js-node` | firebase-js-sdk `firebase@13.0.0`, `packages/auth` | `test/integration/flows`, in Node (mocha), as `yarn test:node:integration:local` |
| `js-browser` | the same | `test/integration/flows`, in headless Chrome (karma), as `yarn test:browser:integration:local` |
| `js-webdriver` | the same | `test/integration/webdriver`, in Chrome through Selenium, as `yarn test:webdriver` (one mocha process per file) |
| `admin` | firebase-admin-node `v14.5.0` | `test/integration/auth.spec.ts` in emulator mode |

## How

```sh
test/compat/firebase-js/run.sh                 # all four suites
test/compat/firebase-js/run.sh js-node admin   # some of them
```

It needs Go, Node 24.12 or newer, git, openssl, `psql` (`ARUMA_PG_BIN`), the local Postgres
(`ARUMA_TEST_DATABASE_URL`) and Google Chrome. The script's header lists every setting.

1. **Prepare (network).** The two repositories are cloned at their pinned tags into
   `.cache/upstream` of the main checkout (`ARUMA_CACHE_DIR`), outside the repository, and
   installed from their lockfiles (`yarn --frozen-lockfile`, `npm ci`). Selenium Manager fetches
   the chromedriver that matches the local Chrome. npm, yarn, Go and Selenium caches all live
   under `.cache`. A stamp file skips the install on later runs; delete a clone to fetch it again.
2. **Server.** A fresh Aruma built from this checkout, on a new database (`aruma_t23_<random>`,
   dropped at the end) and loopback ports 8200 to 8202 (Aruma, the adapter, karma;
   `ARUMA_COMPAT_PORT`). One project, `compat-tests`, with a web app and anonymous sign-in on.
   Enumeration protection is off by default, the emulator's default that the tests assume
   (`ENUMERATION_PROTECTION=on` for Aruma's own default).
3. **Adapter.** `adapter/` stands where the tests expect the emulator. It forwards every call to
   Aruma and serves the emulator-only `/emulator/v1/...` helpers (reset, oobCodes) from Aruma's
   console and Admin APIs. It swaps in real credentials for the tests' fixed ones: the API key,
   `Bearer owner`, and unsigned custom tokens, which it signs with a service account key of the
   project as `createCustomToken` would. It is test code: Aruma itself serves none of this.
4. **Run (offline).** The Node processes load `test/experiments/netguard.cjs`, which refuses any
   connection off the machine. Chrome starts through a launcher that resolves only loopback, so a
   page cannot reach Google either. The summary reports every refused connection.
5. **Results.** In `.cache/upstream/results`: a mocha JSON report per Node suite, the logs,
   `versions.txt`, and the Aruma, adapter and netguard logs. `summarize.mjs` prints the totals, the
   failures and the skipped tests.

## Why it is not in CI

- **Heavy.** A cold run installs the whole firebase-js-sdk monorepo and builds its auth package
  (several GB, tens of minutes). The WebDriver popup and redirect tests each wait for their 20 s
  timeout.
- **Not a pass/fail gate.** Many tests cannot pass by design: they drive emulator-only behaviour
  (unsigned IdP tokens, the emulator's IdP widget) or features Aruma does not offer yet (phone,
  MFA, tenants, email-link sign-in). The result is the classified list in the doc, which a person
  reviews when Auth or the pinned versions change.
- **Needs a desktop Chrome.** The browser suites use the installed Chrome and its matching
  chromedriver, and the WebDriver test page sits on port 4100, fixed upstream.

Every regression it found is covered by a normal test in `test/acceptance`, which does run in CI.
