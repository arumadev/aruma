# How Aruma is tested

Aruma replaces Firebase for the official Firebase SDKs. So the main question is not "do our own tests pass" but "does the real SDK behave the same against Aruma as it does against Firebase". Most of the testing here is built around that question.

This page explains what runs, where it runs, and how to run the main suites yourself. It also says what is not verified yet.

## The layers

1. **Unit and acceptance tests** in Go. They start a real server on a real Postgres and call it over HTTP, WebSocket and gRPC.
2. **Official SDKs** talking to a real Aruma server: web, Node, Go, Android, iOS and Flutter. Where a platform needs an Aruma helper package, the suite uses it.
3. **Firebase's own test suites,** written for the Firebase emulators, run against Aruma. Every failure is classified.
4. **Recorded behaviour.** Answers recorded from real Firebase are replayed against Aruma.
5. **Concurrency checks.** Linearizability for the Document Database. Kill-and-restart durability for the Realtime Database.
6. **A network guard** that proves no request goes to Google.

## CI jobs

Every pull request and every push to `main` runs the jobs in `.github/workflows/ci.yml`. Every job runs on Linux with a Postgres 18 service.

| Job | What it checks |
|---|---|
| `go` | Formatting, `go vet` and the package dependency rules. Generated query and protobuf code is current. Then `make test` (all Go tests with the race detector, including the acceptance, isolation and recorded-behaviour replays), then the build. |
| `smoke` | The official SDKs against a real server: Firebase web, Admin Node, Admin Go, the Aruma admin-auth libraries, forged tokens against those libraries, Android, and the Admin SDKs for Realtime Database and Storage. |
| `documents-smoke` | The Document Database with its official clients: Admin Go, Admin Node over gRPC and REST, Firestore lite for Firebase 11, 12 and 13, and firebase-tools 15.34.0. |
| `web-auth` | The Firebase web SDK 11, 12 and 13 in a browser: Auth in emulator mode and in production mode through `@aruma/web`. Also the `@aruma/web` unit tests. |
| `web-rtdb` | The Firebase web SDK 11, 12 and 13 with the Realtime Database in production mode, under real security rules. |
| `web-documents` | The full Firestore web SDK over WebChannel and the lite SDK over REST, with rules and `@aruma/web`, in Chromium, Firefox and WebKit. |
| `web-storage` | The Storage web SDK built with Vite, webpack, esbuild, Rollup, Rspack and SvelteKit. |
| `web-storage-measure` | Storage in apps built with Vite 5 to 8, Next.js and Expo, each built, served and driven in a browser. |
| `web-storage-flutter` | Storage in a Flutter web app. |
| `storage-sdks` | `@google-cloud/storage`, `cloud.google.com/go/storage` and firebase-tools against Storage. |
| `storage-compat` | Firebase's own Storage tests, in Node and in Chromium, plus firebase-tools' Storage conformance tests. Every difference from the emulator is classified. |
| `browser-matrix` | Aruma's download replies in Chromium, Firefox and WebKit, from pages on other origins and on the dashboard's own. |
| `dashboard` | Lint, unit tests (including colour contrast), the static build, and end-to-end, visual and accessibility tests in Arabic (right to left) and English. |

Two more workflows run outside the main pipeline:

| Workflow | What it checks | When |
|---|---|---|
| `devices.yml` | Android emulator end-to-end suites for Auth, Realtime Database and the Document Database, with the official Firebase Android SDKs. | Every Monday, and by hand. An emulator run costs about as many minutes as the whole main pipeline. |
| `ios-storage.yml` | The Storage iOS suite (Swift and Flutter) in a simulator. | On changes to the paths it lists, and by hand. |

iOS suites run on Codemagic (`codemagic.yaml`), on macOS machines: an iOS smoke test every night, the Auth end-to-end suite twice a week, and the Realtime Database and Storage suites by hand.

## Unit and acceptance tests

`make test` runs `go test -race -count=1 ./...`. It needs a local Postgres, named by `ARUMA_TEST_DATABASE_URL`. Each test that needs a database creates its own and drops it afterwards.

The acceptance tests in `test/acceptance` start the real server and use it as a client would. They cover sign-up and sign-in, token verification, the Management API, security rules, Realtime Database over WebSocket, long polling and REST, Storage uploads and downloads, and tenant isolation between projects. They also run firebase-tools against Aruma (`deploy`, `export`, `delete`) when `ARUMA_FIREBASE_CLI=1` is set.

`make test-short` runs the tests that need no database.

## Official SDKs against Aruma

