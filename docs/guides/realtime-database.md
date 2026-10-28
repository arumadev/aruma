# Connecting your app to Aruma Realtime Database

Your app keeps the official Firebase SDKs as they are. It takes the database address (`databaseURL`) from the config file that Aruma generates. This guide is for people who use Realtime Database in Firebase and are moving their app to Aruma. For the server side (domain, certificates, proxy, timeouts), see [operations.md](operations.md#realtime-database).

## What works in 0.2

| Feature | Status |
|---|---|
| Web (`firebase` 11, 12 and 13), Android (BoM 35), iOS (`FirebaseDatabase` 12) | Supported: reads, writes, listeners, queries, transactions, onDisconnect and offline use |
| Admin Node (`firebase-admin` 14) and Admin Go (v4) | Supported, with the production URL only (see the next table) |
| The `firebase` tool (`database:*` and `deploy --only database`) | Supported, with the production URL only |
| Rules | The whole language except `getPriority()`, which is rejected when you save |
| REST at `/<path>.json` | Supported, **without streaming** (`text/event-stream`) |
| Admin Python | **Not supported.** `google-auth` signs with a fixed `aud` for Google and sends Aruma's token to `iamcredentials.googleapis.com` |

For details, every difference from Firebase, and the limits, see [compatibility](../reference/compatibility.md) (ADR-020).

## The database URL

You find it in the dashboard (project > Realtime Database > `databaseURL`, with a copy button) and in the config files: `databaseURL` for web, `firebase_url` in `google-services.json`, and `DATABASE_URL` in `GoogleService-Info.plist`. It has two forms (ADR-018):

| Form | When | Example |
|---|---|---|
| **Production:** one host per database | The server has `rtdb.domain` | `https://chat-app-default-rtdb.rtdb.example.com` |
| **Development:** `?ns=` on `public_url` | No `rtdb.domain`, on your own machine only | `http://127.0.0.1:8080/?ns=chat-app-default-rtdb` |

What works with each SDK:

| SDK | Production | Development (`?ns=`) |
|---|---|---|
| Web, Android | Yes | Yes. Android needs `http` permission for the host (`network_security_config`), and `adb reverse` so the device reaches `127.0.0.1` |
| iOS | Yes | Only on `127.0.0.1` or `localhost`. On a three-label host (`aruma.example.com/?ns=`) the SDK takes the first label and ignores `ns` |
| Admin Node, Admin Go | Yes | **No.** An `http` URL puts them in emulator mode, so they send the `owner` token, and Aruma does not accept it (Node runs as anonymous, Go gets 401) |
| `firebase` tool | Yes | **No.** The `?ns=` is lost from the request, and the tool sends its token only to https |
| Unity/C++ | Yes | **No.** They do not know `ns` |

**The emulator functions** (`connectDatabaseEmulator` on web, `useEmulator` on Android and iOS) are not needed. The development form in the config file is enough, and none of Aruma's tests call them. If you use them in a client app, they swap the host and port and keep the database name from `databaseURL`. Do not pass `mockUserToken`: Aruma does not accept an unsigned token.

## Connecting from each platform

The samples come from tests that run against Aruma. The rules they assume are in [test/e2e/rtdb-rules.json](../../test/e2e/rtdb-rules.json).

**Web** (Auth through `@aruma/web`, see [auth-web.md](auth-web.md). The database needs nothing):

```js
const app = initializeApp(firebaseConfig); // the Aruma file as it is, with databaseURL in it
const db = getDatabase(app);               // no connectDatabaseEmulator
await set(ref(db, `users/${uid}/profile`), { name: 'Sara' });
await push(ref(db, room), { from, text, ts: serverTimestamp() });
const low = await get(query(ref(db, board), orderByChild('score'), limitToFirst(2)));
```
(From `test/e2e/web-rtdb/src/app.js` and `tests/rtdb.spec.mjs`.)

**Android** (`google-services.json` from Aruma, and `firebase-database` from `firebase-bom:35.0.2`):

```java
FirebaseDatabase db = FirebaseDatabase.getInstance();
DatabaseReference me = db.getReference("status").child(uid);
me.onDisconnect().setValue(map("state", "offline", "at", ServerValue.TIMESTAMP));
Query top = db.getReference(board).orderByChild("score").limitToLast(3);
```
(From `test/e2e/android-rtdb/app/src/androidTest/java/com/aruma/e2e/RtdbE2ETest.java`. Its `map(...)` builds a `HashMap`.)

**iOS** (`GoogleService-Info.plist` from Aruma):

```swift
FirebaseApp.configure()
let db = Database.database()
try await db.reference(withPath: "users/\(uid)/profile").setValue(["name": "Sara"])
let top = db.reference(withPath: board).queryOrdered(byChild: "score").queryLimited(toLast: 3)
```
(From `test/e2e/ios-rtdb/Tests/RtdbE2ETests.swift`.)

**Admin Node:** `cert()` in `firebase-admin` sends the key's signature to Google and ignores `token_uri`. Use a credential that asks Aruma for the token. Copy [aruma-credential.mjs](../../test/e2e/admin-node/aruma-credential.mjs) into your project:

```js
import { arumaCredential } from './aruma-credential.mjs';
const app = initializeApp({ credential: arumaCredential('service-account.json'), projectId, databaseURL });
const db = getDatabase(app);
await db.ref('smoke/a').set({ n: 1, s: 'salam' });
await db.setRules(rules);
```
(From `test/smoke/admin-node/rtdb.test.mjs`. Adding `databaseAuthVariableOverride: { uid: 'u1' }` to `initializeApp` makes calls obey the rules.)

**Admin Go** (the service account key is enough, because `golang.org/x/oauth2` respects `token_uri`):

```go
app, err := firebase.NewApp(ctx, &firebase.Config{DatabaseURL: databaseURL}, option.WithCredentialsFile("service-account.json"))
c, err := app.Database(ctx)
err = c.NewRef("go/basic").Set(ctx, map[string]any{"n": 1})
q := c.NewRef("scores").OrderByChild("score").LimitToFirst(3)
```
(From `test/smoke/admin-go/rtdb/rtdb_test.go`. `AuthOverride` in `firebase.Config` makes calls obey the rules.)

**The `firebase` tool** (Aruma's address on https, and a CLI token from the dashboard: My account > CLI tokens):

```sh
export FIREBASE_API_URL=https://aruma.example.com FIREBASE_TOKEN_URL=https://aruma.example.com \
  FIREBASE_RTDB_MANAGEMENT_URL=https://aruma.example.com FIREBASE_TOKEN=<CLI token> FIREBASE_SKIP_INFORMATIONAL_IAM=1
firebase database:set /smoke smoke.json --project chat-app --force
firebase database:get /smoke --project chat-app
firebase deploy --only database --project chat-app   # firebase.json: {"database": {"rules": "database.rules.json"}}
```
(From `test/experiments/firebase-cli-rtdb.sh`.)

## Rules

- **A new database is locked** (`{".read": false, ".write": false}`). The Admin SDKs and the CLI token bypass the rules. Clients get access to nothing until you publish your rules.
- **Publish** in one of three ways: the "Rules" screen in the dashboard, `firebase deploy --only database`, or `PUT /.settings/rules.json` with an admin token (add `?dryRun=true` to check without saving). An error comes back as `line:column: message`, and the previous rules stay.
- **`rtdb.dev_open_rules`** (`ARUMA_RTDB_DEV_OPEN_RULES`) is for development only. The server accepts it only when `public_url` and `listen` are on loopback, and it gives clients on the same machine open rules. Do not test your rules with it.

**`.indexOn` matters more in Aruma than you may expect.** Queries are filtered on the server (ADR-020):

| Order by | Without `.indexOn` | With `.indexOn` |
|---|---|---|
| Key or priority | Filtered on the server | Same |
| Child value (`orderByChild`) or value (`orderByValue`) | Listening: all children arrive with a `no_index` warning (on web: "Using an unspecified index…"), and the SDK filters them itself. `get()`: fails with "Index not defined, add ".indexOn"…". REST: 400 with the same text | Filtered on the server. Only what is in the window arrives |

```json
"messages": { ".indexOn": ["ts"], "$id": { ".write": "auth != null" } }
```

## REST

The address is `<databaseURL>/<path>.json`, or `http://127.0.0.1:8080/<path>.json?ns=<ns>` in development. Credentials work as in `internal/services/rtdb/api/rest_auth.go`:

| Where | What is accepted | Anything else |
|---|---|---|
| `Authorization: Bearer`, then `?access_token=` | An admin token only: a service account token (`aruma_sat_…`, from `token_uri`) or the project owner's CLI token (`aruma_pat_…`). It bypasses the rules | 401 "Unauthorized request.", and that includes a user's ID token |
| `?auth=` | The ID token of an Aruma Auth user in the project. The request runs as that user | **Ignored** (expired, forged or not a token) and the request runs as anonymous: public data can be read, protected data gets 401 "Permission denied" |
| `auth_variable_override=<JSON>` | With an admin token only: the request runs with that variable under the rules, and `null` means anonymous | Ignored without an admin token. Invalid JSON gets 400 |

- The first credential present is the one used, in the order above.
- `/.settings/rules.json` is for an admin token only: no token gets 403, and a user token gets 401.
- Supported: `GET`, `PUT`, `POST`, `PATCH` and `DELETE`, `print=pretty|silent`, `shallow=true`, `format=export`, queries (`orderBy="ts"` with the quotes), and `X-Firebase-ETag: true` with `if-match` (412 on mismatch) and `if-none-match` (304).
- `writeSizeLimit` is accepted with its five values and stops nothing (Aruma's limit is `rtdb.limits.rest_write_bytes`). An unknown value gets 400.
- **No streaming:** `Accept: text/event-stream` gets 400. Use an SDK to listen.

```sh
curl "https://chat-app-default-rtdb.rtdb.example.com/users/$UID.json?auth=$ID_TOKEN"
curl -X PUT -H "Authorization: Bearer $CLI_TOKEN" --data-binary @database.rules.json \
  "https://chat-app-default-rtdb.rtdb.example.com/.settings/rules.json"
```
(The second comes from `put_rtdb_rules` in `test/e2e/server.sh`.)

## Good to know

- **Limits** match Firebase's published limits (path 768 bytes, depth 32, write 16 MB). The operator can change them. See [compatibility](../reference/compatibility.md).
- **Numbers** stay as written, 64-bit integer or double, as in production. `Long` and `Double` on Android and iOS do not mix.
- **Long polling** (`/.lp`): a browser whose WebSocket failed once (after a server restart, for example) starts using long polling. Aruma serves it, then moves the browser back to WebSocket, so you do not need `forceWebSockets()`. The proxy only has to pass `/.lp` and the WebSocket upgrade.
- **Disabling or deleting a user, or revoking their sessions,** cuts their listeners within seconds (0.5 to 3.5 seconds in tests).
- **onDisconnect** is checked against the rules when you register it and when it runs, using the session's identity at the moment of disconnect.
- **Offline:** writes apply locally and reach the server when it is back. Persistent storage on Android and iOS (`setPersistenceEnabled` and `isPersistenceEnabled` with `keepSynced`) works.

## Testing your app on Aruma

- **Local server:** follow "Running locally" in the [README](../../README.md), and add `ARUMA_RTDB_ENABLED=true` to `.env`. Without `rtdb.domain`, the config files carry the development form. For the production URL locally (Admin SDKs and the tool), `test/experiments/rtdb-caddy.sh` runs the whole setup behind Caddy.
- **Full examples:** `test/e2e/web-rtdb`, `android-rtdb` and `ios-rtdb` (their scenarios are in [test/e2e/README.md](../../test/e2e/README.md)), and `test/smoke/admin-node` and `admin-go`.

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| "Specify the database" or "Firebase error. Please ensure that you have the URL…" | Wrong database name, or the proxy changes `Host` | Copy `databaseURL` from the dashboard, and make the proxy keep the host ([Operations, section 4](operations.md#realtime-database)) |
| iOS or Admin Go cannot find the database at `https://aruma.example.com/?ns=…` | The `?ns=` form on a public host: iOS takes the first label, and Go appends the path to the URL as it is | Use the production URL with `rtdb.domain` |
| Admin Node is refused on everything, or Admin Go gets 401 | An `http` URL: emulator mode and the `owner` token | Use the production URL on https, and `arumaCredential` in Node |
| `permission_denied` | The database is locked by default, or the rule does not allow it | Publish your rules, and test them with a real user, not with `dev_open_rules` |
| Warning "Using an unspecified index", or `get()` fails with "Index not defined" | No `.indexOn` for the child you order by | Add `.indexOn` at the path the message names |
| REST 401 "Unauthorized request." | An ID token in `Authorization` or `access_token` | Put the ID token in `?auth=`, or use an admin token in `Bearer` |
| The browser stays on long polling after a restart | The proxy does not pass the WebSocket upgrade or `/.lp` | Pass the `Upgrade` and `Connection` headers and all paths ([Operations, section 5](operations.md#realtime-database)) |
| `listen()` in Admin Python or `database:profile` fails with 400 | Streaming is not supported in 0.2 | Read with `GET`, or listen with a Node SDK |
