# Using Firestore with Aruma

Your app keeps the official Firebase SDKs for Cloud Firestore. Only the host changes: it points at your Aruma server instead of Google. This guide is for a new app. If you already run Firestore in a live Firebase project and want to move it, read [Moving your app's Firestore to Aruma](firestore-migration.md) as well.

In Aruma the service is called the **Document Database**, described as "Firestore-compatible". The protocol names stay Google's: `google.firestore.v1` and `firestore.googleapis.com` in the SDKs, and `service cloud.firestore` in rules.

## Release candidate status

Firestore support is a **release candidate** (v0.4.0-rc.1). It is off by default. The code is the code planned for v0.4.0, but part of the checking that a final release needs has not run yet.

Verified:

- Web, full SDK (WebChannel) and lite SDK (REST), with Firebase 13: 21 of 21 end-to-end tests on each of Chromium, Firefox and WebKit.
- Admin Node (gRPC and REST) and Admin Go: smoke tests pass.
- firebase-tools 15.34.0: `deploy --only firestore:indexes` and `firestore:rules`, `firestore:indexes` (export), `firestore:databases:list` and `get`, `firestore:delete -r`, and TTL deletion.
- A 65-file corpus recorded on a real Firebase project is replayed against Aruma in every test run.
- The dashboard: 1,665 unit tests and 226 end-to-end, visual and accessibility tests, in Arabic (RTL) and English.

Pending:

- The official Android and iOS Firestore suites. The gRPC transport was tried on Firestore Android 27.0.1 and iOS 12.19.3, but the suites have not run. Flutter uses the native libraries, so it follows them.
- Firebase JS 11 and 12 variants of the web suites.
- Black-box conformance cases for 19 requirements, and the conformance suite against a live server in CI. Unit tests and recorded replays cover them.
- Fault injection and load tests.
- A security review of this release.

Do not treat Firestore as fully verified until these are done. The full table is in [compatibility](../reference/compatibility.md#firestore).

## What is not supported

- Databases other than `(default)`. Each project has the `(default)` database only.
- `findNearest` and vector search. Vector values and indexes are stored.
- Query Explain, point-in-time recovery, restore, clone, managed backups, and import or export through Cloud Storage. Use Postgres backups.
- Bloom filters in Listen.
- Firestore triggers and Extensions. They need a Functions environment.
- `firestore:locations`, `firestore:bulkdelete`, and creating or editing databases with firebase-tools.

Features that are not built answer `UNIMPLEMENTED` with a message that names the feature.

## 1. Turn it on

Set `documents.enabled: true` in the config file, or `ARUMA_DOCUMENTS_ENABLED=true`, and restart the server. See [configuration](../reference/configuration.md#document-database) for every setting.

```yaml
documents:
  enabled: true
```

Until you turn it on, the dashboard shows "The Document Database is off on this server".

Three things to know before you write code:

- **Rules start closed.** Every project begins with `allow read, write: if false;` until you publish rules. Clients cannot read or write anything before that. See [Rules](#rules).
- **One server process per Postgres database.** A second process on the same database answers the Document Database with `UNAVAILABLE`. Connect Aruma to Postgres directly, not through a pooler in transaction mode.
- **gRPC needs HTTP/2 end to end.** Behind a TLS proxy, the proxy must speak HTTP/2 without TLS (h2c) to Aruma. In Caddy, use `transport http { versions h2c 2 }`. A proxy that falls back to HTTP/1.1 breaks the listeners.

For local development only, `documents.dev_open_rules: true` lets clients on your machine read and write every database without rules. The server accepts it only when `public_url` and `listen` are loopback addresses. Do not use it for anything else.

## 2. Connect your app

Each SDK builds its address from `host` and `ssl` in its settings. Pointing at Aruma means changing the host in production mode. Do not use the emulator functions (`connectFirestoreEmulator`, `useEmulator`) or `FIRESTORE_EMULATOR_HOST` against a real Aruma server. They put the SDK in emulator mode, and Aruma refuses the `owner` token that emulator mode sends.

Use the Firebase config file that the dashboard generates for your app. It is the same file as for the other services. Add an app under your project, then copy the web snippet or download `google-services.json` or `GoogleService-Info.plist`. See [Pointing mobile apps at Aruma](mobile-apps.md).

End users sign in with Aruma Auth. The ID token goes with every request, and the rules decide what each user reaches.

### Web

Install `@aruma/web` from your clone, as [auth-web.md](auth-web.md) describes. It rewrites the Firestore host at build time, so the app code does not change. With Vite:

```js
// vite.config.js
plugins: [aruma({ url: 'https://aruma.example.com', firestore: true })],
```

Then use the SDK as usual:

```js
import { initializeApp } from 'firebase/app';
import { getFirestore, collection, addDoc } from 'firebase/firestore';

const app = initializeApp(firebaseConfig); // from the dashboard
const db = getFirestore(app);              // no connectFirestoreEmulator
await addDoc(collection(db, 'rooms'), { name: 'general' });
```

The line for other bundlers:

| Bundler | The line |
|---|---|
| Vite 5 to 8 and SvelteKit | `aruma({ url, firestore: true })`, with `storage: true` too if the app uses Storage |
| webpack, Rspack, Rollup, esbuild, Rolldown | `arumaFirestore.webpack({ url })` and so on (`@aruma/web/unplugin`) |
| Next.js | `withArumaFirestore(config, { url })` (`@aruma/web/next`) |
| Expo, React Native (Metro) | `withArumaFirestore(config, { url })` (`@aruma/web/metro`) |
| Flutter web | `npx aruma-web flutter-web --url <url> --products storage,firestore` |

If your app uses Aruma Auth, turn Firestore on in the plugin before you use it. Without it the build fails, and `@aruma/web` refuses requests to Google's Firestore host, so your Aruma ID token cannot reach Google. If the build-time rewrite cannot reach your build, set the host with the SDK's own settings:

```js
import { initializeFirestore } from 'firebase/firestore';   // or 'firebase/firestore/lite'
import { arumaFirestoreSettings } from '@aruma/web/firestore';
const db = initializeFirestore(app, arumaFirestoreSettings('https://aruma.example.com'));
```

Call it before the first `getFirestore()`. Import `firebase/firestore` from npm: a page that loads the SDK from a CDN cannot be rewritten, and the build refuses it. The `@aruma/web` README has the full details.

### Android, iOS and Flutter

The config files do not carry the Firestore host, so set it in code before the first use. The official Android and iOS Firestore suites have not run yet, so treat this as **not verified**. The setting names come from the SDK documentation. An Android instrumented test sets the host the same way, on an emulator, with `setHost` and `setSslEnabled`.

```kotlin
// Android
firestore.firestoreSettings = firestoreSettings { host = "aruma.example.com"; isSslEnabled = true }
```

```swift
// iOS
let s = FirestoreSettings(); s.host = "aruma.example.com"; s.isSSLEnabled = true
Firestore.firestore().settings = s
```

```dart
// Flutter
FirebaseFirestore.instance.settings = const Settings(host: 'aruma.example.com', sslEnabled: true);
```

Production needs a TLS certificate from a public authority on the proxy in front of Aruma. iOS trusts only its bundled root certificates, so a self-signed or private-authority certificate does not connect. For development on your own machine you can set `isSslEnabled = false` (Android) and allow cleartext traffic for the host in a debug-only `network_security_config`. The auth setup for these platforms (the Aruma redirect libraries) is in [mobile-apps.md](mobile-apps.md).

### Admin SDKs

Download the service account key file from the dashboard. A Google key file does not work here.

An admin credential is accepted only over TLS to Aruma, from a proxy listed in `trusted_proxies` that sends `X-Forwarded-Proto: https`, or from this machine when the server is a loopback development server. Anything else is refused with `UNAUTHENTICATED`. See [configuration](../reference/configuration.md#document-database).

Admin Node, over TLS (set `preferRest: true` for REST):

```js
initializeApp({ credential: cert('service-account.json'), projectId });
getFirestore().settings({ host: 'aruma.example.com', ssl: true });
```

Node's gRPC client ignores `NODE_EXTRA_CA_CERTS`. If your certificate comes from a private authority, set `GRPC_DEFAULT_SSL_ROOTS_FILE_PATH`.

Admin Go:

```go
client, err := firestore.NewClient(ctx, project,
    option.WithEndpoint("aruma.example.com:443"),
    option.WithCredentialsFile("service-account.json"))
```

Admin Python, Java and other Admin SDKs are not verified. Admin access bypasses security rules, as in Firebase.

### firebase-tools

firebase-tools reaches Aruma only with a CLI token (dashboard, Account, CLI tokens) and these variables:

```sh
A=https://aruma.example.com
export FIREBASE_API_URL=$A FIREBASE_TOKEN_URL=$A FIREBASE_RULES_URL=$A \
  FIREBASE_FIREBASESTORAGE_URL=$A FIREBASE_SERVICE_USAGE_URL=$A FIRESTORE_URL=$A \
  FIREBASE_TOKEN=<aruma_pat_...> FIREBASE_SKIP_INFORMATIONAL_IAM=1
```

In `firebase.json`, only the `(default)` database is used:

```json
{ "firestore": { "rules": "firestore.rules", "indexes": "firestore.indexes.json" } }
```

## 3. Indexes

Single-field indexes are automatic, as in Firebase. A query that combines fields, such as a filter on one field and an order on another, needs a **composite index**.

### Without an index

With the default setting (`documents.index_enforcement: enforce`), a query that needs a composite index and does not have one is refused. The error is production's, with its text and link. Create the index, wait until it is ready, and run the query again. A new index builds in the background (`CREATING`, then `READY`), and a query that needs it before then is refused the same way.

Two other modes exist. Both are for development:

- `warn` serves the query and logs the index it needs.
- `off` serves every query, as the Firebase emulator does.

Use `enforce` in production. With `off` your app can work on Aruma and then fail against a server that enforces indexes.

### Deploy indexes from a file

Write `firestore.indexes.json` the way you would for Firebase:

```json
{
  "indexes": [
    {
      "collectionGroup": "messages",
      "queryScope": "COLLECTION",
      "fields": [
        { "fieldPath": "room", "order": "ASCENDING" },
        { "fieldPath": "sentAt", "order": "DESCENDING" }
      ]
    }
  ],
  "fieldOverrides": []
}
```

Then deploy it, with the environment from the firebase-tools section above:

```sh
firebase deploy --only firestore:indexes --project <aruma-project>
```

`firebase firestore:indexes` exports the indexes of a project in the same format. Deploying an exported file again changes nothing. `indexes` holds composite indexes of both scopes (`COLLECTION` and `COLLECTION_GROUP`). `fieldOverrides` holds single-field exemptions and TTL fields.

### In the dashboard

Open your project, then **Document Database**, then **Indexes**:

- **Manual** lists composite indexes with their collection, fields, scope and status (Building, Enabled, Needs repair). You can create and delete them there.
- **Automatic** shows the automatic single-field settings and lets you add exemptions per field.
- **Time to live** manages TTL policies.

The limits are 1,000 composite indexes and 1,000 field overrides per database. Both can be changed under `documents.limits`.

## Rules

Rules decide what your users can read and write. A project starts closed, so publish rules before your app goes to users. The language, the testing options and the limits are in [Security Rules](security-rules.md).

Publish in one of three ways:

- `firebase deploy --only firestore:rules --project <aruma-project>`
- The dashboard: **Document Database**, then **Rules**. The editor checks the rules as you type, keeps a version history, and has a Playground where you simulate a get, create, update or delete as a signed-in or signed-out user.
- The firebaserules API.

Admins bypass rules: a project's service account, the project's owner (a CLI token), and the dashboard's own data editor.

## 4. Live listeners

`onSnapshot` works with both the web SDK (WebChannel) and the gRPC SDKs. Resume after a reconnect, resets, existence filters and named-query bundles are supported. The SDKs reopen streams on their own.

A listener ends in these cases:

- The user's token expires, or the user is disabled, deleted or revoked: the stream closes with `UNAUTHENTICATED`.
- The stream is idle for 5 minutes with no target: it closes with `UNAVAILABLE`, and the client reopens it when needed.
- The server loses its single-instance database lock: `UNAVAILABLE`.

Stream limits are stricter than Firebase on purpose. Nothing is dropped silently: every limit ends in an error code the client sees, a counter and a log line. The defaults are under `documents.limits`:

| Limit | Default | When exceeded |
|---|---|---|
| `streams_per_project` | 10,000 | New stream refused with `RESOURCE_EXHAUSTED`. |
| `streams_per_ip` | 500 | New stream refused with `RESOURCE_EXHAUSTED`. |
| `targets_per_stream` | 1,000 | That target is removed with `RESOURCE_EXHAUSTED`. The stream stays open. |
| `snapshot_rows` | 1,000,000 | That target is removed with `RESOURCE_EXHAUSTED`. |
| `stream_outbox_bytes` | 64 MiB | A client that stops reading is disconnected after `stream_write_stall` (30 seconds). It reconnects and resumes. |
| `stream_lag_events` | 4,096 | Targets are reset and get a fresh snapshot. |
| `stream_first_message` | 10 seconds | `DEADLINE_EXCEEDED`. |

## 5. Transactions and batched writes

Batched writes (`writeBatch`, `BulkWriter`) and `runTransaction` work as in Firebase. Optimistic, pessimistic and read-only transactions (with `read_time`) are supported. End-user clients open optimistic transactions. Admin SDKs can open pessimistic ones.

Limits to know:

- A transaction ends 270 seconds after it begins.
- A write or transaction that waits more than 20 seconds for another transaction's lock is aborted with `ABORTED` and "Too much contention on these documents. Please try again." This is production's text. Retry on `ABORTED`, as you would with Firebase.
- A query inside a pessimistic transaction locks the whole collection it reads.
- Transactions open at a server restart are lost.
- Open transactions are capped per database: 1,000 locking, 10,000 read-only or optimistic, and 100 of the latter per signed-in user. Past a cap, beginning a transaction is `ABORTED`.
- A single Commit or BatchWrite holds at most 10,000 writes and 50,000 field transforms. Past that the request is `INVALID_ARGUMENT`.
- One gRPC message or REST body is at most 17 MiB (`documents.limits.message_bytes`).

Field transforms (`serverTimestamp`, `increment`, array union and remove) are supported, as are the aggregations `count`, `sum` and `avg`.

## 6. Time to live (TTL)

A TTL policy deletes documents whose Timestamp field has passed. Set it up one of two ways:

- Add a field override with `"ttl": true` to `fieldOverrides` in `firestore.indexes.json` and deploy the file.
- Use **Document Database**, **Indexes**, **Time to live**, then **Create policy**. You can set one policy per collection group.

A background sweeper does the deleting. It runs every hour by default (`documents.limits.ttl_sweep_interval`), so a document can be deleted up to an hour after its time, and later if a database has a lot due. Firebase says "typically within 24 hours". Each delete is an ordinary commit, so listeners see it.

The sweeper does not delete a document whose field is not a Timestamp, and it does not delete subcollections.

## 7. The dashboard

Open your project, then **Document Database**. It has these pages:

- **Data** is a browser for collections, documents and fields with their types. You can add, edit and delete documents and fields, and delete a collection with all that is in it. Field names cannot contain `/`, start with a dot, or be longer than 1,500 bytes. A document that does not exist but has subcollections shows as "missing document". Writes from the dashboard are admin writes and bypass rules.
- **Rules** is the editor and Playground described above.
- **Indexes** has the Manual, Automatic and Time to live tabs described above.
- **Usage** shows what the database counted for the project: reads, writes and deletes, peak snapshot listeners and connections, and rules allows, denies and errors. It includes the dashboard's own requests and may not match what a bill would show.

## 8. Limits and differences

Differences from Firebase are listed in [known differences](../reference/known-differences.md). The ones you will meet first:

- Only the `(default)` database exists. Any other name gets `NOT_FOUND`.
- Stream limits (above) and transaction limits (above) are stricter and configurable.
- TTL deletes run hourly by default.
- Of the 24 differences from production recorded in the test corpus, 16 are deliberate restrictions, 7 are provisional, and 1 is an emulator bug where Aruma follows production.

For the exact status of each platform, see [compatibility](../reference/compatibility.md#firestore). To move data in from a live Firebase project, see [Moving your app's Firestore to Aruma](firestore-migration.md).
