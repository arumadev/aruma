# Firebase's own Realtime Database suites, against any server

`run.sh` runs the Realtime Database tests that Firebase publishes in firebase-js-sdk, unmodified,
against the official emulator (the baseline) or a throwaway Aruma. A test that passes on the
emulator and fails on Aruma is a bug, unless `classified.json` explains it.

```sh
test/compat/firebase-js/run.sh rtdb --target emulator --mode production   # the main mode
test/compat/firebase-js/run.sh rtdb --target aruma    --mode production
test/compat/firebase-js/run.sh rtdb --target aruma    --mode emulator --suite exp
test/compat/firebase-js/run.sh rtdb --target emulator --mode emulator --update-baseline
```

`test/compat/firebase-js/run.sh rtdb ...` hands over to `rtdb/run.sh`; the Auth suites keep
the rest of that script. This is its own folder, not more suites in the Auth script, because
nothing is shared but the folder's purpose: the Auth runner always builds Aruma and goes through
the Auth adapter, clones a whole tag and installs the monorepo with yarn. This one picks a
target and a mode, needs no adapter, and installs 63 MB. The script's header lists every
setting.

## What runs

| Suite | Path, at firebase-js-sdk `d7bb0e1` | Tests |
|---|---|---|
| `compat` | `packages/database-compat/test/*.test.ts` (database, datasnapshot, info, order, order_by, promise, query, servervalues, transaction) | 326: 308 run, 18 skipped upstream (`it.skip`, and all of promise.test.ts is `describe.skip`) |
| `exp` | `packages/database/test/exp/integration.test.ts` | 28 in emulator mode; 24 in production mode, where the four `connectDatabaseEmulator` tests are not defined (`if (USE_EMULATOR)`) |
| not run | `packages/database-compat/test/browser/crawler_support.test.ts` | browser-only (crawler support); listed as skipped in every summary |

The package unit tests (`packages/database/test/*.test.ts`) need no server and are not run.

## The pinned source, and how it is built

- **Commit `d7bb0e16f2b13ba15f74b690596eb70ef145d4f6`**, the one the protocol research read
  (`docs/research/rtdb/A-protocol-sdks.md`), so the line numbers there hold. It is 14 commits
  after the `firebase@13.0.0` release (`0c331cc`), and none of those commits touches
  `packages/database*`, `util`, `app`, `component`, `logger` or `config/mocharc.node.js`. Since
  the `@firebase/database@1.1.5` tag (`6cde0c0`) the two database packages changed only in
  `package.json` (engines, versions). So the tests are those of the released SDK, and the
  `@firebase` packages they import are the npm releases that `firebase@13.0.0` ships.
- **Fetch.** `run.sh` downloads the commit's archive from codeload.github.com (6 MB) and keeps
  `packages/database`, `packages/database-compat`, `config/` and `LICENSE` (2 MB) in
  `.cache/upstream/rtdb/firebase-js-sdk` of the main checkout (`ARUMA_CACHE_DIR`). Nothing
  enters the repository.
