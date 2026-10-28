# Architecture

This page is for people who run Aruma and people who change its code. It says what runs, how apps reach it, where each service keeps its data, how projects are separated, what is secure by default, and which limits matter. For every setting, see [configuration](reference/configuration.md). For behavior that differs from Firebase, see [known differences](reference/known-differences.md).

## What runs

Aruma is one Go binary and one PostgreSQL database (version 18 or newer).

- The binary serves every Firebase service you turn on, the Management API, the console and the health checks.
- The console (a Next.js app, in English and Arabic) is built to static files and embedded in the binary. There is nothing else to deploy.
- PostgreSQL holds all state. The exception is Cloud Storage, which keeps object bytes in a directory on a local disk and keeps everything that points to them in PostgreSQL.
- `aruma serve` never changes the database schema. You run `aruma migrate up` first, and the server refuses to start on an old schema.
- Realtime Database, Cloud Storage and the Document Database each assume one Aruma process per database. The Document Database enforces this with a PostgreSQL advisory lock. Storage enforces it with a lock on its data directory.

## Ports and hosts

Aruma listens on one public address (`listen`, default `:8080`). Routing is by path. A second, private address (`metrics_listen`, default `127.0.0.1:9464`) serves `/metrics` and the certificate check for Realtime Database hosts. It is never on the public address.

| Path or host | Served by |
|---|---|
| `/` | Console (static files) |
| `/console/v1/` | Console API (session or CLI token, CSRF on writes) |
| `/v1beta1/`, `/v1alpha/projects/`, `/v1/projects/` (except Firestore paths) | Management API and the rules API that the `firebase` tool uses |
| `/oauth2/v1/token`, `/oauth2/v3/token` | Service account tokens and CLI token exchange |
| `/healthz`, `/readyz` | Health |
| `/identitytoolkit.googleapis.com/…`, `/securetoken.googleapis.com/…`, `/__/auth/…`, `/issuers/…`, `/session-issuers/…` | Authentication: client and admin endpoints, sign-in pages, token keys |
| `/v0/`, `/storage/v1/`, `/upload/storage/v1/`, `/download/storage/v1/` | Cloud Storage: the Firebase SDK API and the Admin SDK (GCS JSON) API |
| `/v1/projects/{project}/databases/…`, gRPC `google.firestore.v1.*`, WebChannel | Document Database (gRPC is told apart by `content-type`) |
| `https://<project_id>-default-rtdb.<rtdb.domain>` | Realtime Database. Each database has its own host, because every SDK reads the database name from the first label of the host |
| `/.ws`, `/.lp`, `<path>.json?ns=` on the main host | Realtime Database in development form, when `rtdb.domain` is not set |

Two consequences for operators:

- One domain and one certificate cover everything except Realtime Database. Realtime Database needs a wildcard DNS record under `rtdb.domain` and a proxy that keeps the `Host` header. Caddy with `on_demand_tls` can issue the certificates, asking Aruma at `GET /rtdb/tls-ask` on the private address. See the [operations guide](guides/operations.md).
- The Document Database needs HTTP/2 end to end. A TLS proxy in front of Aruma talks h2c to it. iOS trusts only certificates from a public authority.

Aruma reads `X-Forwarded-For` only from addresses in `trusted_proxies`. Its own public address comes from `public_url`, never from the `Host` header.

## How SDKs reach Aruma

Apps keep the official Firebase SDKs in their normal production mode. They do not use emulator mode.

- **Config file.** You download `firebaseConfig`, `google-services.json`, `GoogleService-Info.plist` or the Flutter equivalent from the console. It carries Aruma's values for `apiKey`, `appId`, `authDomain`, `storageBucket` and `databaseURL`.
- **Redirect libraries** point the parts of an SDK that have a fixed Google address at Aruma. They are small packages in `sdk/`: `@aruma/web` (a build plugin and a fetch shim for Web), `ArumaRedirect` (iOS), `aruma-redirect` and a Gradle plugin (Android), and a Flutter package. The Cloud Storage and Document Database hosts are replaced at build time or through SDK settings.
- **One-line option for Web Auth.** `connectAuthEmulator(auth, "https://<aruma>", { disableWarnings: true })` after `getAuth()`. Aruma serves Google's own Auth paths, so no other change is needed.
- **Your server.** The official `verifyIdToken` rejects Aruma tokens, because they are issued under Aruma's address, not Google's. Swap one import for `@aruma/admin-auth` (Node) or `adminauth` (Go). Any OIDC library can also verify Aruma tokens through the discovery document at `<public_url>/issuers/<project_id>/.well-known/openid-configuration`.
- **Admin SDKs** use a service account key downloaded from the console. They send a JWT signed with that key. The `firebase` tool uses a CLI token and environment variables such as `FIREBASE_API_URL`.

## How each service stores data

Each service owns a PostgreSQL schema with its own migrations. The `events` schema holds shared change events.

