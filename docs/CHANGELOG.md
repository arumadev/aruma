# Changelog

One section per release, newest first. Each release has an upgrade section when its steps are not obvious. Releases: [v0.5.0](#v050), [v0.4.0](#v040), [v0.4.0-rc.1](#v040-rc1), [v0.3.0](#v030), [v0.2.0](#v020), [v0.1.0](#v010).

## v0.5.0

Released 2026-11-25.

v0.5.0 is for production on one server, for small and medium projects. It adds no new service. It
fixes what a release-wide review, Firebase's own Firestore tests, crash and outage tests and two
example apps found. It also makes Storage backups consistent, deletes rows that outlived their use,
and lets you set Auth's limits, the server's timeouts and the database pool for a real server. The
Android and iOS end-to-end suites of the Document Database now pass.

Read [Upgrading from v0.4.0](#upgrading-from-v040) before you upgrade. It runs three migrations, and
the old process must stop before the new one starts.

### Security

- **Cloud Storage: a list without the `/` delimiter showed names in folders the caller may not
  list.** The SDKs always send `/`. A raw `/v0` list without it, or with another delimiter,
  returned every name below the listed folder: an anonymous list of `shared/` showed
  `shared/sales/hr/salary-review.txt` although a list of `shared/sales/hr/` was denied. File
  contents stayed protected. Now each deeper folder is decided by the rules on its own, and a name
  in a folder the caller may not list is left out. Rules that allow every depth list as before.
  Service accounts are not governed by rules and see every name. See
  [known differences](reference/known-differences.md#cloud-storage).
- **Dashboard: the page after sign-in could be another site.** `/login/?next=` refused `//` but not
  a tab or line break inside it, such as `/%09/evil.example`. Browsers drop those characters, so
  after a successful sign-in the browser went to the other site. Control characters are now
  refused, and the destination must be on the dashboard's own origin.
- **iOS: an Auth host written with a trailing dot went to Google.** `ArumaRedirect` did not
  recognise `identitytoolkit.googleapis.com.`, which is the same host as without the dot, so such a
  request reached Google with the app's credentials. It is now sent to Aruma. A Storage request to such a host still fails instead of being
  rewritten.
- **Flutter web: sign-up and sign-in went to Google.** A Flutter web app set up as the guides said
  loaded Google's `firebase-auth.js`, so it sent the user's email and password to
  `identitytoolkit.googleapis.com`: `aruma-web flutter-web` rewrote Storage and Firestore only. The
  tool now has an `auth` product, on by default, that rewrites the pinned `firebase-auth.js` to your
  server and loads it first. It stops the build if a Google Auth host is left in the file, or if
  `web/index.html` loads a rewritten product from Google itself. Leaving `auth` out prints a warning
  when the project uses `firebase_auth`. With `auth` on, the tool refuses a project whose Storage or
  Firestore would still go to Google with the Aruma ID token. See
  [Connecting a web app to Aruma Auth](guides/auth-web.md#flutter-web).
- **Go 1.27.4.** `govulncheck` found GO-2026-6859, a crash in `net/http`'s HTTP/1 server, reachable
  from `aruma` and `arumactl` built with Go 1.27.3. The binaries and the Docker image are built with
  Go 1.27.4. The Go SDK modules move to `golang.org/x/net` 0.62.0, and the dashboard to Next.js
  16.4.6; Next.js's advisories did not apply to the dashboard's static build. The Go SDK modules
  also move to OpenTelemetry 1.47.0 for GO-2026-6974, which Google's client libraries pull in; the
  server does not use it. After the moves, `govulncheck` finds no reachable advisory and
  `npm audit` finds none in the four npm packages.
- **API key restrictions, corrected in the docs.** [Security](security.md) said key restrictions
  apply to every request. They apply to Auth and to the Document Database's WebChannel handshake.
  The Document Database's REST and gRPC requests do not check the key, as production does not; the
  rules and the caller's identity decide access there.

The release-wide review and its scope are in [security](security.md#v05).

### Fixes

**Realtime Database**

- **A client that left still held the database.** Listens and reads queued by a client that then
  disconnected still ran on the database's single writer, each loading its node for nobody. Thirty
  such listens of a 180,000-row node held every other client's writes for 27 seconds. The writer now
  answers a closed connection's listens and reads without loading data, and still applies its
  writes. In the same run with the fix, writes waited 1.2 seconds, for the one listen already
  running (one run on a test server).
- **One process per database.** A second `aruma serve` on the same Postgres database also served the
  Realtime Database, so a write through one never reached the listeners on the other. At startup the
  process now takes an advisory lock (`rtdb.instance`), as the Document Database does. A process
  without it answers the Realtime Database with `UNAVAILABLE` and `/readyz` with 503. A process that
  loses it closes its connections and takes it again. See
  [configuration](reference/configuration.md#realtime-database-instance).

**Document Database (Firestore-compatible)**

- **Several writes to one document in a commit.** The rules judged them as an update when any of
  them had a precondition. So a batch or transaction that set a missing document and then updated
  it was denied whenever a rule read `resource.data`. The first write now says what the commit
  asserts of the document, as in the emulator. An update first is still an update.
- **Delete, then update with a precondition, in one commit** now fails with `INVALID_ARGUMENT`
  "Cannot delete then update an entity in the same request.", as the emulator answers and
  firebase-js-sdk's transaction tests expect. It failed with `NOT_FOUND`. A set after a delete still
  commits, as production's recording has it.
- **Equality on the document id beside another field's inequality.** A query with `==` or `in` on
  the document id, an inequality on another field and none on the document id now fails with
  `INVALID_ARGUMENT` "Equality on key is not allowed if there are other inequality fields and key
  does not appear in inequalities.", as the emulator does. It was served, or refused for a missing
  index.

**Auth**

- **Signed-out clients were left stuck after a month.** The refresh token of a revoked session or a
  deleted user is kept 30 days, so that the client hears `TOKEN_EXPIRED` or `USER_NOT_FOUND` and
  signs out. The 30 days were counted from the token's creation, so a session older than a month
  lost its token within the hour of the revocation, and the client was stuck on
  `INVALID_REFRESH_TOKEN`. They are now counted from the revocation or the deletion. A live refresh
  token is never deleted.
- **A purged project's Auth data stayed.** When a deleted project's 30 days ended, its users (with
  their password hashes), refresh tokens, audit rows, signing keys and settings, and its change log
  rows, were kept. They are now deleted with the rest of the project.
- **A flood of sign-ins slowed everyone.** With 100,000 entries in a rate-limit table, each new
  entry scanned the whole table twice under one lock, about 17 ms each time in a unit test on a busy
  machine. The sign-in delay is kept per email typed, so many distinct emails slowed every sign-in.
  Adding and evicting an entry now takes constant time.

**Every service**

- **503 instead of 500 or 400 during database trouble.** With Postgres down or a connection cut,
  Auth, the console and the Management API answered 500 `INTERNAL`, and an API key lookup answered
  400 `API key not valid`, which the SDKs report as a bad key and do not retry. The Document Database
  answered `INTERNAL` when Postgres ended a connection, and Storage 500 when that happened inside a
  transaction. All of them now answer 503 `UNAVAILABLE`, which clients retry. A request whose own
  client went away is not counted as an outage. In 10 Postgres outages (stops, pauses and cut
  connections) with clients writing, the Document Database answered `INTERNAL` 15 times before the
  fix, and never in 10 more after it.
- **Readiness waits for the Document Database.** After a restart, `/readyz` answered 200 about 2
  seconds in, while the Document Database still refused for about 30 seconds, until its lock was
  free. `/readyz` now answers 503 (`documents-instance`) until it is served. The two instance locks
  are taken at the same time, so a second process opens its port after 30 seconds instead of 60.
- **Back within seconds of Postgres.** The Realtime Database and the Document Database retried their
  instance lock with a backoff of up to 30 seconds, so after Postgres came back they stayed down up
  to 30 seconds longer than the other services. The backoff now stops at 5 seconds. After four
  15-second Postgres stops, both served again 2.0 to 2.8 seconds after Postgres started, against 16
  seconds before.

**Dashboard**

- A long app nickname no longer overflows its card on the project overview (it made a 360-pixel
  page 1,576 pixels wide).
- The Document Database's root breadcrumb no longer breaks an accessibility rule (`aria-label` on an
  element with no role), the only violation the accessibility check found across the dashboard.

**SDK packages**

- `@aruma/admin-auth`: errors carry firebase-admin's own messages, such as "The email address is
  already in use by another account.", not "Aruma Auth answered EMAIL_EXISTS.". The codes were
  already the same. A detail from the server still comes first.
- `adminauth` (Go): a verification no longer waits past its own context while another call loads the
  signing keys.
- `@aruma/web`: the web guide now says that on mobile browsers and in Safari `getAuth()` loads a
  script from `apis.google.com` whatever the sign-in method, and that `authStateReady` waits for it
  where that host is blocked. It shows `initializeAuth` without the popup helper for apps that do
  not need it. See [the guide](guides/auth-web.md#mobile-browsers-and-safari-the-apisgooglecom-script).

### Outage handling

- **A frozen database no longer hangs every request.** A Postgres that is paused, stalled on its
  disk or cut off by a partition that keeps TCP open answers nothing. Database calls had no
  deadline, so in a 40-second pause Auth, the Document Database, the Realtime Database and Storage
  gave no answer at all, and the pool filled with waiting requests. The server now bounds the wait
  for a connection and each statement by the new `database_timeout` (`ARUMA_DATABASE_TIMEOUT`, 15
  seconds by default), and answers 503 `UNAVAILABLE` past it. In the same pause with the fix, every
  service answered 503 after 15.0 seconds and served again half a second after Postgres resumed.
  Streams stay long-lived; only the database calls inside them are bounded. `migrate`, `backup` and
  `restore` are not bounded, nor is the server's own background work (housekeeping, the purge of a
  deleted project, garbage collection, index builds), whose statements grow with a project. See
  [database timeout](reference/configuration.md#database-timeout).
- **Two more 500s are 503 now.** A pooled connection that ended in the middle of an answer gave 500
  "unexpected EOF", and a connection that timed out while opening gave 500 on Storage and the
  Document Database and 499 `CANCELLED` on Auth.
- **A request cut by its client is 400, not 500.** A client that announced a body and went away in
  the middle of it got 500 from Auth. Such a request ends with the same error as a lost database
  connection, so it is now marked where it is read and answered 400 `INVALID_ARGUMENT`, and never
  counted as an outage.
- **Realtime Database writes survive an outage.** A write, listen or read that failed because the
  database was out of reach was answered `unavailable`, which the SDKs do not retry: the app was
  told the write failed and the value was reverted. The server now closes the connection instead,
  and the SDK sends the request again when it reconnects, as after a restart. With the official JS
  SDK writing every 50 ms through 10 outages, 37 of 3,380 writes failed before the fix and none of
  3,378 after it; after a 3-minute stop of Postgres the client was connected again 5 seconds after
  Postgres was back (37 seconds before the fix, one run each). A failure that is not an outage is
  still answered, so a write that can never succeed is not sent again forever. An `increment` whose
  commit was cut by the outage may be applied twice: see
  [known differences](reference/known-differences.md#realtime-database).
- **Web users stay signed in through an outage.** Auth answered its SDK endpoints with 503 as an
  Identity Toolkit error in JSON. The web SDK signs the user out at the next app start on any error
  but a network failure, so a page reloaded during an outage came back signed out. Those endpoints
  now answer 503 with a plain-text body and `Retry-After: 5`, which the SDK reports as
  `auth/network-request-failed`: it keeps the user and retries the token refresh. Tested in Chromium
  with Firebase JS 11, 12 and 13. The Admin API keeps its JSON `UNAVAILABLE`. Android and iOS were
  not run against the new answer; by their source they sign a user out only when the server says the
  user or the token is gone. See [known differences](reference/known-differences.md#authentication).
- **Realtime Database REST requests do not count as connections.** At
  `rtdb.limits.connections_per_project`, every REST read and write was refused with 429, so a
  backend could not write to a database its own clients filled with listeners. Only WebSockets and
  long polls count now, as in Firebase. The limits are listed in
  [Realtime Database limits](reference/configuration.md#realtime-database-limits).

### Operations

- **Storage backups, from one snapshot.** The documented procedure took the database dump and then
  copied the Storage directory. Aruma deletes the file of an overwritten or deleted object a minute
  later, and a finished upload moves its file, so a copy that ended after the dump could miss files
  the dump names. The restored objects then answered 503. `aruma backup --storage-out FILE` now
  writes the dump and an archive of exactly the files that dump names, from one snapshot, while the
  server runs. Storage holds its file deletions while a backup runs. In a test that backs up while
  clients write, then restores, the old procedure left objects without their file in 2 of 4 runs;
  `--storage-out` in 0 of 6. The [operations guide](guides/operations.md#docker) has the backup and
  restore steps for Docker and for the binary.
- **`aruma restore` refuses to run while a server is connected** to the database.
- **`backup`, `restore` and `migrate` read `--config`**, and `backup` and `restore` accept a database
  URL with `pool_max_conns`, as the guide recommends. Before, they read only the environment and
  refused that URL.
- **`aruma migrate up` can run twice at once.** Two runs on an empty database raced and one failed
  (2 of 6 concurrent runs in a test). Migrate now holds an advisory lock: a second run waits, finds
  nothing to do and succeeds.
- **Housekeeping deletes what outlived its use**, every hour: the sign-in audit (`auth.audit`) and
  the administrative audit log (`control.audit_log`) after 90 days, the change log
  (`events.changes`) after 30, expired console sessions and CLI tokens, and the refresh tokens above.
  The new `retention.auth_audit_days`, `retention.control_audit_days` and `retention.events_days`
  set the three periods, and `0` keeps a table forever. Rows go in batches, at most 50,000 per table
  per hour. The audit log still refuses any change, and any deletion of a row younger than seven
  days. Each pass logs a `housekeeping` line and counts in `aruma_housekeeping_purged_rows_total`.
  See [retention](reference/configuration.md#retention).
- **Auth limits and server timeouts are settings.** `auth.limits.*` set the per-address limits
  (300 requests a minute with a burst of 100; sign-ups 100, then 2 a minute), and `server.*_timeout`
  the public listener's timeouts (10 seconds for headers, 60 to read and to write a request, 120
  idle). The defaults are unchanged. See
  [Auth rate limits](reference/configuration.md#auth-rate-limits) and
  [server timeouts](reference/configuration.md#server-timeouts).
- **A larger database pool by default.** It was pgx's max(4, CPUs) for all services and projects
  together. It is now max(16, 4 × CPUs), at most 64, opened on demand; `pool_max_conns` in the URL
  still decides. In one run with one busy project among four on 4 CPUs, a pool of 32 gave the busy
  project 43% more Document Database writes and halved its effect on the others. The
  server logs the size at startup, and warns when Postgres's `max_connections` is too low for it.
- **Docker:** where Docker's own open-file limit is below 65536, lower `nofile` in `compose.yaml`; the
  [operations guide](guides/operations.md#docker) says how.

### For developers

- **Two example apps:** [examples/web](../examples/web) (React, TypeScript and the `firebase`
  package) and [examples/flutter](../examples/flutter) (FlutterFire), the same Team Board app on one
  data model and one set of rules. They use Auth, the Document Database, the Realtime Database and
  Storage. CI drives each against a real server as two users and a guest. The web example's run
  passed 18 of 18 times on Aruma and 9 of 9 on the Firebase emulators with no code change; the
  Flutter example's web build passed 5 of 5, and 3 of 3 after it moved to the `auth` product; its
  19 widget tests pass. See
  [examples](../examples/README.md).
- **An iOS suite for the Document Database:** `test/e2e/ios-firestore` runs the official
  FirebaseFirestore and FirebaseAuth 12 against a real server under released rules: the Android
  suite's scenarios, and rules with `get()` and `exists()`, two apps seeing each other's writes,
  queries and cursors, collection groups, aggregations, concurrent transactions, an offline write
  that lands when the server returns, a persistent cache, and a disabled user losing its listeners.
  It runs on GitHub's macOS runners (`ios-firestore.yml`) and on Codemagic by hand.
- **Firebase's own Firestore tests in CI:** `test/compat/documents/run.sh` and the `documents-compat`
  job run firebase-js-sdk 13.0.0's Firestore integration, persistence and lite suites in Node against
  a fresh server, and fail on any difference from the Firestore emulator 1.22.0 that is not
  classified. Aruma passes 544 of 919, 1,169 of 1,907 and 197 of 444, the emulator 545, 1,171 and
  198. The four differences are named databases (three tests) and one aggregation that production
  also refuses without an index.
- **`test/ops`** runs the `aruma` binary as an operator does: backup and restore while clients write,
  compared byte for byte; restore under another master key, over a running server and without the
  Storage archive; the upgrade from a populated v0.4.0; and a second process beside a running one.
  The `ops` CI job runs it (15 tests).
- **Crash, load and soak tools:** `tools/docs-chaos` and `tools/storage-chaos` kill the server while
  clients write and check every acknowledged change; `tools/dsbench` measures the Document Database
  and Storage; `tools/soak` runs all four services for as long as you ask and gives a pass or fail.
  Their numbers are in [benchmarks](benchmarks.md#document-database).
- **Access-control tests:** twenty acceptance tests walk every console and Management API route
  that names a project, across two projects, put forged and foreign tokens to every data service, cut access on disable,
  delete, revoke and password change (open WebSockets included), try 35 spellings of a protected
  path on each service, and look for issued credentials in a dump of every schema and in the log.
- **A supply-chain workflow:** `supply-chain.yml` runs `govulncheck` on the built binaries and
  `npm audit` on the npm packages and the dashboard, on pull requests, on `main` and every Monday.

### Evidence

Measured on the code of this release:

| Check | How | Result |
|---|---|---|
| Firebase's Firestore tests (firebase-js-sdk 13.0.0, Node, gRPC) | `test/compat/documents/run.sh` | 544/919, 1,169/1,907 and 197/444 (emulator 545, 1,171 and 198); every difference classified |
| Firebase's Realtime Database tests | `test/compat/firebase-js/run.sh rtdb`, run again | 332 passed in production mode, 336 in emulator mode, no regression |
| Android end-to-end: Auth, Realtime Database, Document Database (official SDKs, BoM 35) | `devices.yml`, on GitHub's Android emulator, 23 November 2026 | passed |
| iOS end-to-end: Document Database (FirebaseFirestore 12) | `ios-firestore.yml`, in a simulator on GitHub's macOS runners | 17 of 17 in three runs; an earlier run failed on one test's timing, and the test was fixed |
| Crash durability | `tools/docs-chaos`, `tools/storage-chaos`, `tools/rtdb-chaos` | No acknowledged change lost or applied twice: Document Database 100 SIGKILLs in 10 runs, Storage 70 in 7, Realtime Database 108 in 18 (151,415 acknowledged writes) |
| Backup and restore while clients write | `test/ops` | 6 of 6 runs with `--storage-out`, no object without its file |
| Postgres outages | 20 outages (stop, pause, cut connections), Go clients and Firebase JS 13 on every service | No acknowledged write lost. See [Outage handling](#outage-handling) |
| Soak | `tools/soak`, 90 minutes, all four services | 361,759 operations; memory, goroutines, open files and connections flat |
| Load | `tools/dsbench`, one run per scenario on a shared 4-vCPU VM | [Benchmarks](benchmarks.md#document-database) |
| Flutter web Auth and Storage | `test/e2e/web-storage/measure.sh flutter` | 6 of 6 runs; no request to Google's Auth hosts |
| Supply chain | `govulncheck` (binary mode), `npm audit` | No reachable advisory; no npm advisory |

### Upgrading from v0.4.0

1. **Back up with the server stopped.** v0.4.0's `aruma backup` has no `--storage-out`. Stop the
   server, run `aruma backup --out aruma.dump`, and copy the Storage `data_dir` (with Docker, the
   `storage` volume) while it is still stopped. From v0.5.0 on, back up with `--storage-out` while the server runs.
2. **Stop the v0.4.0 process before you start v0.5.0.** v0.4.0 takes no Realtime Database lock, so
   with both running, both serve the Realtime Database, and listeners on one miss the writes made on
   the other. `docker compose up -d --build` stops the old container before it starts the new one.
3. **Run `aruma migrate up` with the v0.5.0 binary.** Three migrations: Auth 00004 (`ended_at` on
   refresh tokens, set by triggers, and indexes for housekeeping), control 00004 (indexes, and an
   audit log that allows deleting rows older than seven days) and events 00002 (an index). The
   Docker image runs them before the server starts. v0.5.0 refuses to start on the old schema, and
   v0.4.0 refuses the new one: to go back, restore the backup.
4. **Decide on the audit tables before the first start.** Housekeeping now deletes
   `control.audit_log` and `auth.audit` rows older than 90 days, and `events.changes` rows older than
   30, starting at the first start and at most 50,000 rows per table per hour. To keep a table, set
   its `retention.*` key to `0` (for the audit log, `retention.control_audit_days: 0`), or export
   it first.
5. **Check Postgres's `max_connections`.** The pool now defaults to max(16, 4 × CPUs), at most 64,
   per process. Postgres must accept it and two more connections, and twice that while a second
   process starts or `aruma migrate` runs. The server warns at startup when it cannot. To keep the
   old size, put `pool_max_conns` in `database_url`.
6. **A second process now waits.** Another process on the same database answers the Realtime
   Database and the Document Database with `UNAVAILABLE`, and `/readyz` with 503, until the first
   stops. In Kubernetes use `strategy: Recreate`.
7. **Storage rules that differ by folder:** a list without the `/` delimiter no longer shows names in
   folders the caller may not list. The SDKs always send `/` and are not affected.
8. **The Document Database refuses two requests with `INVALID_ARGUMENT`:** a query with `==` or `in`
   on the document id beside an inequality on another field (it was served, or refused for an
   index), and a commit that deletes a document and then updates it with a precondition (it failed
   with `NOT_FOUND`).
9. **Flutter web:** run `npx aruma-web flutter-web` again. It now includes `auth` by default. List
   every product the app uses (`--products auth,storage,firestore`): with `auth` on, the tool fails if
   the app uses Storage or Firestore and that product is not listed. For an app whose Auth stays at
   Google, pass a list without `auth`.
10. **`@aruma/admin-auth`:** code that matches `error.message` sees firebase-admin's messages. Match
    `error.code` instead.
11. **A database call that takes longer than 15 seconds now fails** with 503 `UNAVAILABLE` instead
    of finishing slowly. If your Postgres is slow under load and requests begin to fail with 503,
    raise `database_timeout`.
12. **Auth during a database outage:** the SDK endpoints answer 503 with a plain-text body, not
    JSON. Code of your own that calls those endpoints and parses the error body must accept it.
13. Building from source needs Go 1.27.4 (the `toolchain` line in `go.mod`).

### Not verified yet

- **A 72-hour soak.** The soak ran 90 minutes. Memory, goroutines, open files and connections were
  flat, but the database grew all the way through, partly by design. Whether that growth stops
  needs a run longer than every retention window.
- **Production Firebase beyond the recordings.** Behaviour is compared with the recorded corpus,
  the emulators and Firebase's own tests. Nothing new was recorded against production for this
  release.
- **The Flutter example on Android and iOS.** Its Android build did not complete and its iOS build
  was not tried. Only its web build is tested.
- **Firefox and WebKit for the example apps.** They run in Chromium only.
- **Popup and redirect, email-link and phone sign-in on Flutter web.** The Flutter web test covers
  email and password, anonymous sign-in, token refresh, profile and delete.
- **The Android, iOS and Flutter helper packages in the security review.** The review read the web
  package's host handling; `aruma-redirect`, `ArumaRedirect`, the Gradle plugin and `aruma_storage`
  were not reviewed.
- **More than one server, and failover.** Aruma runs on one server. Several servers on one
  database, and a Postgres failover to a replica, were not tested.
- **Remaining Document Database checks:** Firebase's own Android and iOS Firestore test suites, the
  Firebase JS 11 and 12 variants of the web suites, black-box conformance cases for 19 requirements
  and the conformance suite against a live server in CI. The Android and iOS end-to-end suites
  connect in cleartext on loopback, so TLS from a phone is not tested, and Flutter on Android and
  iOS is not tested separately.

## v0.4.0

Released 2026-10-29.

The Document Database (Firestore-compatible) from [v0.4.0-rc.1](#v040-rc1), released as v0.4.0,
and the first release you can install without building from source: archives for Linux, macOS and
Windows, and a Docker setup with PostgreSQL 18.

v0.4.0 is released before the checks the release candidate listed under
[Before v0.4.0](#before-v040) have run: the official Android and iOS suites, the remaining Firebase
JS variants, the black-box conformance cases and the conformance suite against a live server in CI,
fault injection and load, and the release-wide security review. They continue on this code, and
whatever they find is fixed in a v0.4.x release. Until then, the list under
[Not verified yet in this candidate](#not-verified-yet-in-this-candidate) still applies.

### Install

- **Docker:** `compose.yaml` runs Aruma and PostgreSQL 18, with the Realtime Database, Storage and
  the Document Database on. The image is built on your machine from the `Dockerfile`; no image is
  published to a registry yet. It runs as a non-root user, applies migrations before the server
  starts, and has the PostgreSQL 18 client tools for `aruma backup` and `aruma restore`. See
  [getting started](getting-started.md).
- **Archives:** `aruma_0.4.0_<os>_<arch>` for Linux and macOS (`.tar.gz`) and Windows (`.zip`), on
  amd64 and arm64, with `aruma`, `arumactl` and the dashboard inside the binary. `checksums.txt` has
  their SHA-256 sums, and each archive has a GitHub build attestation (`gh attestation verify`). The
  binaries are not signed by Apple or Microsoft. See [operations](guides/operations.md#prebuilt-binaries).
- **Windows:** the server runs, but Cloud Storage does not start on Windows: it needs file locks and
  disk calls that Aruma implements on Linux and macOS only. Use Docker or WSL for Storage.
- `make dist` builds the same archives locally, and a pushed `v*` tag builds and publishes them.

### Security

- The server and both Admin Auth libraries accept a token only in canonical base64url, so one token has one spelling.
- The Admin Auth libraries refuse a Google address written with a trailing dot (for example `accounts.google.com.`).

## v0.4.0-rc.1

Released 2026-10-28. Release candidate.

A Firestore-compatible database for the official web SDKs (full over WebChannel, lite over REST),
the mobile SDKs' gRPC transport, the Admin SDKs for Node and Go, and firebase-tools, with security
rules, a dashboard, and a guide for moving an app over. It is a **release candidate**: the code is
the code planned for v0.4.0, and the verification a final release needs (the official Android and
iOS suites, among others) comes after it. What is not verified yet is listed below; read it before
you rely on this version.

The service is called Document Database in Aruma, with "Firestore-compatible" under it; the
protocols keep Google's names (`google.firestore.v1`). It is off by default.

Evidence is in [compatibility](reference/compatibility.md#firestore), the differences in [known differences](reference/known-differences.md#document-database-firestore-compatible), and how to move an app in [the migration guide](guides/firestore-migration.md).

### What ships

**Storage and ordering**, on the Postgres Aruma already runs on:

- Documents with every value type, including vectors and the BSON shapes production accepts, and a
  per-database sequencer that orders every write, so `read_time` and Listen come from one order and
  not from the clock.
- A change log that keeps each document before and after a write, for triggers later.
- One server per database, enforced by an advisory lock on Postgres.

**Transactions, queries and indexes:**

- `Get`, `BatchGet`, `List` (with `show_missing`), `Create`, `Update`, `Delete`, `ListCollectionIds`,
  `Commit`, `BatchWrite`, `BeginTransaction`, `Rollback`, `RunQuery`, `RunAggregationQuery` and
  `PartitionQuery` on `(default)`.
- Both transaction models and read-only transactions with `read_time`; every query operator, OR,
  collection-group queries, `limitToLast`, `count`, `sum` and `avg`; field transforms.
- Composite indexes with production's rule, in three modes: `enforce` (production's error and
  console link), `warn` and `off` (`documents.index_enforcement`). Single-field exemptions and
  TTL policies.
- TTL deletes expired documents in the background through the ordinary commit path, so listeners and
  the change log see the deletion. It runs hourly by default; Google itself promises "typically
  within 24 hours".

**Listen and Write**, on the one public port:

- gRPC (HTTP/2, with or without TLS) for mobile and the Admin SDKs, WebChannel for the full web SDK,
  and REST for the lite SDK, with production's credentialed CORS.
- Listen with its full contract: resume, resets, existence filters by count, and `namedQuery`
  bundles. Bounded streams (per project, per address, targets, snapshot size, idle time) with no
  silent drops: every limit ends in a code the client sees and a counter.

**Security rules for Firestore** (the engine shipped in v0.3):

- `service cloud.firestore`, with `get()`, `exists()` and `getAfter()` (10 and 20 lookups) and the
  `list` model measured against production. `firestore.get()` and `firestore.exists()` in Storage
  rules now read the Document Database instead of denying.
- A project with no published rules is closed. Rules are published with
  `firebase deploy --only firestore:rules`, the firebaserules API, or the dashboard.

**Admin API and Admin SDKs:** service-account JWTs as the Node and Go Admin SDKs send
them, and the Admin API firebase-tools calls: databases, indexes, fields with `ttlConfig` and
`vectorConfig`, operations. Proved with firebase-tools 15.34.0 under the network guard
(`test/acceptance/documents_cli_test.go`): `deploy --only firestore:indexes` and `firestore:rules`,
`firestore:indexes` (export), `firestore:databases:list` and `get`, `firestore:delete -r`, and TTL
deleting an expired document. Rules, indexes, exemptions and TTL export and deploy back without
loss.

**Web guard** (security finding SR3-10, the Firestore part): with Auth on Aruma, `@aruma/web` refuses requests to Google's
Firestore host, so the user's Aruma ID token cannot reach Google through Firestore. Firestore itself
is routed to Aruma at build time (`firestore: true` for Vite, `arumaFirestore` for other bundlers,
`withArumaFirestore` for Next.js and Metro, `aruma-web flutter-web --products firestore`), or at run
time with `arumaFirestoreSettings`. See Behaviour changes.

**Dashboard:** a data browser in three panels (collections, documents, typed fields) with
add, edit, delete and recursive delete as the Firebase console does them; the rules editor with
version history, errors in line and a Rules Playground; composite indexes, single-field exemptions
and TTL policies; and usage by hour: documents read, written and deleted as Firestore bills them,
listener and connection peaks, and rules decisions. Every write from the dashboard is audited. In
Arabic and English.

**Moving an app** ([guide](guides/firestore-migration.md)): pointing each SDK at Aruma, importing
indexes from a live project (including those created from console links), rules, TTL, and data.

**Evidence**, from the last green CI run on main (40678191810, `6b694da`):

| Suite | Result |
|---|---|
| Web Document Database e2e: full SDK over WebChannel, lite SDK over REST, rules, `@aruma/web` (Firebase 13) | 21/21 on each of Chromium, Firefox and WebKit |
| firebase-tools 15.34.0 against the Document Database, under the network guard | `TestDocumentsWithTheFirebaseCLI` passes |
| Document Database SDK smoke: Admin Go, Admin Node over gRPC and REST, JS lite | job passes; no count taken |
| Dashboard unit tests; e2e, visual and accessibility tests in Arabic (RTL) and English | 1665 passed; 226 passed |
| Go: lint, unit, acceptance and isolation tests, with the oracle replays | job passes; no count taken |
| The suites of earlier releases (Auth, Realtime Database, Storage, browser matrix) | all green in the same run |

### The production oracle

Behaviour is judged against Google's production, not against the emulator or a reading of the
docs. The corpus in `test/oracle/documents/corpus/` (65 files) was recorded on a real Firebase
project, with the Firebase console watched in a browser for the dashboard, and the recordings are
replayed against Aruma in every `go test` (`test/differential/documents`). A difference is fixed or
listed in `test/oracle/documents/parity-exceptions.json` with its reason, and the oracle test fails
when a listed exception names a test that does not exist. There are 24 exceptions: 16 restrictions
(stricter than Firebase on purpose), 7 provisional (a limit or behaviour to revisit) and 1 where the
emulator is wrong and production is followed. Where production was not recorded, the behaviour is marked as a guess.

### Changes

- **Auth mail and account pages default to English; Arabic stays available as a template language.**
- **`@aruma/web` now refuses Google's Firestore host while Auth is on Aruma.** In a browser,
  `fetch` and `XMLHttpRequest` to `firestore.googleapis.com` get a `403` with an explanation, and
  the Vite plugin fails a build that includes `@firebase/firestore` without `firestore: true`. In
  Node, DNS, TCP, TLS, HTTP/2 and proxy `CONNECT` to that host fail with the error code
  `ARUMA_FIRESTORE_BLOCKED` before any byte is written, and the Firestore SDK reports
  `unavailable`. An app already on Aruma Auth that also uses Firestore at Google must route
  Firestore to Aruma or stop using it.
- **Callable Functions still go to Google** and carry the Aruma ID token (security finding SR3-10, the Functions
  part). An app on Aruma Auth should not use them.

### Not verified yet in this candidate

Final v0.4.0 depends on these.

- **The official Android and iOS Firestore suites,** and the mobile host settings. The transport was
  tried on Android 27.0.1 and iOS 12.19.3, not measured by the full suites. **iOS needs
  a public test domain with a certificate from a public authority:** it trusts only its bundled
  roots, so a self-signed or private-CA certificate cannot connect. Flutter uses the
  same native libraries.
- **Firebase JS 11 and 12 variants** of the Document Database suites (the web e2e runs Firebase 13).
- **Black-box conformance cases for 19 requirements.** They are covered by unit tests
  and by the replays of the recordings, but not yet by cases run against a running server.
- **The conformance suite against a live server in CI.**
- **Fault injection and load.**
- **A release-wide security review** ([security](security.md#reviews) has one for each earlier release). Each part
  was reviewed as it was built; this version has no review of its own yet.

### Not supported

Each is refused with an explicit error, never ignored.

- **Databases other than `(default)`:** answered as production answers a database that does not
  exist (`NOT_FOUND`, with the text of each surface); creating, changing or deleting one is
  `UNIMPLEMENTED`. Named databases and named rules are planned for v0.4.x.
- **Import and export through GCS,** and managed backups and their schedule. Use Postgres backups.
- **`findNearest` and vector search:** vector values and indexes are stored; `findNearest` is
  answered with production's error without a matching vector index and `UNIMPLEMENTED` with one.
- **Query Explain:** `explain_options` is `UNIMPLEMENTED`.
- **Bloom filters in Listen:** the existence filter carries the count alone.
- **Point-in-time recovery, restore and clone;** `read_time` reads reach back as far as the version
  window the operator sets.
- **The Enterprise edition and Pipelines.**
- **`firestore:locations` and `firestore:bulkdelete`,** and editing or deleting databases.
- **Firestore triggers and Extensions,** which need a Functions environment. The change log keeps
  what they will need.
- **A tool to move data from Firebase.** Indexes can be exported from a live project with
  `firebase firestore:indexes`; the migration guide covers the rest.

### Known differences

Every difference from production is in `test/oracle/documents/parity-exceptions.json` with its
reason and its test. The stream limits, which are stricter than Firebase on purpose and adjustable
under `documents.limits`, are in [compatibility](reference/compatibility.md#firestore). Examples: a query inside a
pessimistic transaction locks the whole collection it reads; transactions open at a restart are
lost; a Listen resumed beyond the version window is answered with a reset; REST answers are compact
JSON; WebChannel answers carry no `x-client-wire-protocol` header. Usage counters follow Firestore's
billing rules from its documentation, not from a recording.

### Upgrading from v0.3

1. Back up the database (`aruma backup`), stop the server, run `aruma migrate up` with the new
   binary (the documents schema, migrations 00001 to 00004: tables, index builds, the instance lock
   and the hourly usage), and start it. The server refuses to start on an old schema. Migrating
   does not turn the service on.
2. Auth, the Realtime Database and Storage need no change.
3. The Document Database is off by default. Turn it on with `documents.enabled: true`
   (`ARUMA_DOCUMENTS_ENABLED`). Every `documents.*` and `documents.limits` key is in
   [configuration](reference/configuration.md). Two are for local development only, and the server refuses to start with
   them unless `public_url` and `listen` are loopback addresses: `documents.dev_open_rules` and
   `documents.dev_owner_token`.
4. Every project starts with closed Firestore rules. Deploy yours with
   `firebase deploy --only firestore:rules` or from the dashboard.
5. Serve gRPC clients through a TLS proxy with a certificate from a public authority, talking h2c to
   Aruma ([guide](guides/firestore-migration.md)). iOS will not connect otherwise.
6. SDK changes for apps already on Aruma:
   - **Web:** see Behaviour changes. Add `firestore: true` (or `arumaFirestoreSettings`) to any app
     that uses Firestore.
7. Building from source needs Go 1.27.3 (the `toolchain` line in `go.mod`).

### Before v0.4.0

The remaining verification runs on this code: the official Android and iOS suites, the
remaining Firebase JS variants, the black-box conformance cases and the conformance suite against a
live server in CI, fault injection and load, and the release-wide security review. v0.4.0 follows on
the same code once they pass. Whatever they find is fixed in `v0.4.0-rc.2`, and the affected checks
are run again.

## v0.3.0

Released 2026-10-24.

Firebase Security Rules for Storage and Firestore, and Cloud Storage for Firebase for the official
SDKs on web, Android, iOS and Flutter, the Admin SDKs for Node and Go, and firebase-tools, with a
build-time production path on every platform. Google's own Storage tests pass against it (145 of
153; every one left is a documented difference), the rules engine agrees with production and the
emulator on every counted case of its oracle, and the network guard around the suites saw no
request leave the machine.

Evidence is in [compatibility](reference/compatibility.md#cloud-storage), the security review in [security](security.md#v03-security-rules-and-cloud-storage), and the differences in [known differences](reference/known-differences.md#cloud-storage).

### What ships

**Security Rules**

- One engine for `firebase.storage` and `cloud.firestore` (Firestore uses it from v0.4), with
  bounded memory and compile time for any source a project publishes.
- The firebaserules v1 API that firebase-tools calls: `:test`, rulesets and releases.
- A locked default for every project's Storage: `allow read, write: if false;`.
- `firestore.get()` and `firestore.exists()` compile in Storage rules and deny until v0.4.

**Cloud Storage**, one default bucket per project on local disk:

- The client API (`/v0`): metadata, downloads by rules or token (one Range, streamed gzip, headers
  that keep uploaded files from running as pages), download URLs, updates, delete, list and
  listAll, and media, multipart and resumable uploads.
- The GCS JSON API for the Admin SDKs, with service accounts only: objects, uploads, downloads,
  `rewriteTo` (copy and move), preconditions, checksums, and bucket CORS.
- `firebase init storage` and `firebase deploy --only storage`.
- The dashboard: a file browser, the Storage rules editor, bucket CORS, and usage and quota.

**Evidence**, from the last green CI run on main (40111454561, `d9391be`):

| Suite | Result |
|---|---|
| firebase-js-sdk Storage integration (Node, Chromium) and storage-compat | 13/13, 13/13, 8/8 |
| firebase-tools Storage conformance (not `emulatorOnly`) | 111/119: 6 documented differences, 2 emulator errors |
| Emulator recordings replayed on Aruma | 188 steps, 836 differences, none an Aruma bug, each classified |
| Browser matrix (Chromium, Firefox, WebKit) | 14/14 on each |
| Admin SDKs: `@google-cloud/storage`, `cloud.google.com/go/storage` | 12/12 and 12/12 |
| Rules oracle: deployed rules, Rules Test API, emulator | 87/87, 3643/3643, 1985/1985, no case more permissive |

### The production path per platform

On its production path every platform gets the unified download URL, `https://<aruma>/v0/b/<bucket>/o/<path>?alt=media&token=…`,
which opens on Aruma in any image loader, player or shared link.

| Platform | Production path (build time) | Fallback (run time) |
|---|---|---|
| Web: Vite 5 to 8, webpack, Rspack, Rollup, esbuild, Next.js (webpack and Turbopack), SvelteKit, Expo (Metro) | `@aruma/web` with `storage: true` swaps the SDK's host when the app is built, and fails the build when it cannot | `installStorage` / `useArumaStorage` |
| Flutter web | `aruma-web flutter-web`: the pinned `firebase-storage.js`, swapped once | as the web |
| Android | Gradle plugin `dev.aruma.storage` | `aruma-redirect` for the Storage host |
| iOS (SwiftPM) | FirebaseStorage from the `arumadev` mirror, patched for `ArumaURL`, through `swift package config set-mirror`; tags pinned in `mirror.lock` | `ArumaRedirect` |
| iOS (CocoaPods) | `aruma_storage.rb` patches the last releases published there | `ArumaRedirect` |
| Flutter (Android, iOS) | the native plugin and mirror, and `aruma_storage` (`fromArumaUrl` before `refFromURL`) | as native |
| Admin Node | `@aruma/admin-storage` in place of `firebase-admin/storage` | — |
| Admin Go | `adminstorage` | — |

A download link from Aruma reads back with `refFromURL` on the web, Android and iOS, and through
`fromArumaUrl` on Android, iOS and Flutter (table in [compatibility](reference/compatibility.md#cloud-storage)).

### Known limits

- **No signed URLs** (`getSignedUrl` and V4 POST policies are refused before anything is signed),
  and no XML API. Planned after launch.
- **No named databases** and no named Firestore rules, and no tools to move data from Firebase.
  Planned after launch.
- One bucket per project, one server, local disk (not NFS). ACLs, `makePublic`, IAM, notifications,
  versions, `compose` and lifecycle rules answer 501, as GCS does for what it does not serve.
- App Check headers are accepted and ignored.
- **Web apps with Auth on Aruma still send the user's Aruma ID token to Google from Firestore and
  callable Functions** (security finding SR3-10). Those services move to Aruma in v0.4; until then such an app should
  not use them. Storage is covered: without `storage: true` the web library refuses Storage
  requests to Google.
- **Publishing rules in a loop can delay other projects' first reads** (security finding SR3-05, publish pileup): each
  publish starts a fresh load of its release, and one project publishing and reading in a loop can
  fill the load turns, so a cold load of another project may wait and answer 503. Decisions and
  memory are not affected. A fix is planned.
- Rules limits stricter than Firebase on files that are not realistic: the literal patterns of one
  file share 4 MiB, and one ruleset may weigh a quarter of the compiled-rules cache.
- Turbopack and Metro cannot fail a build that never reached the SDK; the run-time fallback still
  covers its requests. On plain http browsers send no Fetch Metadata, so the refusal of scripts
  from the dashboard's origin rests on its CSP alone: serve Aruma over https off loopback.
- Admin SDKs for Python, Java and .NET, and Unity and C++, are not measured.
- Smaller items deferred from the last reviews: hardening of the CI network guard; two edge cases of
  the web SDK guard (it also refuses signed-out Storage requests, and a library that replaces
  `XMLHttpRequest` later bypasses it); documenting that Android's strict mode refuses plain download
  links where the web lets them through; and the iOS build gate checking local edits in package
  checkouts.

### Upgrading from v0.2

1. Back up the database (`aruma backup`), stop the server, run `aruma migrate up` with the new
   binary (two migrations: the rules tables and the Storage schema), and start it. The server
   refuses to start on an old schema.
2. Auth and the Realtime Database need no change.
3. Storage is off by default. To turn it on, set `storage.enabled: true` (`ARUMA_STORAGE_ENABLED`)
   and `storage.data_dir` (`ARUMA_STORAGE_DATA_DIR`) to an absolute path on a local disk. Every
   `storage.*` and `rules.limits` key is in [configuration](reference/configuration.md). In short:
   - the disk must be mounted before Aruma starts (systemd `RequiresMountsFor=`). Aruma refuses to
     boot on an empty directory when the database expects files in it.
   - the directory belongs to one server and one database. `aruma backup` copies only the database,
     so back up the directory with it.
   - a new version replaces the old one by stopping it first. In Kubernetes use `strategy: Recreate`:
     a rolling update waits on the directory's lock.
   - serve Storage over https off loopback. Aruma warns at boot when it runs on plain http.
4. Every project starts with locked Storage rules. Deploy yours with `firebase deploy --only storage`
   or from the dashboard.
5. SDK changes for apps already on Aruma:
   - **Web:** `install()` from `@aruma/web` now refuses Storage requests to Google, and the Vite
     plugin fails a build that includes `@firebase/storage` without `storage: true`. If the app uses
     Storage, add `storage: true` (or `installStorage`).
   - **Android:** with `ArumaStorageRedirect=false`, `aruma-redirect` now refuses requests to the
     Storage host instead of letting them reach Google.
6. Building from source needs Go 1.27.3 (the `toolchain` line in `go.mod`).

## v0.2.0

Released 2026-10-21. Realtime Database.

A Firebase-compatible Realtime Database for the official SDKs on web, Android and iOS, for the Admin SDKs for Node and Go, and for firebase-tools, in production mode and emulator mode. Google's own database suites pass against it: 332 tests in production mode and 336 in emulator mode.

### What ships

- The Realtime Database wire protocol over WebSocket and long polling, and REST at `/<path>.json` without streaming.
- Security rules for the database, with a locked default. The whole rules language works except `getPriority()`, which is rejected when you save.
- One database host per database (`rtdb.domain` with a wildcard DNS record), or `?ns=` on `public_url` for development.
- The `firebase` tool's `database:*` commands and `deploy --only database`.
- A data browser and rules editor in the dashboard.
- Off by default. Turn it on with `rtdb.enabled: true` (`ARUMA_RTDB_ENABLED`).

### Known limits

- Admin SDK for Python is not supported. `google-auth` signs with a fixed audience for Google and sends Aruma's token to `iamcredentials.googleapis.com`.
- REST streaming (`text/event-stream`) is not supported.

See the [Realtime Database guide](guides/realtime-database.md), the [operations guide](guides/operations.md) and [compatibility](reference/compatibility.md#realtime-database).

## v0.1.0

Released 2026-10-20. Auth, and the platform under it.

- Projects, apps, API keys, service accounts, the Management API and the dashboard, in one Go binary with PostgreSQL.
- Firebase Auth for the official SDKs on web, iOS, Android and Node, in production mode and emulator mode: Identity Toolkit, Secure Token, the Admin API, identity providers and email actions.
- Redirect libraries that point the official SDKs at Aruma without app code changes.
- The `arumactl` command line tool and the `firebase` tool against the Management API.
- Evidence: 27 of 27 priority scenarios, and 180 passing official Firebase JS Auth tests. See [compatibility](reference/compatibility.md#auth).
