# End-to-end tests

Official Firebase SDKs, unchanged, against a real Aruma server that the runner starts on a
throwaway Postgres database (`test/smoke/genconfig`, which also writes the config files the
Management API serves). Every suite has a network guard: a request to Google, or anywhere off this
machine, fails the run.

| Suite | SDK | Mode | Runner | Where it runs |
|---|---|---|---|---|
| [web-auth](web-auth) | `firebase` 11, 12, 13 in Chromium (Playwright) | emulator, production (`@aruma/web`) | `web-auth/run.sh` | GitHub Actions, every push (`ci.yml`) |
| [android-auth](android-auth) | `firebase-auth` (BoM 35), instrumented tests | production, through `aruma-redirect` | `android-auth/run.sh` | GitHub Actions emulator, weekly and by hand (`devices.yml`) |
| [ios-auth](ios-auth) | `FirebaseAuth` 12, XCTest in an empty host app | production, through `ArumaRedirect` | `ios-auth/run.sh` | Codemagic, twice a week (`codemagic.yaml`, `ios-auth`) |
| [web-rtdb](web-rtdb) | `firebase` 11, 12, 13 (Auth and Realtime Database) in Chromium (Playwright) | production; Auth through `@aruma/web` | `web-rtdb/run.sh` | GitHub Actions, pull requests and main (`ci.yml`) |
| [web-storage](web-storage) | `firebase` 13 (Auth and Storage) built with Vite 8, webpack, esbuild, Rollup, Rspack and SvelteKit, in Chromium (Playwright) | production; Storage rewritten at build time by `@aruma/web`, and the run-time fallback | `web-storage/run.sh`; `web-storage/measure.sh` for Vite 5 to 8 and their dev servers, Next.js (webpack, Turbopack), Expo and Flutter web | GitHub Actions, pull requests and main (`ci.yml`: `web-storage`, `web-storage-measure`, `web-storage-flutter`) |
| [browser-matrix](browser-matrix) | Aruma's download replies, and `firebase` 13.1.0 `getBytes`/`getBlob`, in Chromium, Firefox and WebKit (Playwright) | pages on other origins and on the dashboard's own | `browser-matrix/run.sh [engine…]` | GitHub Actions, pull requests and main (`ci.yml`, `browser-matrix`, one job per engine) |
| [android-storage](android-storage) | `firebase-storage` and `firebase-auth`, instrumented tests, debug and release builds | production, through the Gradle plugin `dev.aruma.storage` and `aruma-redirect` | `android-storage/run.sh` | by hand |
| [ios-storage](ios-storage) | `FirebaseStorage` 12.19.3 from the Aruma mirror (SwiftPM) or 12.19.0 patched (CocoaPods), XCTest in a host app | production, `ArumaURL` in Info.plist; Auth through `ArumaRedirect` | `ios-storage/run.sh [--cocoapods]` | by hand (Codemagic later) |
| [android-rtdb](android-rtdb) | `firebase-database` and `firebase-auth` (BoM 35), instrumented tests | production; Auth through `aruma-redirect` | `android-rtdb/run.sh` | GitHub Actions emulator, weekly and by hand (`devices.yml`) |
| [ios-rtdb](ios-rtdb) | `FirebaseDatabase` and `FirebaseAuth` 12, XCTest in an empty host app | production; Auth through `ArumaRedirect` | `ios-rtdb/run.sh` | Codemagic, by hand (`codemagic.yaml`, `ios-rtdb`) |
| [android-firestore](android-firestore) | `firebase-firestore` and `firebase-auth` (BoM 35), instrumented tests | production host set in code; Auth through `aruma-redirect`; the server under the cloud.firestore ruleset of `firestore.rules`, released through the firebaserules API (`ARUMA_E2E_RULES=open` opts in to `documents.dev_open_rules`) | `android-firestore/run.sh` | GitHub Actions emulator, weekly and by hand (`devices.yml`) |

The Auth device suites cover the Auth P0 list ([docs/reference/compatibility.md](../../docs/reference/compatibility.md)):
sign-up, duplicate email, weak password, invalid email, sign-in, wrong password, anonymous,
forced token refresh with the ID token's `iss`/`aud`/`sub`, display name, password change after
reauthentication, password reset through the dev mailbox, delete and sign-out. They also check
that the redirect library is active and that Installations and Analytics are not linked in.

## Realtime Database (`*-rtdb`)

The server runs with the database enabled and the rules of [rtdb-rules.json](rtdb-rules.json),
deployed by the runner (`put_rtdb_rules`), never `dev_open_rules`. The clients know the database
only from the generated config (`databaseURL`, `firebase_url`, `DATABASE_URL`: Aruma's development
form `http://127.0.0.1:<port>/?ns=<project>-default-rtdb`); the code calls no emulator API. The
other party is a second SDK client where the platform allows one (a second browser on the web, a
second `FirebaseApp` on iOS, a process of its own on Android) and the database's REST API with an
admin credential (the CLI token) everywhere. genconfig's `-switch` takes the whole server down and
up again for the offline scenarios.