- **Install.** `package.json` and `package-lock.json` here list what the suites import: the
  `@firebase` packages at the versions `firebase@13.0.0` depends on (app 0.16.3, app-compat
  0.5.19, database 1.1.5, util 1.15.3, component 0.7.5, logger 0.5.2 and the type packages), and
  the test tools at the versions the monorepo's `yarn.lock` pins (mocha 11.8.0, ts-node 10.9.2,
  typescript 5.5.4, chai 4.5.0, sinon 22.1.0 ...). They are copied into the source root and
  installed with `npm ci --ignore-scripts` (152 packages, 61 MB, npm's cache in `.cache/npm`).
  The yarn install and lerna build of the whole monorepo, which the Auth runner does, is not
  needed. A stamp holds the commit and the lockfile's hash; changing either installs again.
- **Run**, as the packages' own `test:node` scripts do, from the package directory:

  ```sh
  TS_NODE_FILES=true TS_NODE_CACHE=NO TS_NODE_COMPILER_OPTIONS='{"module":"commonjs","baseUrl":"<sdk>","paths":{...}}' \
  NODE_OPTIONS='--no-strip-types --require test/experiments/netguard.cjs [--require rtdb/hosts.cjs]' \
    <sdk>/node_modules/.bin/mocha 'test/*.test.ts' --file src/index.node.ts \
    --config ../../config/mocharc.node.js --reporter json --reporter-option output=<results>/compat.mocha.json
  ```

  The upstream mocha config applies: ts-node, a 5 s timeout and 5 retries per test.
  `--no-strip-types` keeps Node 24 from loading the `.ts` files itself. ts-node type-checks as
  upstream does; the `paths` point `@firebase/app`, `app-compat`, `database` and `util` at the
  per-file declarations inside those npm packages, which include the internal API the sources
  use (the packages' public typings hide it, and the monorepo's own build exposes it). Type
  checking also inlines `@firebase/component`'s `const enum`, which `--transpile-only` cannot.

## Modes

**Production mode is the primary one.** The suites' helpers
(`packages/database-compat/test/helpers/util.ts:45-51`, the same in `packages/database`) read
`databaseURL` from `config/project.json`, which `run.sh` writes:

- `databaseURL` is `https://compat-tests-default-rtdb.rtdb.aruma.test`: a four-label host, the
  shape ADR-018 gives Aruma (`<project_id>-default-rtdb.<rtdb.domain>`).
- **TLS.** Each run makes a CA and a certificate for that name signed by it, valid for a day,
  in a temporary directory removed at exit. Node trusts the CA through `NODE_EXTRA_CA_CERTS`.
- **Name resolution, in Node only.** `hosts.cjs`, preloaded with `--require`, resolves
  `*.aruma.test` to 127.0.0.1 (`dns.lookup`) and sends port 443 of those names to the TLS front
  (`net.Socket#connect`); TLS still sends the name as SNI and checks the certificate against
  it. `/etc/hosts` is never touched. It loads after `netguard.cjs`, which then sees a loopback
  address and refuses anything else.
- **TLS front.** `tls-proxy.mjs` stands in for Caddy: https and wss on port 9471, forwarded to
  the target's plain http port 9470, with `Host` unchanged and `X-Forwarded-Proto`/`-For` added.
  It forwards WebSocket upgrades, which `test/experiments/tls-proxy.mjs` does not.

**Emulator mode** sets `RTDB_EMULATOR_PORT` and `RTDB_EMULATOR_NAMESPACE`, so the tests use
`http://localhost:9470?ns=compat-tests-default-rtdb`. It also works against Aruma, through the
development URL of ADR-018.

**Targets.**
- `emulator`: `firebase-database-emulator-v4.11.2.jar` from firebase-tools' cache, on
  127.0.0.1:9470.
- `aruma`: this checkout built and served on a fresh database (`aruma_rtdbc_<random>`, dropped
  at exit), with `ARUMA_RTDB_DOMAIN=rtdb.aruma.test`, an owner account, and the project
  `compat-tests`. Realtime Database is enabled (`ARUMA_RTDB_ENABLED`) without
  `dev_open_rules`: as on Firebase, the suites get access through the rules the harness loads,
  which the rules engine evaluates on every operation, `.indexOn` included.

## Before the tests

1. **Rules.** The suites mock Auth with a null token (`util.ts`), so they need open rules. The
   harness loads the ones the upstream emulator runner loads, `config/database.rules.json` (read
   and write for all, `.indexOn: testIndex` under `/testing`), with
   `PUT /.settings/rules.json`, then reads them back and compares. The PUT goes the way the tests
   go: in production mode to `https://<ns>.rtdb.aruma.test/.settings/rules.json` through the
   TLS front. The credential is pluggable:
   - The emulator gets `Authorization: Bearer owner`.
   - Aruma gets what `firebase deploy` sends: an access token from the owner's CLI token, by
     the refresh grant at `/oauth2/v3/token`.
   - `RTDB_ADMIN_AUTH` sets the header for either target.
   - `RTDB_RULES_HOOK=<command>` replaces the step; it is called with the rules URL and file.
     `RTDB_RULES_HOOK=none` skips it.
2. **Handshake.** `rtdb-admin.mjs handshake` opens `/.ws?v=5&ns=<ns>` as the SDK does and needs
   the server's hello within 10 s. Without it the run stops (exit 3) instead of letting 350 tests
   time out five times each. The hello goes into the results.

## Results

They are written to `.cache/upstream/rtdb/results/<target>-<mode>/`:

- `results.json`: one entry per test, with suite, file, full title, state, first error line,
  retries and duration, plus the run's metadata, the hello and the timing.
- The raw mocha reports and logs, `handshake.json`, and the TLS front's, emulator's or Aruma's
  log.
- `netguard.log`: every connection the run's Node processes made.

`summarize.mjs` prints the following and exits 1 when there is a regression:

- Per file: passed, failed and skipped, with each failure's first error line and its class.
- The browser-only file, and the connections refused off the machine (none so far).
- The comparison with the committed baseline of the same mode, `baseline-emulator-<mode>.json`.
  Regressions are tests that pass there and fail or are missing here; classified differences
  and tests that only pass here are listed as well.

`--update-baseline` (emulator target only) records a new baseline. Baselines leave out
durations, retries and the hello's time and session, so a new recording differs only where a
result does.

**Exit codes:**
- 0: no regression.
- 1: regressions.
- 2: usage error.
- 3: setup failed: download, install, server, rules, handshake, or a suite left no report.

Nothing is interactive, so CI (R7) only needs Java, Node and the jar, or Go and Postgres.

## Emulator baseline (2026-10-20, emulator 4.11.2, Node 24.21)

| Mode | Passed | Failed | Skipped upstream | Suites | Whole run, warm cache |
|---|---|---|---|---|---|
| emulator | 336 | 0 | 18 | 24 s | 26 s |
| production (TLS, four-label host) | 332 | 0 | 18 | 24 s | 26 s |

- The four tests missing in production mode are the emulator-only ones in `exp`.
- No test needed a retry in the recorded runs, and a second run of each mode matched its
  baseline exactly.
- A cold first run adds the download and `npm ci`: about 40 s with an empty npm cache, 3 s with
  a warm one.
- Disk: 63 MB under `.cache/upstream/rtdb`, plus npm's cache.

**The JS SDK sends `ns=` to every host outside firebaseio.com** (`RepoInfo.ts`,
`repoInfoNeedsQueryParam`). So in production mode the emulator sees
`wss://compat-tests-default-rtdb.rtdb.aruma.test/.ws?v=5&ns=compat-tests-default-rtdb` with that
`Host`, and serves the right database.

**One emulator behaviour breaks production mode: the `h` of its hello.** It is the emulator's
bind address, `127.0.0.1:9470`. The SDK adopts `h` as the host for every later connection of
that database (`Connection.ts:391-393`, `RepoInfo.ts:77-84`), for example after `goOffline()` and
`goOnline()`. That host is plain http, past the TLS front, so those reconnections fail.
- **Emulator mode is not affected:** the address is plain http there too.
- **Production Firebase is not affected:** its `h` is a TLS host serving the database.
- **ADR-018 has Aruma answer with the host the client connected to.**

For the emulator target in production mode, the TLS front therefore rewrites `h` in the first
frame of each WebSocket to the request's `Host` (`--rewrite-hello`, recorded as
`helloRewritten` in the results). `RTDB_EMULATOR_HELLO=keep` turns that off. Without the
rewrite:
- `exp` fails one test, "blocks get requests until the database is online".
- `compat` does not finish. Its tests share one app per connection index (`getRootNode`), so
  once one test reconnects, every later test on that app waits out its 5 s timeout and five
  retries. After 15 minutes the SDK had opened 24 connections straight to 127.0.0.1:9470, and
  the run was stopped.

