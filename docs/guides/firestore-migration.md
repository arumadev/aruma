# Moving your app's Firestore to Aruma

The official Firebase SDKs (Cloud Firestore) stay as they are. Only the **host** changes. This guide is for people who use Firestore in a live project and are moving it to Aruma. It covers pointing the SDKs at Aruma, then indexes, rules and TTL, then data, then the dashboard.

In Aruma the service is called the **Document Database**, described as "Firestore-compatible". The protocol names stay Google's: `google.firestore.v1` and `firestore.googleapis.com` in the SDKs, and `service cloud.firestore` in rules. Behaviour matches what was recorded from production. Where it differs, see [compatibility](../reference/compatibility.md) (ADR-027, ADR-028).

**Marks in this guide:** anything not run in the repository's tests is marked "**Not verified**". Anything proven with an official tool or SDK against a live server names its source.

## What works

| Feature | Status |
|---|---|
| Web (`firebase` 11, 12 and 13): full (WebChannel) and lite (REST) | Supported, with `@aruma/web` (below). Firebase 13 verified (21/21 on Chromium, Firefox and WebKit); 11 and 12 not verified yet |
| Admin Node (`firebase-admin`) and Admin Go (`cloud.google.com/go/firestore`) | Supported over gRPC and REST (`preferRest`). See `test/smoke/admin-node` and `test/smoke/admin-go/documents` |
| Android, iOS and Flutter | The transport is tried on Android 27.0.1 and iOS 12.19.3 (ADR-027). **Their full test suites are not finished, so this is not verified yet.** Flutter uses the same native SDKs |
| The `firebase` tool | `deploy --only firestore:indexes` and `firestore:rules`, `firestore:indexes`, `firestore:databases:list` and `get`, and `firestore:delete -r`. `TestDocumentsWithTheFirebaseCLI` proves them with firebase-tools 15.34.0 under the network guard |
| Composite indexes, single-field exemptions, and TTL | Supported. They are exported and deployed with no loss (same test) |
| Rules (`service cloud.firestore`) | Supported. See [compatibility](../reference/compatibility.md) |
| A database named anything but `(default)` | **Not supported.** Each project has `(default)` only, and any other name gets `NOT_FOUND` (ADR-028) |
| `find_nearest` with a matching vector index, Query Explain, PITR, backups, and GCS export and import | `UNIMPLEMENTED`, with a message that names the feature (ADR-028) |
| Firestore triggers and Extensions | Do not exist (they need a Functions environment) |

## 1. Pointing the SDKs at Aruma