| # | Scenario | Checked |
|---|---|---|
| 1 | Sign-in | Aruma Auth (email and password, anonymous); the SDK's root reference is the generated URL |
| 2 | Rules | own data written and read; another user's write, read and listen `permission_denied`; signed out, denied |
| 3 | Two clients | writes reach the other's `child_added` and value listeners in order, with the server's `serverTimestamp` |
| 4 | Queries | `orderByChild` with `.indexOn` and `limitToLast(3)`; children entering and leaving the window; `limitToFirst` with `get()` |
| 5 | Transactions | concurrent `runTransaction` increments and the admin's ETag writes add up exactly |
| 6 | Presence | `onDisconnect` runs on `goOffline`, when the tab closes (web) and when the process is killed (Android) |
| 7 | Offline | with the server down a write is applied locally and lands when it is back; on the web also over long polling with WebSockets refused |
| 8 | Revocation | disabling the user cancels its listens within seconds (measured: 0.5 to 3.5 s) |
| 9 | Network guard | as in the Auth suites, and nothing for `*.firebaseio.com` or `*.firebasedatabase.app` |

Beyond the list: a persistent cache catching up after a reconnect (Android, iOS), a refreshed ID
token on an open connection, `onDisconnect` update and cancel, and a query without `.indexOn`
(web). The web runner ends with the long-poll trap of
[rtdb-lp-browser.sh](rtdb-lp-browser.sh) (`ARUMA_E2E_LP=off` skips it).

The guards: on the web the browser resolves no host name but localhost, and every request and
WebSocket is checked. On Android the iptables half covers the database's WebSockets (they are
sockets of the app's uid). On iOS the database's WebSockets do not go through URLSession, so
`ios-rtdb/run.sh` samples the host app's sockets with `lsof` every 200 ms and fails on one whose
far end is not loopback.

## In CI

`ci.yml` runs on pull requests and on pushes to main. Besides the Auth and RTDB suites above, the
v0.3 Storage suites have a job each:

| Job | What | Command |
|---|---|---|
| `storage-sdks` | `@google-cloud/storage` 8.4.0 and `cloud.google.com/go/storage` 1.62.1 (`TestStorageGCSWithTheAdminSDKs`), firebase-tools 15.34.0 (`TestStorageWithTheFirebaseCLI`) | `ARUMA_GCS_SDK=1 ARUMA_FIREBASE_CLI=1 go test -run '^(TestStorageGCSWithTheAdminSDKs\|TestStorageWithTheFirebaseCLI)$' ./test/acceptance/` |
| `browser-matrix` | Chromium, Firefox and WebKit, in parallel | `test/e2e/browser-matrix/run.sh <engine>` |
| `web-storage` | Vite 8, webpack, esbuild, Rollup, Rspack, the fallback, SvelteKit | `test/e2e/web-storage/run.sh` |
| `web-storage-measure` | Vite 5.4.21, 6.4.5, 7.3.7, 8.3.5 (build and dev server), Next.js 16.4.3 (webpack, Turbopack, dev), Expo 58.0.19 (web export in the browser, the Android bundle) | `test/e2e/web-storage/measure.sh vite5 vite6 vite7 vite8 next expo` |
| `web-storage-flutter` | Flutter 3.44.2 web with `firebase_storage` 14.0.1 | `test/e2e/web-storage/measure.sh flutter` |
| `storage-compat` | firebase-js-sdk 13.0.0's Storage suites (Node, Chromium, compat), firebase-tools 15.34.0's conformance tests, the captures | `test/compat/storage/run.sh` |

Every version is pinned: Node 24.21.0, Go from go.mod, the lockfiles, Playwright 1.64.1, the
versions and registry date in `measure.sh`, Flutter 3.44.2, and the upstream commits in
`test/compat/storage/run.sh`. The caches are keyed on them: npm, Go modules and builds, the
Playwright browsers, Flutter, and `.cache/upstream/storage` (the upstream sources and their
installs, on the pinned commits and the lockfiles).

**The network.** Each job installs everything first (`npm ci`, `go mod download`, `playwright
install`, `measure.sh --install`, `run.sh --prepare`), then runs its suite under
[test/ci/egress-guard.sh](../ci/egress-guard.sh): iptables rejects, logs and counts every packet a
process of the suite sends beyond loopback and the job's Postgres container, whatever the program.
Under it nothing is installed (the scripts see `ARUMA_EGRESS_GUARD`), npm runs from its cache
only, npx fetches nothing, and sudo cannot gain root (`no_new_privs`), so no root process escapes
the rules. The guard proves itself with probes before the suite, and the job's last step fails
when it was not turned on or counted a packet. What it does not count: DNS goes to the runner's
stub resolver on 127.0.0.53, which forwards it as its own user (the last step reports how many
packets went to port 53; the suites' guards refuse Google's names before any lookup), and an IPv6
connection without an IPv6 route fails before the rules (probed only when there is a route). The suites' own guards stay on
under it: netguard.cjs in every Node process that has one, the browsers' resolver rules and request
checks, the Go programs' loopback-only dialers, and in `TestStorageWithTheFirebaseCLI` netguard
alone (sandbox-exec is macOS only). Nothing in CI needs a secret or reaches Google.

