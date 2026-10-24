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
| [web-storage](web-storage) | `firebase` 13 (Auth and Storage) built with Vite 8, webpack, esbuild and SvelteKit, in Chromium (Playwright) | production; Storage rewritten at build time by `@aruma/web`, and the run-time fallback | `web-storage/run.sh`; `web-storage/measure.sh` for Vite 5 to 8 and their dev servers, Next.js (webpack, Turbopack), Expo and Flutter web | by hand |
| [android-rtdb](android-rtdb) | `firebase-database` and `firebase-auth` (BoM 35), instrumented tests | production; Auth through `aruma-redirect` | `android-rtdb/run.sh` | GitHub Actions emulator, weekly and by hand (`devices.yml`) |
| [ios-rtdb](ios-rtdb) | `FirebaseDatabase` and `FirebaseAuth` 12, XCTest in an empty host app | production; Auth through `ArumaRedirect` | `ios-rtdb/run.sh` | Codemagic, by hand (`codemagic.yaml`, `ios-rtdb`) |

The Auth device suites cover the Auth P0 list ([docs/compat/auth-p0-scenarios.md](../../docs/compat/auth-p0-scenarios.md)):
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
[test/experiments/rtdb-lp-browser.sh](../experiments/rtdb-lp-browser.sh) (`ARUMA_E2E_LP=off` skips it).

The guards: on the web the browser resolves no host name but localhost, and every request and
WebSocket is checked. On Android the iptables half covers the database's WebSockets (they are
sockets of the app's uid). On iOS the database's WebSockets do not go through URLSession, so
`ios-rtdb/run.sh` samples the host app's sockets with `lsof` every 200 ms and fails on one whose
far end is not loopback.

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
