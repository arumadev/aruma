# Compatibility

What works with the official Firebase SDKs and tools when they talk to Aruma, and what does not.

**Supported** means the official SDK or tool works against Aruma without changes to your app code, and a test checks it. Behavior is judged against Google's production service; where production was not recorded, the Firebase emulator and Google's documentation come next. A feature is **Partial** when some of it works or it works with a stated limit, and **Not supported** when Aruma refuses it with an explicit error or does not serve it.

Some products need a small Aruma helper that points the SDK at your server instead of Google (build-time on the web, a redirect library on Android and iOS). The helper does not change the SDK's API. The per-platform tables below say where one is used.

Versions covered: Auth 0.1, Realtime Database 0.2, Cloud Storage and Security Rules 0.3, Firestore 0.4.0.

| Service | Status |
|---|---|
| [Auth](#auth) | Supported, with the gaps listed |
| [Realtime Database](#realtime-database) | Supported, with the gaps listed |
| [Cloud Storage](#cloud-storage) | Supported, with the gaps listed |
| [Security Rules](#security-rules) | Supported |
| [Firestore](#firestore) | Released in 0.4.0; official mobile suites not yet run |

---

## Auth

### Features

| Feature | Status | Notes |
|---|---|---|
| Email and password: sign up, sign in, sign out | Supported | |
| Update profile, update password, re-authentication, `requires-recent-login` | Supported | |
| Anonymous sign-in | Supported | |
| Link anonymous account to email and password | Supported | The uid is kept. |
| Custom tokens (`signInWithCustomToken`) | Supported | Tokens from the official `createCustomToken` work. |
| Token refresh, forced refresh, `onAuthStateChanged` | Supported | |
| Token revocation, user disabled or deleted | Supported | The client is signed out (`auth/user-token-expired`). |
| Email verification, `verifyBeforeUpdateEmail` | Supported | Mails go to the built-in development mailbox. |
| Password reset | Supported | |
| `fetchSignInMethodsForEmail`, email enumeration protection | Supported | Protection is on by default, as in new Firebase projects. |
| Password policy | Supported | `passwordPolicy` is served; the official suites do not test it. |
| Google sign-in (popup, redirect, direct ID token), linking, re-authentication | Supported | Tested over HTTP against a fake provider. A real Google account needs your own credentials. |
| Apple sign-in (`form_post`, nonce, direct ID token), token revocation on delete | Supported | Tested against a fake provider. A real Apple account needs a public HTTPS domain. |
| API key restrictions (web referrers, iOS bundle IDs, Android package and SHA-1) | Supported | A correct restriction passes; a wrong one is refused. |
| Phone sign-in | Not supported | Answers `auth/operation-not-allowed`. |
| Multi-factor authentication | Not supported | |
| Email-link sign-in | Not supported | Answers `auth/operation-not-allowed`. |
| Multi-tenancy | Not supported | |
| SAML and OIDC providers | Not supported | Google and Apple are the only identity providers. |
| Facebook and GitHub sign-in | Not supported | Answers `auth/invalid-provider-id`. |

### Admin API

| Feature | Status | Notes |
|---|---|---|
| `verifyIdToken`, `checkRevoked` | Partial | Use the Aruma Admin Auth libraries for Node and Go. Official `firebase-admin` requires `iss = https://securetoken.google.com/<project>`; Aruma issues `<public_url>/issuers/<project>` on purpose. The libraries are a one-line import change. |
| Create, get, get by email, update, delete user | Supported | |
| `listUsers` | Partial | Pagination works. Password hashes are never returned (`REDACTED`). `nextPageToken` for an exactly full last page differs from the emulator. |
| Custom claims | Supported | Reserved claims and payloads over 1000 bytes are refused. |
| `revokeRefreshTokens`, disable and enable user | Supported | |
| Session cookies | Supported | Create, verify, revoke. Verification with official `firebase-admin` fails for the `iss` reason above. |
| Password reset and email verification links | Partial | The server supports them; the Aruma Admin libraries do not expose them yet. |
| `getUserByPhoneNumber`, phone numbers on users | Not supported | |
| `importUsers`, project config, tenants, SAML/OIDC config | Not supported | The Admin SDK shows "An internal error has occurred". |
| `generateSignInWithEmailLink` | Not supported | |

### SDK and platform coverage

| Platform | Status | Notes |
|---|---|---|
| Web (`firebase` 11, 12, 13) | Supported | End-to-end tests on all three. |
| iOS | Supported | Tested on the simulator (iOS 18.5) with the Aruma redirect library. |
| Android | Supported | Tested on emulators (API 33, 34), debug and R8-minified release builds, with the Aruma redirect library. HTTP to a non-loopback address is allowed in debuggable builds only. |
| Flutter | Supported | Uses the native Android and iOS libraries. Not tested separately. |
| Admin Node, Admin Go | Partial | Through the Aruma Admin Auth libraries (see above). |
| Real devices | Not tested | All mobile tests ran on emulators and simulators. |

On iOS, one startup request goes to `firebaseinstallations.googleapis.com` from Analytics, Crashlytics and Remote Config, even with data collection off. It is not an Auth request.

### Verified by

- 27 of 27 end-to-end scenarios (19 web, 8 Admin), run in CI.
- The official Firebase Auth integration suites (firebase-js-sdk 13.0.0, firebase-admin-node 14.5.0), run against Aruma: 180 passed, 98 failed, 83 skipped. Every failure and skip is classified: 3 were Aruma bugs (fixed, each with a test); the rest are features Aruma does not offer, emulator-only behavior, or test-environment issues. No known bug remains in the supported scope.
- A chat app run against real Firebase and Aruma on the same platform, compared field by field: web 12/12, iOS 12/12, Android 12/12, Node Admin 10/10. The one skipped scenario is sign-in with a real Google account.
- Zero Auth requests to Google on every platform. Network guards in CI (web, Node, Android weekly, iOS twice a week) fail any request that leaves the machine.

---

## Realtime Database

### Features

| Feature | Status | Notes |
|---|---|---|
| WebSocket protocol | Supported | |
| Long-polling fallback | Supported | |
| Read, write, update, push, remove | Supported | |
| Transactions | Supported | |
| `onDisconnect` | Supported | Checked against rules when set and when run. |
| Queries: `orderBy*`, `limitTo*`, `startAt`, `startAfter`, `endAt`, `endBefore`, `equalTo` | Supported | Ordering by child or value needs `.indexOn`, as in Firebase. |
| Priorities, server values (`timestamp`, `increment`) | Supported | |
| Conditional writes (hash, `If-Match`) | Supported | |
| Security rules on every operation | Supported | See [Security Rules](#security-rules). Rule changes cancel listeners that lost access. `getPriority()` in rules is rejected on save. |
| Aruma Auth tokens (`auth`, `auth.provider`, expiry, revocation) | Supported | Disabling or deleting a user cancels their listeners within 0.5 to 3.5 seconds. |
| Admin access that bypasses rules, `databaseAuthVariableOverride` | Supported | |
| REST: `GET`, `PUT`, `POST`, `PATCH`, `DELETE`, `X-HTTP-Method-Override` | Supported | |
| REST parameters: `print`, `shallow`, `format=export`, `orderBy` and the limit/range set, `timeout` | Supported | `writeSizeLimit` and `disableTriggers` are accepted and ignored. |
| REST ETag and `if-match` / `if-none-match` | Supported | |
| `/.settings/rules.json` (read, write, `dryRun`) | Supported | |
| REST streaming (`Accept: text/event-stream`) | Not supported | Answers 400. Admin Python `listen()` and `firebase database:profile` fail for this reason. |
| JSONP (`callback=`), `download=` | Not supported | Ignored. |
| Legacy database secrets in `?auth=` | Not supported | |
| Cloud Functions triggers | Not supported | `disableTriggers` has no effect. |

Known difference: `-0.0` is stored and returned as `0.0`. It can show on Android, iOS and the Admin SDKs.

### SDK and platform coverage

| Platform | Status | Notes |
|---|---|---|
| Web (`firebase` 11, 12, 13) | Supported | 10 end-to-end scenarios per version, plus the long-polling fallback. |
| Android (`firebase-database`, BoM 35) | Supported | 10 scenarios on an API 33 emulator. |
| iOS (`FirebaseDatabase` 12.19) | Supported | 10 scenarios on an iOS 18.5 simulator. |
| Flutter | Supported | Uses the native Android and iOS libraries. Not tested separately. |
| Admin Node (`firebase-admin` 14) | Supported | 9 of 9 checks: get, set, update, push, remove, transaction, rules, `orderByChild`, auth override. |
| Admin Go (`firebase-admin-go` v4) | Supported | Get, Set, Update, Push, Delete, Transaction, ETag helpers, shallow get, queries, `AuthOverride`. |
| Admin Python (`firebase-admin` 7.7) | Not supported | The credential in `google-auth` signs with a fixed Google audience and sends your Aruma token to `iamcredentials.googleapis.com`. Fixing it needs a custom credential in your app code. |
| firebase-tools 15.34.0 | Supported | `database:set`, `get` (with `--shallow`, `--pretty`, `--export`, queries), `update`, `push`, `remove`, `deploy --only database` (and `--dry-run`): 39 checks pass. Use the subdomain database URL; the `?ns=` form is lost by `database:get` and `database:remove`. |

### Verified by

- The official firebase-js-sdk Realtime Database tests: 332 of 332 in production mode (subdomain database URL, TLS); 335 of 336 in emulator mode (the one failure is a timing test, `resolves timestamps locally`).
- Recorded sessions from a real Firebase database, replayed against Aruma (protocol frames and REST), with every difference listed and explained.
- Web, Android and iOS end-to-end tests with real rules, and Admin Node and Go checks over TLS and behind Caddy.

---

## Cloud Storage

### Features

| Feature | Status | Notes |
|---|---|---|
| Upload: simple, multipart, resumable | Supported | Pause, resume and cancel work. |
| Download, `getBytes`, `getBlob`, `getDownloadURL` | Supported | Range requests and gzip are handled. |
| Download tokens (create, delete) | Supported | Tokens count toward the 8 KiB custom metadata limit. |
| Get and update metadata, custom metadata | Supported | |
| List and `listAll`, with page tokens | Supported | |
| Delete | Supported | |
| `refFromURL`, `gs://` URLs | Supported | Web and mobile, with the platform notes below. |
| Bucket CORS | Supported | Matches origin and method literally or `*`; no patterns. |
| Security rules on every request | Supported | `firestore.get()` and `firestore.exists()` read Firestore from Storage rules. |
| Preconditions (`ifGenerationMatch` and others) | Supported | |
| GCS JSON API for Admin SDKs | Partial | Service accounts only. A personal CLI token or user token is refused (401). |
| `firebase deploy --only storage`, `firebase init storage` | Supported | |
| More than one bucket per project | Not supported | Copying to another bucket answers 404. |
| GCS XML API, signed POST policy uploads | Not supported | |
| `makePublic` and object ACLs | Not supported | Answers 501. |
| Rewrite tokens | Not supported | Answers 400. |

Downloads served by Aruma are hardened: content is sent under `sandbox`, PDFs are forced to download, and requests from service workers and scripts on other origins are refused with 403.

### SDK and platform coverage

| Platform | Status | Notes |
|---|---|---|
| Web | Supported | Uses `@aruma/web`, which swaps the Google host for yours at build time. Tested with Vite 5, 6, 7 and 8, webpack, esbuild, Rollup, Rspack, SvelteKit, Next.js 16 (webpack and Turbopack), Expo (Metro) and Flutter web. A link on `firebasestorage.googleapis.com` is rejected after the swap; convert it with `toArumaUrl`. |
| Browsers | Supported | Browser test matrix 14 of 14 on Chromium, Firefox and WebKit. |
| Android | Supported | `dev.aruma.storage` redirect library; `refFromURL` works. |
| iOS | Supported | A patched mirror of the Firebase Storage SDK; `refFromURL` works. |
| Flutter (mobile) | Partial | `aruma_storage` plugin. `refFromURL` with an Aruma link throws, because `firebase_storage` only accepts Google hosts, so app code needs one `fromArumaUrl` call; this is why it is Partial. Verified by `test/e2e/flutter-storage` in the iOS Storage workflow. |
| Admin Node (`@google-cloud/storage` 8.4.0), Admin Go (`cloud.google.com/go/storage` 1.62.1) | Supported | 12 of 12 steps each, through the GCS JSON API. The `@aruma/admin-storage` package passes 15 of 15 checks. |
| firebase-tools 15.34.0 | Supported | Rules deploy and init, end to end. |

### Verified by

- The official Cloud Storage tests (firebase-js-sdk 13.0.0, firebase-tools 15.34.0), unmodified: 145 of 153 pass. js-node 13/13, js-browser 13/13, js-compat 8/8, firebase-tools conformance 111/119. The 8 others are documented differences (6) and emulator bugs where production disagrees with the emulator (2). Zero are Aruma bugs.
- 13 recorded emulator scenarios (188 steps) replayed against Aruma. Every difference is classified; none is an Aruma bug.
- Recorded behavior from real Cloud Storage for the wire formats, error order and CORS.

---

## Security Rules

One rules engine serves Firestore and Cloud Storage. Realtime Database rules (JSON) are enforced by the Realtime Database itself. See [Realtime Database](#realtime-database).

### Features

| Feature | Status | Notes |
|---|---|---|
| Rules language for Firestore (`service cloud.firestore`) and Storage (`service firebase.storage`) | Supported | Same grammar for both. |
| Standard library: strings, regex, math, hashing, bytes, timestamps, paths, lists, maps, sets | Supported | |
| `get()`, `exists()`, `getAfter()`, `existsAfter()` | Supported | 10 lookups per request (20 for multi-document operations) in Firestore, 2 in Storage. |
| `debug()` | Supported | Returns its first argument, as published rules do. |
| `request.auth.token` claims | Supported | Numbers keep their exact type and precision. |
| A project with no published rules | Supported | Closed by default. |
| Publish with `firebase deploy --only firestore:rules` / `storage`, the firebaserules API, or the dashboard | Supported | |
| Test a ruleset (`:test`) | Partial | Compile errors are reported as Firebase reports them. A ruleset with `testCases` is refused (400); `testSuite` is not run. |
| Argument-count warnings on some functions | Partial | Production warns for every function with a known type; Aruma warns for a few. Warnings never change the decision. |
| Rulesets over Aruma's size limits | Not supported | Regex programs are capped at 4 MiB in total, and the whole ruleset at one quarter of 32 MiB of compiled size. Both are far above any real ruleset. |

Known differences from production:

- `values()` of a map has no stable order in published Firestore rules; Aruma keeps the order of the source, so do not rely on the order of `values()`.
- `'x'[float(...)]` makes production answer 500 at compile time; Aruma compiles it and denies at evaluation.
- Evaluation error messages may differ in wording; the allow or deny decision is the same.
- Storage `list` on a path that does not end in `/` is decided on the folder.

### Verified by

Aruma's rules engine is compared with three sources: rules published on real Storage, Google's Rules Test API, and the Firebase emulators.

- Development set: 100% agreement with each source (3,643 counted cases against the Rules Test API, 1,985 against the emulators, 87 against published rules). Aruma never allowed a request that a higher-ranked source denied.
- A second set, kept out of development and measured once: 99.2% against the Rules Test API (246 of 248) and 99.6% against the emulators (239 of 240), with no over-permissive case. Two cases were later reclassified as test-tool artifacts and one binding bug was fixed. A rerun with the current classification gave 100% on both. It reconfirms the result and is not a new measurement.
- Grammar: 1,940 distinct rules files compile with the same result and the same first-error position as production (100%); error text matches in 577 of 577, and the Firestore and Storage service declarations parse identically in 1,892 of 1,892.
- Random rules compared with the emulator: 7 seeds, 6,300 generated cases, 1 difference (the `float` index above, production agrees with the emulator).
- Fuzzing of the parser, evaluator and regex engine.

---

## Firestore

Firestore support is released in v0.4.0. It ships in the Aruma server as the Document Database, off by default. v0.4.0 was released before part of its verification ran, and that verification has still not run. The gaps are listed under [Verification pending](#verification-pending).

### Features

| Feature | Status | Notes |
|---|---|---|
| Documents with every value type | Supported | Including vectors and the BSON shapes production accepts. |
| `Get`, `BatchGet`, `List`, `Create`, `Update`, `Delete`, `Commit`, `BatchWrite`, `ListCollectionIds` | Supported | `(default)` database only. |
| Transactions: optimistic, pessimistic, read-only with `read_time` | Supported | A query inside a pessimistic transaction locks the whole collection it reads. Transactions open at a server restart are lost. |
| Queries: every operator, `OR`, collection-group, `limitToLast`, `PartitionQuery` | Supported | |
| Aggregations: `count`, `sum`, `avg` | Supported | |
| Field transforms | Supported | |
| Real-time listeners (Listen) | Supported | Resume, resets, existence filters by count, named-query bundles. |
| Composite indexes | Supported | Modes `enforce` (production's error and console link), `warn`, `off`. |
| Single-field exemptions, TTL policies | Supported | TTL deletes in the background, hourly by default. |
| Security rules | Supported | See [Security Rules](#security-rules). |
| Admin API for firebase-tools and the Admin SDKs | Supported | Databases, indexes, fields (`ttlConfig`, `vectorConfig`), operations. |
| Dashboard: data browser, rules editor with Playground, indexes, TTL, usage | Supported | Arabic and English. |
| Databases other than `(default)` | Not supported | Planned for v0.4.x. |
| Import and export through GCS, managed backups | Not supported | Use Postgres backups. |
| `findNearest` and vector search | Not supported | Vector values and indexes are stored. |
| Query Explain (`explain_options`) | Not supported | Answers `UNIMPLEMENTED`. |
| Bloom filters in Listen | Not supported | The existence filter carries the count only. |
| Point-in-time recovery, restore, clone | Not supported | `read_time` reaches back as far as the version window you set. |
| Enterprise edition, Pipelines | Not supported | |
| `firestore:locations`, `firestore:bulkdelete`, creating or editing databases | Not supported | |
| Firestore triggers and Extensions | Not supported | They need a Functions environment. |

Stream limits are stricter than Firebase on purpose. They are set under `documents.limits` (see [Configuration](configuration.md)). Nothing is dropped silently: every limit ends in an error code the client sees, a counter and a log line.

| Limit | Default | When exceeded |
|---|---|---|
| `streams_per_project` | 10,000 | New stream refused with `RESOURCE_EXHAUSTED`. |
| `streams_per_ip` | 500 | New stream refused with `RESOURCE_EXHAUSTED`. |
| `targets_per_stream` | 1,000 | That target is removed with `RESOURCE_EXHAUSTED`; the stream stays open. |
| `snapshot_rows` | 1,000,000 | That target is removed with `RESOURCE_EXHAUSTED`. |
| `stream_outbox_bytes` | 64 MiB | A client that stops reading is disconnected after `stream_write_stall`; it reconnects and resumes. |
| `stream_lag_events` | 4,096 | Targets are reset and get a fresh snapshot. |
| `stream_idle` | 5 minutes | Stream closed with `UNAVAILABLE`. |
| `stream_first_message` | 10 seconds | `DEADLINE_EXCEEDED`. |

Streams also close with `UNAUTHENTICATED` when the token expires or the user is disabled, deleted or revoked, and with `UNAVAILABLE` if the server loses its single-instance database lock.

Of the 24 differences from production recorded in the test corpus, 16 are deliberate restrictions, 7 are provisional, and 1 is an emulator bug where Aruma follows production.

### SDK and platform coverage

| Platform | Status | Notes |
|---|---|---|
| Web, full SDK (WebChannel) | Supported | Firebase 13: 21 of 21 tests on each of Chromium, Firefox and WebKit. Firebase 11 and 12 are pending. |
| Web, lite SDK (REST) | Supported | Same suite. |
| Android | Pending | gRPC transport tried with Firestore Android 27.0.1; the official suite has not run. |
| iOS | Pending | gRPC transport tried with iOS 12.19.3; the official suite has not run. iOS trusts only its bundled root certificates, so it needs a public test domain with a certificate from a public authority. |
| Flutter | Pending | Uses the native libraries, so it follows Android and iOS. |
| Admin Node (gRPC and REST), Admin Go | Supported | Smoke tests pass. |
| firebase-tools 15.34.0 | Supported | `deploy --only firestore:indexes` and `firestore:rules`, `firestore:indexes` (export), `firestore:databases:list` and `get`, `firestore:delete -r`, TTL deletion. |

With Auth on Aruma, `@aruma/web` refuses requests to Google's Firestore host so your Aruma ID token cannot reach Google. Route Firestore to Aruma at build time or with `arumaFirestoreSettings`.

### Verified by

- Web end-to-end tests: 21 of 21 on Chromium, Firefox and WebKit (Firebase 13).
- Replay of a 65-file corpus recorded on a real Firebase project, in every test run.
- firebase-tools against the Document Database under a network guard: passes.
- Admin SDK smoke tests (Go, Node over gRPC and REST, JS lite): pass.
- Dashboard: 1,665 unit tests and 226 end-to-end, visual and accessibility tests, in Arabic (RTL) and English.

### Verification pending

Final v0.4.0 depends on these. Do not treat Firestore as fully verified until they are done.

- The official Android and iOS Firestore suites, and the mobile host settings.
- Firebase JS 11 and 12 variants of the web suites.
- Black-box conformance cases for 19 requirements, and the conformance suite against a live server in CI. They are covered by unit tests and recorded replays.
- Fault injection and load tests.
- A security review of this release.