**Manual, not on GitHub's Ubuntu runners:**

| Suite | Why | Command |
|---|---|---|
| android-auth, android-rtdb, android-firestore | an emulator run costs about as many minutes as the whole of `ci.yml`: weekly and by hand in `devices.yml` | `test/e2e/android-auth/run.sh`, `test/e2e/android-rtdb/run.sh` |
| android-storage | the same cost, and two builds (debug and release) with bundletool; not in `devices.yml` yet | `test/e2e/android-storage/run.sh` (a `google_apis` emulator for the iptables half) |
| ios-auth, ios-rtdb | Xcode and a simulator: macOS only; Codemagic (`codemagic.yaml`) | `test/e2e/ios-auth/run.sh`, `test/e2e/ios-rtdb/run.sh` |
| ios-storage | Xcode, the Aruma mirror of firebase-ios-sdk; for Codemagic later | `test/e2e/ios-storage/run.sh`, `test/e2e/ios-storage/run.sh --cocoapods` |

## Running locally

All runners need Go, python3 and the local Postgres (`ARUMA_TEST_DATABASE_URL`, default
`postgres://postgres@127.0.0.1:5432/postgres?sslmode=disable`). The device runners share
[server.sh](server.sh):

| Variable | Default | |
|---|---|---|
| `ARUMA_E2E_ADDR` | `127.0.0.1:0` | Server address; port 0 picks a free port. |
| `ARUMA_E2E_DB_PREFIX` | `aruma_e2e_` | Prefix of the throwaway database, dropped at the end. |
| `ARUMA_E2E_SWITCH_ADDR` | `127.0.0.1:0` | The `*-rtdb` suites: the server's off switch (genconfig `-switch`). |

The `*-rtdb` runners take the same arguments as their Auth siblings, e.g.
`test/e2e/android-rtdb/run.sh -e class com.aruma.e2e.RtdbE2ETest#queryWindowFollowsWrites` or
`test/e2e/ios-rtdb/run.sh -only-testing:RtdbE2ETests/RtdbE2ETests/testQueryWindowFollowsWrites`;
`web-rtdb/run.sh` passes its arguments to Playwright. `ARUMA_E2E_PAGE_PORT` moves the web suite's
Vite server (default 4181), and `ARUMA_DERIVED_DATA` the iOS suite's DerivedData.

**Android** (a JDK 17+, the Android SDK in `ANDROID_HOME`, one emulator or device):

```sh
test/e2e/android-auth/run.sh                                     # every test
test/e2e/android-auth/run.sh -e class com.aruma.e2e.AuthE2ETest#anonymous
```

- `ANDROID_SERIAL` picks the device when more than one is connected.
- `adb reverse` gives the device the server's own address, so `ArumaURL` is
  `http://127.0.0.1:<port>`. The manifest gets it through the `arumaUrl` Gradle property.
- The guard has two halves. In the process, a recording `ProxySelector` sees every connection the
  platform opens and fails the test on one that leaves loopback. The `URLStreamHandlerFactory` is
  left to `aruma-redirect`: a process can have only one. On the device, iptables rejects every
  packet of the app's uid that leaves loopback. The runner first proves this with a probe, then
  fails if the counter moved during the tests. This half needs `adb root`, which a `google_apis`
  image has and a Play Store image does not. `ARUMA_E2E_DEVICE_GUARD=off` skips it.
- The runner uninstalls the apps, removes the rules and the port mapping, and returns adbd to
  non-root if it found it that way.

**iOS** (Xcode 16 or later):

```sh
test/e2e/ios-auth/run.sh                                         # every test
test/e2e/ios-auth/run.sh -only-testing:AuthE2ETests/AuthE2ETests/testAnonymous
```

- `ARUMA_IOS_SIMULATOR` picks the simulator. The default is a booted iPhone, else the first
  available one.
- `ARUMA_XCODE_DIR` holds package checkouts and DerivedData (default `.cache/xcode`).
- The tests need a host app. Without one, FirebaseAuth cannot use the keychain on the simulator
  (error 17995).
- FirebaseAuth and ArumaRedirect are linked into the test bundle only. ArumaRedirect reads
  `ArumaURL` from the host's Info.plist, which the build setting `ARUMA_URL` fills.
- `LoopbackGuard` sits first in every URLSession. It refuses what is neither loopback nor taken by
  ArumaRedirect.
- The project is generated from `project.yml` by [XcodeGen](https://github.com/yonaskolb/XcodeGen)
  and committed. After editing `project.yml`, run `xcodegen generate` in `ios-auth` (or `ios-rtdb`)
  and commit both.

**Web:** see [web-auth/run.sh](web-auth/run.sh) (`ARUMA_E2E_MODE=emulator|production|production-build`).
