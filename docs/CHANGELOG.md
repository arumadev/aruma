# Changelog

One section per release, newest first. Each release has an upgrade section when its steps are not obvious. Releases: [v0.4.0-rc.1](#v040-rc1), [v0.3.0](#v030), [v0.2.0](#v020), [v0.1.0](#v010).

## Unreleased

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
