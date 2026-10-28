# Known differences from Firebase

Aruma serves the Firebase APIs so that your app keeps the official SDKs. In some places it behaves differently, on purpose or because a feature is not built yet. This page lists them by service. Things that behave the same are not listed.

For each difference you get what Firebase does, what Aruma does, and what it means for your app. Limits that an operator can change are marked "configurable". See [configuration](configuration.md).

## Across services

| Topic | Firebase | Aruma | What it means for your app |
|---|---|---|---|
| Emulator mode | The SDKs have emulator functions and variables, and `Bearer owner` is an admin token in the emulators | `Bearer owner` and unsigned tokens are always refused. The only exception is the Document Database on a loopback development server with `documents.dev_owner_token` | Do not set `*_EMULATOR_HOST` variables for a real Aruma server. Use the config file Aruma generates. On the web, `connectAuthEmulator` with Aruma's address is a development option (see the [web Auth guide](../guides/auth-web.md)); other emulator functions are not a supported way to connect |
| Fixed Google addresses | SDKs call Google hosts such as `firebasestorage.googleapis.com` | Aruma serves the same paths, but SDKs with a hard-coded host need a redirect: a build plugin, a small library, or an SDK setting | Each platform needs the redirect described in its guide. Without it, the SDK talks to Google |
| API key shape | `AIza…` | `Aru…` (39 characters), one per platform per project | Treat it as opaque. Code that checks for `AIza` fails |
| Project domain | Google domains for the auth domain, bucket and database | Your own domain: `authDomain` is the project domain, and the bucket is `<project_id>.<project_domain>` | Changing `project_domain` changes bucket names, so stored links break |
| Cloud Functions (`httpsCallable`) | The SDK calls your function at Google with the user's ID token | Aruma does not serve Cloud Functions, and the web package does not block these calls. An app that signs in with Aruma Auth sends the user's Aruma ID token to Google when it calls a function | Do not call Cloud Functions from an app that signs in with Aruma Auth |

## Authentication

| Topic | Firebase | Aruma | What it means for your app |
|---|---|---|---|
| Token issuer | ID tokens are issued by `https://securetoken.google.com/<project>` | ID tokens are issued by `<public_url>/issuers/<project_id>` and session cookies by `<public_url>/session-issuers/<project_id>`. Keys are published at the issuer's JWKS address | The official `verifyIdToken` and `verifySessionCookie` reject Aruma tokens. On your server, swap one import to `@aruma/admin-auth` (Node) or `adminauth` (Go). Other languages can use any OIDC or JWT library |
| `FIREBASE_AUTH_EMULATOR_HOST` on your server | Makes the Admin SDK skip token verification | Aruma's libraries refuse to start if it is set (you can allow it explicitly outside production) | Remove the variable from server environments |
| Custom tokens | Signed with a service account key | Same. `createCustomToken` works unchanged. Aruma requires a key of the same project, and rejects `firebase` and `sub` inside the claims | Use a service account key of the project that receives the token |
| Google and Apple sign-in on Web | The SDK loads one script from `apis.google.com` | Same script, loaded from Google. This is the only request to Google that Auth makes. Email and password, anonymous, custom token and refresh make none | A strict content security policy or an offline test environment must allow that script |
| Other federated providers | Facebook, GitHub, SAML, OIDC and others | Only Google and Apple. Others return `auth/invalid-provider-id` | Do not rely on other providers yet |
| Phone, multi-factor, email-link sign-in | Supported | Not built. The SDK gets `auth/operation-not-allowed`, as for a provider that is turned off | Plan for email and password, anonymous, custom token, Google and Apple |
| Tenants | Multi-tenancy | Not available. A non-empty `tenantId` returns `auth/invalid-tenant-id` | Do not use tenants |
| Admin SDK methods | The full Admin Auth API | Supported: `verifyIdToken`, `verifySessionCookie`, `createSessionCookie`, `createUser`, `getUser`, `getUserByEmail`, `getUsers`, `updateUser`, `deleteUser`, `deleteUsers`, `listUsers`, `setCustomUserClaims`, `revokeRefreshTokens`. Not built: phone numbers, `importUsers`, project config, tenants, SAML and OIDC config, and the email action links such as `generatePasswordResetLink` | Calls outside the supported list fail |
| Password hashes | `listUsers` can return the password hash and salt | The hash is never returned. The field holds `REDACTED` | Do not export password hashes through the Admin SDK |
| `signInWithRedirect` across sites | Documented limit in browsers that partition third-party storage | Same limit. It applies when the app and Aruma (or `authDomain`) are on different sites | Serve Aruma on the app's own site, or use `signInWithPopup` |

