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

The device suites cover the Auth P0 list ([docs/compat/auth-p0-scenarios.md](../../docs/compat/auth-p0-scenarios.md)):
sign-up, duplicate email, weak password, invalid email, sign-in, wrong password, anonymous,
forced token refresh with the ID token's `iss`/`aud`/`sub`, display name, password change after
reauthentication, password reset through the dev mailbox, delete and sign-out. They also check
that the redirect library is active and that Installations and Analytics are not linked in.

## Running locally

All runners need Go, python3 and the local Postgres (`ARUMA_TEST_DATABASE_URL`, default
`postgres://postgres@127.0.0.1:5432/postgres?sslmode=disable`). The device runners share
[server.sh](server.sh):

| Variable | Default | |
|---|---|---|
| `ARUMA_E2E_ADDR` | `127.0.0.1:0` | Server address; port 0 picks a free port. |
| `ARUMA_E2E_DB_PREFIX` | `aruma_e2e_` | Prefix of the throwaway database, dropped at the end. |

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
  and committed. After editing `project.yml`, run `xcodegen generate` in `ios-auth` and commit both.

**Web:** see [web-auth/run.sh](web-auth/run.sh) (`ARUMA_E2E_MODE=emulator|production|production-build`).