The end-to-end suites in `test/e2e` use the official Firebase SDKs, with the Aruma helper package where a platform needs one (see [SDK packages](sdks.md)). A runner starts a real server on a throwaway database. The SDK gets its settings from the config files Aruma generates, the same way a developer would. Apart from the web Auth run in emulator mode, the code under test calls no emulator API.

| Platform | SDKs | Where it runs |
|---|---|---|
| Web | `firebase` 11, 12 and 13 (Auth, Realtime Database, Firestore, Storage) in Chromium, Firefox and WebKit | Every pull request |
| Android | `firebase-auth`, `firebase-database`, `firebase-firestore` (BoM 35), `firebase-storage`, as instrumented tests | Auth, Realtime Database and Document Database: weekly, on an emulator. Storage: by hand |
| iOS | `FirebaseAuth` and `FirebaseDatabase` 12, `FirebaseStorage` 12.19.3, as XCTest | Codemagic (see above) and by hand |
| Flutter | Storage on iOS and on the web, and the smoke tests | Web: every pull request. iOS: Codemagic |
| Node and Go | Admin SDKs: Auth, Realtime Database, Storage, Document Database | Every pull request |

The scenarios are the ones an app uses: sign-up, sign-in, wrong password, anonymous users, token refresh, password reset, security rules that allow and deny, two clients seeing each other's writes in order, queries, transactions, presence (`onDisconnect`), offline writes that land when the server returns, and a disabled user losing their listeners within seconds.

### Firebase's own suites

Firebase publishes integration tests with its open-source SDKs. They were written for the Firebase emulators. Aruma runs them unmodified (`test/compat`), and a test that passes on the emulator and fails on Aruma is a bug unless it is explained.