## Realtime Database

| Topic | Firebase | Aruma | What it means for your app |
|---|---|---|---|
| Database address | `https://<name>.firebaseio.com` | `https://<project_id>-default-rtdb.<rtdb.domain>`. Without `rtdb.domain`, a development form `…/?ns=<name>` that suits local use only | In production the operator must set up wildcard DNS and a proxy that keeps the host. iOS, Admin Go and Admin Python do not work with the `?ns=` form |
| REST streaming | `Accept: text/event-stream` streams changes | Not built. The request returns 400 | `listen()` in Admin Python and `firebase database:profile` fail. Listen through a Node, Web, Android or iOS SDK |
| Admin Python | Works | Not supported. `google-auth` signs for Google's token address and sends Aruma's token to `iamcredentials.googleapis.com` | Use Admin Node or Admin Go. Python needs a custom credential in your code |
| Admin Node | `cert()` works | `cert()` signs for Google, so use a credential that asks Aruma for the token (`aruma-credential.mjs`) | Copy the credential file into your project |
| Rules: `getPriority()` | Supported | Rejected when you save the rules | Remove it before you publish |
| Rules change with open listeners | Not documented | Listeners that are no longer allowed are cancelled at once, not at the next reconnect | A tightened rule takes effect immediately |
| Rejected auth token on a live connection | A rejected token leaves the session on its previous identity | The session continues as anonymous. The SDKs close the connection on any rejection anyway | Not visible to apps that use an SDK |
| REST credentials | Legacy database secrets in `?auth=` | Database secrets do not exist. `?auth=` takes a user's ID token; `Bearer` or `access_token` takes an admin token only. An invalid `?auth=` is ignored and the request runs as anonymous | Use an admin token for REST admin work |
| JSONP | `callback=` is supported | Ignored; the reply is plain JSON | Do not use `callback=` |
| REST write size | Up to 256 MB | 16 MB by default (`rtdb.limits.rest_write_bytes`, configurable) | Large REST imports need the operator to raise the limit |
| Negative zero | — | A stored `-0.0` is returned as `0.0`. It can show on Android, iOS and the Admin SDKs | Do not rely on the sign of zero |
| Large reads | A single request has a size cap and fails with "Data requested exceeds the maximum size that can be accessed with a single request." | Same error, but the cap counts stored rows: 1,000,000 per read by default (configurable) | Use `shallow=true` or narrower queries |

## Cloud Storage