| Service | Schema | Storage model |
|---|---|---|
| Platform | `control` | Projects, apps, API keys, service accounts, rulesets and rules releases. |
| Authentication | `auth` | Users, hashed refresh tokens and per-project signing keys. Private signing keys are encrypted with envelope encryption (AES-256-GCM) under the master key. |
| Realtime Database | `rtdb` | One row per leaf value, keyed by database and path (`jsonb` value and priority). Inner objects are inferred from their leaves. One writer per database applies commands in arrival order and batches them into PostgreSQL transactions. It sends changes to listeners only after the commit. Listeners live in memory. |
| Cloud Storage | `storage` | One row per object (name, generation, checksums, metadata, encrypted download tokens). Bytes live on local disk under `storage.data_dir`. Upload sessions, a delete queue for orphaned files and per-project usage are tables. |
| Document Database | `documents` | One row per document (protobuf bytes), plus index entries, versions and a change log. Each database has one sequence row that orders all writes and supplies every `read_time`. A dispatcher inside the process sends changes to Listen streams in sequence order. |

Security rules for Firestore and Storage run in one hand-written Go engine. Rulesets and releases are stored in `control`.

## Tenancy

- **Project.** Has a `project_id` (6 to 30 characters, lowercase letters, digits and hyphens, never reused) and a 12-digit `project_number` that the server generates. Deleting a project or app is soft for 30 days and can be undone in that time.
- **Isolation.** The `project_number` is the first column of every key in every data table. Every query on a project table takes it as a condition, and a CI test checks this. Aruma does not use PostgreSQL row-level security.
- **App.** Belongs to one project and one platform (`web`, `ios` or `android`). Its `app_id` is `1:<project_number>:<platform>:<16 hex>`.
- **API key.** One per platform per project. It starts with `Aru` and is 39 characters long. It is not a secret and gives no admin right. It only tells Aruma which project a request is for, and its restrictions (referrers, bundle IDs, package and SHA-1) are enforced like Google's.
- **Service account.** Named `<name>@<project_id>.sa.<project_domain>`. Its private key is shown once when you create it and is never stored; Aruma keeps only the public key. Admin SDKs use it to get tokens (`aruma_sat_…`) from `/oauth2/v1/token`.
- **Console accounts.** Separate from end users of your apps. The first account needs a one-time setup code printed in the log. Sign-up is closed by default. CLI tokens (`aruma_pat_…`) belong to a console account and act as the owner of the projects it owns.

What a caller does not own returns `NOT_FOUND`, not `PERMISSION_DENIED`. Cloud Storage's Admin API is the exception and answers 403.

## Security defaults

- No default account or password. Sign-up is closed.
- A new Realtime Database, Cloud Storage bucket and Document Database starts with locked rules. Admins, service accounts and the project owner's CLI token bypass the rules.
- `Bearer owner` and any token with `alg: none` are always refused and logged. The one exception is the Document Database, where `Bearer owner` is accepted if you turn on `documents.dev_owner_token`, and only on a loopback development server.
- Admin credentials for the Document Database are accepted only over TLS, from a trusted proxy that sets `X-Forwarded-Proto: https`, or on a loopback development server.
- Console passwords use argon2id. Session, CSRF, CLI and service-account tokens are random 256-bit values, and only an HMAC of each is stored. All keys derive from `master_key` (HKDF-SHA256). A leaked database does not contain a usable token or private key.
- Console sessions: cookie `__Host-aruma_session` (`Secure`, `HttpOnly`, `SameSite=Lax`), 30 minutes idle, 8 hours absolute. Writes need a CSRF token.
- Rate limits and a rising delay on sign-in, and one error message for an unknown user, a wrong password and a disabled account.
- CORS is closed for the console, the Management API and admin endpoints. It is open, without cookies, only for the endpoints that SDKs call from a browser: the Auth client endpoints, token keys and discovery documents, and the Cloud Storage API for the Firebase SDK. Storage downloads follow the bucket's own CORS setting, which is empty by default.
- The console is served with a strict content security policy, `frame-ancestors 'none'`, `nosniff` and HSTS on https.
- Files served from Cloud Storage get a sandboxing policy, PDFs download instead of rendering, and requests from workers are refused.
- ID tokens last one hour. Signing keys rotate every 30 days, and a retired key stays published until its last token expires. Revoking a user's tokens is exact to the second.

## Limits that matter

Most limits can be changed. See [configuration](reference/configuration.md) for names and defaults.

| Area | Limit |
|---|---|
| All routes except those below | Request body 1 MiB, header read 10 seconds, idle connection 120 seconds |
| Realtime Database | Path 768 bytes, depth 32, one string 10 MB, WebSocket write 16 MB, REST write 16 MB by default, 64 MB written per minute per database, 10,000 connections per project |
| Cloud Storage | One object and one project 5 GiB by default, custom metadata 8 KiB, upload sessions 7 days, an upload slower than 4 KiB per second over 30 seconds is cut |
| Document Database | Message 17 MiB, 10,000 streams per project, 500 per client address, 1,000 targets per stream, transactions end after 270 seconds, 1,000 composite indexes per database |
| Security rules | Firebase's own limits (call depth 20, 10 lookups per operation, 256 KiB file) plus Aruma limits that protect the server |
| Auth | ID token one hour; custom token claims at most 1,000 bytes |