`classified.json` lists the `exp` failure under `emulator-hello-host`.

## What the suites need from the server

What it takes, beyond storing and syncing data, for the baseline to pass on Aruma:

- **The hello (`h` control frame).**
  - `ts`: the SDK sets `.info/serverTimeOffset` from it. `info.test.ts` expects a number.
  - `h`: a host that serves the same database over the same scheme. Under ADR-018 that is the
    `Host` of the request. It must not start with `s-`, which the SDK caches across sessions.
  - `v`: `"5"`.
  - `s`: the session id.
- **Both `Host` and `ns=`** on every WebSocket, naming the same database (ADR-018's 400 only
  applies when they differ).
- **`.info/connected` and reconnection.** Several tests call `goOffline()`/`goOnline()` and
  expect the reconnection to be quick: "blocks get requests until the database is online" gives
  the reconnection and a `get` 2 s. `.info/*` itself is computed in the SDK and never sent to
  the server.
- **Server time.** "resolves timestamps locally" (`servervalues.test.ts`) wants
  `ServerValue.TIMESTAMP` within 1 s of the client's clock. The increment tests need `increment`
  on null, numbers, strings and objects.
- **Rules.** Open rules, plus `.indexOn: testIndex` under `/testing`, loaded through
  `PUT /.settings/rules.json` and returned by `GET`. One `exp` test filters on `testIndex`.
- **Timing.** Every test has 5 s, five times over. Several wait 2 s to prove that no further
  event comes. A server that answers in tens of milliseconds is far inside that.
- **Several connections at once.** `getRandomNode(n)` and `getFreshRepo` open one connection
  per app to the same database. Each test works under a fresh push key at the root.
- **Nothing from Firebase's real hosts.** `database.test.ts` parses `*.firebaseio.com` URLs and
  `testRepoInfo` (`packages/database/test/helpers/util.ts`) only accepts them, but neither
  suite connects to them. The network guard refused nothing.