- **Auth** (`test/compat/firebase-js/run.sh`): Firebase JS 13.0.0 in Node and in Chrome, and firebase-admin-node 14.5.0. This run is not part of CI, because it needs Chrome and clones the upstream repositories. In the last run, 180 tests passed, 98 failed and 83 were skipped. Every failing test is classified in [compatibility](reference/compatibility.md#auth). They fall into four groups: features Aruma does not offer yet (phone sign-in, email-link sign-in, multi-tenancy, importing users), tests that depend on the emulator's fake identity provider, ID tokens whose issuer is not `securetoken.google.com` (a deliberate choice that the Aruma admin libraries handle), and test environment limits.
- **Realtime Database** (`test/compat/firebase-js/run.sh rtdb`): the database-compat and database suites of firebase-js-sdk, run against the real Firebase emulator first as a baseline, then against Aruma. 326 compat tests (308 run) and 28 modern-SDK tests.
- **Storage** (`test/compat/storage/run.sh`, and the `storage-compat` CI job): the Firebase JS Storage integration tests in Node and in Chromium (13 tests each), the compat package (8) and firebase-tools' conformance tests (123).
- **Document Database:** the official clients are exercised through the `documents-smoke` and `web-documents` jobs. The official Android and iOS Firestore suites have not been run yet.

## Recorded behaviour

The question "what does Firebase really do here" is answered by recording the real thing. `test/oracle` holds answers recorded against real Firebase and against Google's official emulators, checked into the repository with the date, tool versions and a hash for every file. The recordings are replayed against Aruma without network access, in the normal Go test run.

| Service | What is recorded | Where it is replayed |
|---|---|---|
| Document Database | 65 files recorded on a real Firebase project: reads, writes, queries, transactions, Listen, rules, and the Firebase console's behaviour | `test/differential/documents`, in every `go test` |
| Security rules | Cases for the rules language shared by Firestore and Storage, decided by Google's Rules Test API, and the same cases on the official emulators | The rules engine's tests in `internal/platform/rules` |
| Storage | The wire exchanges of the Storage emulator with the web, Node and Go clients, recorded again against Aruma and compared | `test/compat/storage` |

Rules for reading the recordings:

- **Production wins.** Where Aruma differs from a production recording, that is a bug, unless the difference is listed in a parity-exceptions file with its reason and a test that proves it.
- **The emulator ranks second.** Where the emulator and production disagree, Aruma follows production.
- **Exceptions are checked.** The Document Database test fails if an exception names a test that does not exist. There are 24 exceptions: 16 where Aruma is stricter than Firebase on purpose, 7 that are provisional, and 1 where the emulator is wrong and Aruma follows production.
- **Storage has almost no production recordings.** `test/oracle/storage/production/` holds one probe of Google's answers for a project without a default bucket. Creating a default Storage bucket needs a paid plan, so the rest of Storage is checked against the emulator, Google's documentation and the SDK sources.
- **Where production was not recorded,** the design record marks the behaviour as a guess.

## Concurrency and durability

- **Linearizability (Document Database).** `test/linearizability/documents` runs concurrent clients against a live server and checks the recorded history with [Porcupine](https://github.com/anishathalye/porcupine). It also checks that commit order is strict and that commit times rise with it. A short run is part of `make test`. `make linearizability` runs it longer (60 seconds by default, set `LIN_DURATION`).
- **Contention.** `test/contention/documents` tests phantom reads under concurrent transactions.
- **Kill-and-restart durability (Realtime Database).** `tools/rtdb-chaos` kills the server with SIGKILL while clients write, restarts it, and checks that no acknowledged write is missing and none ran twice. The results are in [benchmarks](benchmarks.md#durability).
- **Fault injection and load for the Document Database** have not been run yet.

## The network guard: no request reaches Google

Aruma must work with no connection to Google. The test suites prove that.

- **In CI,** `test/ci/egress-guard.sh` puts the test commands under a firewall rule (iptables, matched by group). The commands may reach loopback and the job's Postgres, and nothing else. Every other packet is rejected and counted, whatever program sent it: Go, Node, a browser or a build tool. A last step, `Nothing left the machine`, fails the job if the count is not zero.
- **In each suite,** a second guard refuses early: Node suites load a guard (`netguard.cjs`) that blocks DNS, TCP, TLS and HTTP to anything but loopback. Browsers are started so they resolve no name except `localhost`. Android runs check the app's sockets with iptables. iOS runs sample the app's sockets and fail on a remote end that is not loopback.
- **A probe proves the guard bites.** Before the suites run, the script sends a packet it expects to be rejected.

Two things the CI guard does not count, and the suite guards cover: DNS queries to the runner's local resolver, and IPv6 on a runner with no IPv6 route, where nothing leaves the machine anyway.

`@aruma/web` adds a guard of its own at run time. With Auth on Aruma, it refuses requests to Google's Firestore host so that an Aruma ID token cannot reach Google.

## Run the main suites yourself

You need Go, PostgreSQL 18 and, for the SDK suites, Node 24. Point the tests at a Postgres you can create databases on:

```sh
export ARUMA_TEST_DATABASE_URL='postgres://postgres@127.0.0.1:5432/postgres?sslmode=disable'
```

| Goal | Command |
|---|---|
| Format, vet and dependency rules | `make lint` |
| All Go tests (unit, acceptance, isolation, recorded-behaviour replays) | `make test` |
| Only the tests that need no database | `make test-short` |
| The official SDKs against real config files | `make smoke` (all targets your machine supports) or `make smoke SMOKE="web admin-node"` |
| Document Database linearizability, longer | `make linearizability LIN_DURATION=5m` |
| One end-to-end suite | `test/e2e/web-auth/run.sh`, `test/e2e/web-rtdb/run.sh`, and so on. Each suite's folder has a `run.sh` |
| Firebase's Auth suites | `test/compat/firebase-js/run.sh` |
| Firebase's Realtime Database suites | `test/compat/firebase-js/run.sh rtdb --target aruma --mode production` |
| Firebase's Storage suites | `test/compat/storage/run.sh` |
| Durability under kill | `go run ./tools/rtdb-chaos -seed 101 -runs 6 -cycles 6` |

Tests that call firebase-tools or the Google Cloud Storage SDKs only run when you ask for them: set `ARUMA_FIREBASE_CLI=1` or `ARUMA_GCS_SDK=1`. The egress guard changes your firewall and is for CI runners only. On a laptop, the suites' own guards are enough.

The first run of a compat or end-to-end suite downloads packages and the upstream test sources. After that it uses a local cache.

## What is not verified yet

- **The Document Database is a release candidate.** The official Android and iOS Firestore suites, the Firebase JS 11 and 12 variants of its suites, black-box conformance cases for 19 requirements, the conformance suite against a live server in CI, and fault injection and load have not been run. See the [changelog](CHANGELOG.md#v040-rc1).
- **Android and iOS Storage suites** run by hand, not on a schedule.
- **Rules are not part of the load tests.** The performance figures in [benchmarks](benchmarks.md) used open rules for the Realtime Database.
- **Test counts per job** are not published here. Where a job's result is a count, the release notes for that version give it. The last Document Database release candidate recorded 21 of 21 web tests passing on each of Chromium, Firefox and WebKit, and 1,665 dashboard unit tests and 226 end-to-end tests passing.