| Topic | Firebase | Aruma | What it means for your app |
|---|---|---|---|
| Buckets | Several buckets per project | One default bucket per project, `<project_id>.<project_domain>`. Buckets cannot be created, listed or deleted. A copy to another bucket returns "The specified bucket does not exist." | Keep all files in the default bucket |
| Download link | `https://firebasestorage.googleapis.com/v0/b/<bucket>/o/<path>?alt=media&token=…` | `https://<aruma>/v0/b/<bucket>/o/<path>?alt=media&token=…`. On Web, `ref()` with a Google link throws `storage/invalid-url` | Use `getDownloadURL()`. Convert old stored Google links with `toArumaUrl` from `@aruma/web` |
| Storage on Web, Android, iOS | Works by default | Needs the build-time redirect (`@aruma/web`, the Gradle plugin, or the patched iOS package). A runtime fallback exists | Without the redirect the SDK talks to Google |
| Overwriting an object | Not verified against Firebase | Every new write through the Firebase SDK API creates a new download token, so the old link stops working | Read the new link after each upload |
| Custom metadata set to `null` | The GCS API keeps download tokens inside custom metadata | On the Firebase SDK API it clears custom metadata but keeps the tokens. On the GCS API it clears the tokens too, so old links stop working | Do not clear metadata through the GCS API if you share links |
| Download tokens and metadata size | The GCS API keeps tokens in custom metadata | Tokens count toward the 8 KiB custom metadata limit (about 200 tokens on an object with no other metadata) | Do not create many tokens per object |
| Bucket CORS | Set per bucket | A bucket's CORS setting governs `alt=media` downloads only. The default is no rule, so cross-origin `fetch` is refused until you set one | Set CORS for the bucket before using `getBytes` or `fetch` from another origin |
| Serving files | Does not apply these restrictions | Every type is served with a sandboxing policy, PDFs are forced to download, requests from workers (Service Worker and similar) are refused with 403, and `Cache-Control` defaults to `private, max-age=0` | HTML and SVG you upload cannot run scripts in Aruma's origin. A PDF cannot be shown in a frame on another site |
| Object size | Up to 5 TiB | 5 GiB per object and per project by default (configurable, object limit up to 5 TiB) | The operator raises `object_bytes` and `project_bytes` for large files |
| Upload abuse limits | No such limits are documented | A body slower than 4 KiB per second over 30 seconds is cut with 408. Caps apply to live upload sessions per project, user and address, and to upload starts per address. Admin imports use a separate, looser limit | Retry on poor connections; the resumable protocol continues. Large batches from one client may get 429 |
| Signed POST policies and the XML API | Supported | Not served | Upload through the SDK or the resumable API |
| Admin SDK (GCS API) | Full API | Needs a service account token. Unsupported calls return 501: `copyTo`, `compose`, `moveTo`, `restore`, ACL, IAM, notifications, and bucket create, list, delete and PUT-update. Fields such as `acl`, holds, retention, `customTime`, KMS and a storage class other than `STANDARD` return 501. Copy works through `rewriteTo` | Use `copy` and `move` as the Node and Go libraries do. Do not set holds or retention |

## Security Rules (Firestore and Storage)

Rules run in Aruma's own engine. It follows Firebase's language and limits. These are the differences.

| Topic | Firebase | Aruma | What it means for your app |
|---|---|---|---|
| Order of `values()` | Not stable: three identical writes gave different orders | Keeps the order of the written map. `keys()` is always sorted | Do not write rules that depend on the order of `values()` |
| Invalid replacement text in `replace()` (such as a lone `$`) | An internal error (HTTP 500) with no text | An evaluation error, so the rule denies | Both reject the request. Fix the pattern |
| Float used as an index (`'x'[float(…)]`) | An internal error at compile time | Compiles and fails at evaluation, so the rule denies | Avoid it |
| Argument-count and type warnings | Many | Fewer. Aruma reports errors and a few warnings (unknown or unused function, missing `rules_version`) | A clean deploy on Aruma does not prove a clean deploy on Firebase |
| Very large `split()` | "maximum memory usage … reached" | Rejected with "evaluation exceeded the allocation limit of 16777216 bytes" | The decision is the same. The message differs |
| Aruma-only limits | None | Limits that protect the server: total size of compiled patterns in a ruleset (4 MiB, configurable), compiled rulesets in memory, one pattern's program size, evaluation work and memory. A file past them fails to compile with "Regular expressions are too complex to compile safely" or "The rules are too large to keep compiled" | Only unusual rulesets are affected. Firebase's own limits can be lowered, never raised |
| Busy server | Not applicable | Compilation waits for a free slot. A request that waits too long gets 503 or `CANCELLED` | Retry. Heavy publish loops from one project can slow others |