Each SDK builds its address from `host` and `ssl` in its settings (by default it contains `firestore.googleapis.com`). So pointing at Aruma means swapping the host **in production mode**, not with the emulator functions. Production needs a **TLS certificate from a public authority** on a proxy in front of Aruma (Caddy, for example) that talks to Aruma over h2c. iOS trusts only public authorities, so a self-signed or private-CA certificate means it cannot connect (ADR-027). The proxy snippet is in [operations.md](operations.md#realtime-database).

**Web** (the `@aruma/web` package. Its details are in [auth-web.md](auth-web.md) and in the Cloud Firestore section of `sdk/web/aruma-web/README.md`): no change in the app code.

```js
// vite.config: adding firestore: true replaces the Firestore host at build time
plugins: [aruma({ url: 'https://aruma.example.com', firestore: true })],
```

Without a build tool (a CDN, for example) the requests are refused, so import `firebase/firestore` from npm. If the build-time rewrite cannot work, use the SDK's own settings:

```js
import { initializeFirestore } from 'firebase/firestore';   // or 'firebase/firestore/lite'
import { arumaFirestoreSettings } from '@aruma/web/firestore';
const db = initializeFirestore(app, arumaFirestoreSettings('https://aruma.example.com'));
```

Next.js, webpack, Metro and Flutter web have equivalents in the package README. Do not use `connectFirestoreEmulator`.

**Android, iOS and Flutter** (not verified in their test suites. The names come from the SDK documentation): `google-services.json` and `GoogleService-Info.plist` do not carry the Firestore host, so set it in code before first use:

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

On your own machine for development without TLS, use `isSslEnabled = false` (ADR-027). Android needs a `network_security_config` that allows `http` for the host. End users sign in with Aruma Auth, and the ID token goes with every request, so the rules govern access.

**Admin Node:** with TLS, the service account key works with no change (`firebase-admin` sends a self-signature that Aruma accepts, ADR-029). The following comes from `test/smoke/admin-node/documents.test.mjs`, except `ssl: true`:

```js
initializeApp({ credential: cert('service-account.json'), projectId });
getFirestore().settings({ host: 'aruma.example.com', ssl: true });   // preferRest: true for REST
```

With a private authority, set `GRPC_DEFAULT_SSL_ROOTS_FILE_PATH` (grpc-js ignores `NODE_EXTRA_CA_CERTS`). Without TLS (`ssl: false`), gRPC sends the text `Bearer owner`, not the key. That is accepted only on a loopback development server that has `documents.dev_owner_token` (ADR-029). **Admin Go:** `firestore.NewClient(ctx, project, option.WithEndpoint("aruma.example.com:443"), option.WithCredentialsFile("service-account.json"))`, or `FIRESTORE_EMULATOR_HOST` for local development (h2c). Admin Python, Java and the rest: **not verified**.

Do not use `FIRESTORE_EMULATOR_HOST` against a real server. It puts the SDK in emulator mode, so it sends `owner`, and Aruma refuses that outside loopback.

## 2. Indexes: from the live project to Aruma

The composite indexes you created yourself, **and those created by the "Create index" button in a console error link, are all ordinary composite indexes** in production, so the tool exports them with the rest. In Aruma, a query that needs a missing index is refused with the production error and the text of its link (`documents.index_enforcement: enforce`, the default). So move the indexes **before** your app starts using Aruma.

```sh
# 1) From the live project (your normal login, with no Aruma variables in the environment)
firebase firestore:indexes --project <live> > firestore.indexes.json
# 2) To Aruma (the environment is in the next block)
firebase deploy --only firestore:indexes --project <aruma-project>
```

The file holds `indexes` (composite indexes of both scopes, COLLECTION and COLLECTION_GROUP, plus array-contains and vectors) and `fieldOverrides` (single-field exemptions, and TTL fields with `"ttl": true`). Deploying the exported file again changes nothing. A test proves it on a file with one composite index, one vector index, two exemptions and a TTL field: no loss and no difference. Each index builds in the background (`CREATING`, then `READY`), and a query that needs it before then is refused, as in production. The two limits are 1000 composite indexes and 1000 exemptions per database (`documents.limits`, see [configuration](../reference/configuration.md)).

**Not verified:** exporting a large live project with every index shape (Datastore mode, other scopes). If a line is refused, the reply names it. Tell us about it.

The tool reaches Aruma only if you set the environment (a CLI token from the dashboard: Account > CLI tokens). This is what the test sets:

```sh
A=https://aruma.example.com
export FIREBASE_API_URL=$A FIREBASE_TOKEN_URL=$A FIREBASE_RULES_URL=$A \
  FIREBASE_FIREBASESTORAGE_URL=$A FIREBASE_SERVICE_USAGE_URL=$A FIRESTORE_URL=$A \
  FIREBASE_TOKEN=<aruma_pat_…> FIREBASE_SKIP_INFORMATIONAL_IAM=1
```

The `(default)` database exists for every project, so you do not create it. Creating a database with the tool (`POST databases`) is `UNIMPLEMENTED`. In `firebase.json`, only `"firestore": {"rules": "firestore.rules", "indexes": "firestore.indexes.json"}` is used, for the `(default)` database.

## 3. Rules

Copy `firestore.rules` from your project (or download it with `firebase init firestore` on the live project) and publish it: `firebase deploy --only firestore:rules --project <aruma-project>`, or use the "Rules" screen in the dashboard (it has a check before saving and a playground). A new database is **locked** until you publish your rules. The Admin SDKs and the CLI token bypass the rules. Deploying the rules read back from the server again does not create a new ruleset (a test proves it). For the language, its limits and what differs, see [compatibility](../reference/compatibility.md). **Not verified:** your own rules. Try them in the playground before you switch.

## 4. TTL

The definition stays in `fieldOverrides` (`"ttl": true` on one Timestamp field per collection, as in Firebase). It arrives when you deploy the same indexes file, or from the TTL screen in the dashboard. A sweeper then deletes the documents whose time has passed. Each delete is an ordinary Commit, so listeners see it. The sweep runs **every hour** by default (`documents.limits.ttl_sweep_interval`), so a delete can come up to an hour after the time. (Production says "typically within 24 hours".) It does not delete a document whose field is not a Timestamp, and it does not delete subcollections. A test proves that an expired document was deleted, and that a later one and one with a text field stayed. (The sweeper is set to one second in the test.)

## 5. Data

What exists today, honestly:

- **No managed import** of a GCS export (`gcloud firestore export`), and no export. Both are `UNIMPLEMENTED`, and ADR-028 rules them out for good (Google Cloud infrastructure). There is also no ready-made data migration tool. It is postponed (ADR-028).
- **Copy with a script using the official Admin SDKs** (not verified as a full migration, but Aruma supports everything it needs: `listCollections`, `getAll`, queries, `bulkWriter` and `BatchWrite`). Read from the live project with its key, and write to Aruma with its project key, using two `initializeApp(..., name)` apps. Three things to handle:
  - Document references (`DocumentReference`) carry the project name, so rebuild them on the Aruma database.
  - Subcollections are downloaded with `listCollections()` on each document. These include "missing" documents with no fields (`show_missing`, which Aruma supports).
  - A nanosecond-precision Timestamp moves as it is, but do not use `serverTimestamp()` when copying.
- **Documents that TTL deletes:** arrange not to copy them already expired. Otherwise the sweeper deletes them an hour later.
- **Without downtime:** copy, then point the apps at Aruma. There is no continuous sync between the two.

Check the copy by counting documents (`count()`) on both sides, and by sampling values.

## 6. The dashboard

In the dashboard, go to the project > **Document Database** (`/project/documents`). It has the data browser (collections, documents and fields with their types; add, edit and delete; recursive delete), then Rules, Indexes (composite and exemptions), TTL, and Usage. Writes from the dashboard are admin writes that bypass the rules, as in the Firebase console. **Not verified by this guide:** that the screens match the Firebase console. The dashboard's own tests are still to come.

## Migration checklist

1. Enable the service (`documents.enabled`, see [configuration](../reference/configuration.md)), and set up a TLS proxy with a public certificate.
2. Move the indexes (section 2) and wait until all are `READY`.
3. Publish the rules (3) and TTL (4).
4. Copy the data (5).
5. Point the SDKs at Aruma (1), and try your queries. A "The query requires an index" error means an index is missing.