## Design records

Aruma was built from numbered design records. Code comments across the repository cite them by number, for example "ADR-021 §4". The full records are not published. This table lists every record, so a citation can be read without them. Numbers 001 to 003, 005 and 006 are not in this table because they are not part of this set.

| ID | Title | What it decides |
|---|---|---|
| ADR-004 | ID token contract and issuer | Tokens carry the issuer `<public_url>/issuers/<project_id>` (session cookies use a separate issuer); RS256 keys per project and purpose, rotated every 30 days; one-hour tokens; refresh tokens do not expire but are revoked through `tokens_valid_after`; custom tokens are accepted; unsigned tokens and `Bearer owner` never are. |
| ADR-007 | Projects, keys and identifiers | The formats of `project_id`, `project_number`, `app_id`, API keys and service account emails; one API key per platform; how config file fields are derived; 30-day soft delete. |
| ADR-008 | Single entry point | One port and one domain, routed by path; `/metrics` on a private address; trusted proxies; shared body and timeout limits. |
| ADR-009 | Data tooling | pgx without an ORM, sqlc for queries, goose migrations per schema, no DDL in `aruma serve`, `project_number` first in every key, PostgreSQL 18 minimum. |
| ADR-010 | Secure defaults | A list of defaults every release must meet (closed sign-up, CSRF, rate limits, closed CORS, security headers, no secrets in logs) and how each secret is hashed or encrypted. |
| ADR-011 | Estimation calibration | A planning record on how work size is measured. No effect on behavior. |
| ADR-012 | Durations: measured and estimated | A planning record of how long work took. No effect on behavior. |
| ADR-013 | What counts as "one line" | The only app change allowed per platform (one Web Auth line, one import swap on the server); declared exceptions for Google and Apple sign-in and the `firebase` tool; the Admin emulator variable is refused. |
| ADR-014 | Hosting the Auth endpoints | The Auth route table; open CORS without cookies for client routes only; authorized domains, `state` tokens and API key restrictions enforced on the server. |
| ADR-015 | Services layout and boundaries | Each service is its own package layer; services plug into the console through one mount type; dependency rules are checked by a test. |
| ADR-016 | Languages of the token verification library | Node and Go only; other languages use any OIDC or JWT library through the discovery document. |
| ADR-017 | Project isolation inside PostgreSQL (row-level security) | Deferred. Isolation relies on the leading `project_number` key and CI checks. Revisit if runtime-built queries, an isolation bug or mutually distrusting tenants appear. |
| ADR-018 | Realtime Database URL, domain and certificate | `https://<project_id>-default-rtdb.<rtdb.domain>`; a development form with `?ns=`; how the server finds the database from the host; on-demand certificates through a private `ask` endpoint. |
| ADR-019 | Realtime Database storage, write order and limits | One row per leaf; one writer per database; in-process fan-out after commit; the default size and rate limits. |
| ADR-020 | Realtime Database 0.2 scope | Server-side query filtering and `.indexOn`; the rules language (without `getPriority()`); REST without streaming; long polling at `/.lp`. |
| ADR-021 | Rules engine for Firestore and Storage | One hand-written Go engine for both services, with its limits, evaluation order, compile errors and storage of rulesets. |
| ADR-022 | Cloud Storage architecture | Package layout; one default bucket per project; tables; a small disk driver; the upload state machine; download tokens; safe serving of bytes; CORS; the Firebase and GCS API surfaces; limits. |
| ADR-023 | Scope of 0.3: rules language and Cloud Storage | What shipped in 0.3, what was deferred, and the documented differences. |
| ADR-024 | Document Database: storage, write order and locks | Table model; one sequence row per database orders writes and gives `read_time`; in-process lock manager; one instance through an advisory lock; version retention. |
| ADR-025 | Document Database: queries, indexes and transactions | A reference query path and an index planner with re-checking; automatic single-field indexes; composite index enforcement like production; aggregations; transaction models. |
| ADR-026 | Document Database: Listen and Write streams | The Listen contract (targets, snapshots, resume, per-target errors); stream limits and overflow policy; cancellation on token expiry; the mobile Write stream. |
| ADR-027 | Document Database: transport and protos | gRPC through the `connect` library on the one port, over HTTP/1.1 and HTTP/2; REST and WebChannel; keepalive; TLS from a public authority for iOS. |
| ADR-028 | Document Database: name, 0.4 scope and verification | The name "Document Database"; what ships in 0.4.0 and what comes later or never; parse then reject with a message, never ignore; how compatibility is verified. |
| ADR-029 | Document Database: admin authentication | Accepts the self-signed service account JWT and `aruma_sat_`/`aruma_pat_` tokens on this surface; rules for checking the JWT; the conditions under which `Bearer owner` is allowed. |
| ADR-030 | Document Database: console API | The console's data browser, rules, indexes and TTL routes under `/console/v1/projects/{project}/documents`; dashboard writes are admin writes that bypass rules. |