## Document Database (Firestore-compatible)

The Document Database speaks the Firestore protocols. Its protocol names stay Firestore's, such as `google.firestore.v1` and `service cloud.firestore`.

| Topic | Firebase | Aruma | What it means for your app |
|---|---|---|---|
| Databases | Named databases | Only `(default)`. Any other name returns `NOT_FOUND`. Creating a database returns `UNIMPLEMENTED` | Keep one database per project |
| Not built | Vector search (`find_nearest`) with a vector index, Query Explain, point-in-time recovery, backups, export and import to Cloud Storage, triggers, Extensions | `UNIMPLEMENTED` with a message that names the feature. Triggers and Extensions do not exist | Copy data with a script that uses the Admin SDKs. See the [migration guide](../guides/firestore-migration.md) |
| Composite indexes | A query that needs a missing index fails with a console link | The default is the same (`enforce`), with the same error text. `warn` serves the query and logs the index. `off` serves every query | Deploy your indexes before you switch. `off` hides missing indexes that Firebase would reject |
| TTL deletes | Typically within 24 hours | A sweeper runs every hour by default (configurable). It deletes through normal commits, so listeners see the delete. It does not delete subcollections | Deleted documents disappear up to one interval late |
| Listen stream limits | No documented limits | Limits per project (10,000), per client address (500), per stream (1,000 targets, 1,000,000 rows per snapshot read), an idle stream ends after 5 minutes with `UNAVAILABLE`, and a client that stops reading is cut. All configurable | The SDKs reopen streams on their own. A target over a limit is removed with `RESOURCE_EXHAUSTED` while the stream stays open |
| Token expiry on a stream | Not recorded. The token travels only in the headers that open the stream | The stream closes with `UNAUTHENTICATED` when the user's or service account's token expires. A user who is disabled, deleted or revoked has their streams closed at once | The SDK refreshes the token and reconnects |
| Transactions | Contention gives `ABORTED` "Too much contention on these documents. Please try again." | Same text. Aruma also ends a transaction 270 seconds after it begins, waits 20 seconds for a lock, and caps open transactions per database and per user | Retry on `ABORTED`, as with Firebase |
| Request size | Smaller write bounds | A commit holds at most 10,000 writes and 50,000 field transforms, far above anything production was seen to accept. A message may be up to 17 MiB | None for apps that stay within Firebase's limits |
| Index counts | — | 1,000 composite indexes and 1,000 field overrides per database (configurable), the number production allows with billing | The operator can set a lower number |
| Admin credentials | — | Accepted only over TLS to Aruma, through a trusted proxy that sets `X-Forwarded-Proto: https`, or from loopback on a development server. Otherwise `UNAUTHENTICATED` | Run the Admin SDKs against an https address. A Node client with `ssl: false` sends `Bearer owner` and is refused |
| Certificates | — | iOS trusts only certificates from a public authority, so a self-signed or private-CA certificate cannot connect from iOS. Node's `firebase-admin` needs `GRPC_DEFAULT_SSL_ROOTS_FILE_PATH` for a private authority | Use a public certificate on the proxy |
| Instances | Managed | One Aruma process per database, held by an advisory lock. A second process answers the Document Database with `UNAVAILABLE`. A pooler in transaction mode does not hold the lock | Connect Aruma to PostgreSQL directly |
| Other known differences | — | Differences between production Firestore and the emulator or an SDK's assumption are listed in `test/oracle/documents/parity-exceptions.json`, each with the test that proves it. There are 24: 16 where Aruma is stricter than Firebase on purpose, 7 provisional (no production recording yet), and 1 where the emulator is wrong and Aruma follows production | Read that file for the exact cases |
| Not yet checked | — | The full Android and iOS test suites, and Admin SDKs other than Node and Go, have not been run against it | Test your own app before you move to production |
