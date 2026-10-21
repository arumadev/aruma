# Experiments T22 and T23: official tools against Aruma by configuration only

Results for exit criterion X-10. Run on 2026-10-18 against a live Aruma started by `test/smoke/genconfig -serve` (project `smoke-app` with one app per platform and a service account key).

| | Version |
|---|---|
| Machine | macOS 15 (Darwin 24.5.0), arm64 |
| Node / Go / Flutter | 24.21.0 / 1.27.2 / 3.44.2 |
| firebase-tools | 15.34.0 |
| flutterfire_cli | 1.4.1 |
| firebase-admin (Node) | 14.5.0, with google-auth-library 11.3.1 and gtoken 8.0.0 |
| golang.org/x/oauth2 (Go) | 0.37.0 |

## Method

- **Network guard** ([test/experiments/netguard.cjs](../../test/experiments/netguard.cjs)): preloaded into every Node process and its Node children with `NODE_OPTIONS=--require`. It records every TCP connection by host and refuses anything that isn't loopback.
- **OS sandbox**: on macOS every CLI also runs under `sandbox-exec`, which denies all outbound network except loopback. This covers the Dart process of the FlutterFire CLI, which the Node guard can't see.
- **HTTPS**: a small TLS proxy ([tls-proxy.mjs](../../test/experiments/tls-proxy.mjs)) with a self-signed certificate sits in front of the plain-http test server, for the reason given under T23. Node trusts it through `NODE_EXTRA_CA_CERTS`.
- **Reproduce**: run the scripts below. Each one starts and stops its own server, and its throwaway database is dropped on exit.
  - `test/experiments/firebase-cli.sh` (T23, 20 checks)
  - `test/experiments/admin-node.sh` (T22, 3 tests)

## T23: firebase-tools and the FlutterFire CLI

**Conclusion: works, with four exceptions.** All six firebase-tools commands and `flutterfire configure` succeeded. No connection left loopback. The exceptions:

1. The API origin must be **https**.
2. Three phone-home paths must be switched off.
3. The only working credential is the **deprecated** `FIREBASE_TOKEN`.
4. `flutterfire reconfigure` can't be used.

### Configuration used

```sh
export FIREBASE_API_URL=https://<aruma>        # Management API origin
export FIREBASE_TOKEN_URL=https://<aruma>      # token refresh goes to {origin}/oauth2/v3/token
export FIREBASE_TOKEN=aruma_pat_...            # a CLI token from the console
export CI=true                                 # no MOTD fetch
export NO_UPDATE_NOTIFIER=1                    # no npm update check
export XDG_CONFIG_HOME=<dedicated dir>         # fresh configstore: no GA4 usage opt-in, no Google login
```

### Results

| Command | Result |
|---|---|
| `firebase projects:list --json` | success: `smoke-app`, with the right `projectNumber`, and `resources.hostingSite` = `smoke-app` |
| `firebase apps:list --project smoke-app --json` | success: the 3 genconfig apps (ANDROID, IOS, WEB), with `namespace` and `apiKeyId` |
| `firebase apps:create web "CLI Web" --project smoke-app --json --non-interactive` | success. The operation was polled, the result shows below, and the Hosting API was not called |
| `firebase apps:create android "CLI Android" --package-name com.aruma.cli --project smoke-app --json --non-interactive` | success: `packageName: com.aruma.cli`, empty `sha1Hashes`/`sha256Hashes` |
| `firebase apps:sdkconfig web <appId> --project smoke-app --json` | success: `fileName: firebase-js-config.json`, and `sdkConfig` carries the web key shared by the project |
| `firebase apps:sdkconfig android <appId> --project smoke-app --json` | success: `google-services.json`, with both Android apps of the project (base64 decoded by the CLI) |

The output of `apps:create web` (token-free, verbatim):

```json
{
  "status": "success",
  "result": {
    "@type": "type.googleapis.com/google.firebase.service.v1beta1.WebApp",
    "apiKeyId": "01a14c24-eb94-7994-b8b0-eb6fdfbdbd9f",
    "appId": "1:825230563081:web:162f7902a8e7bc62",
    "appUrls": [],
    "displayName": "CLI Web",
    "etag": "1_f7ecc67f-c661-4fa2-84a6-9ff92f94199f",
    "name": "projects/smoke-app/webApps/1:825230563081:web:162f7902a8e7bc62",
    "projectId": "smoke-app",
    "state": "ACTIVE",
    "webId": "162f7902a8e7bc62"
  }
}
```

The guard log for the six commands held 12 connections, all `ALLOW 127.0.0.1:<proxy port>`, and none was blocked.

### Exception 1: plain http sends `Bearer owner`, not the token

`apiv2.js` calls `isLocalInsecureRequest(urlPrefix)`, which is simply `protocol === "http:"`. For any http API origin, not only localhost, the CLI sends `Authorization: Bearer owner`, the emulator convention, instead of the credential.

With `FIREBASE_API_URL=http://127.0.0.1:<port>`, `projects:list` fails. Aruma answers 401 "Request is missing required authentication credential". Over https the same command succeeds.

Production Aruma sits behind TLS anyway. For local development, put a TLS proxy in front of it (Caddy `tls internal`, or mkcert) and set `NODE_EXTRA_CA_CERTS` to its CA.

### Exception 2: phone-home paths and their switches

Each path was triggered on purpose and seen blocked. Each switch was then confirmed to remove it.

| Path | Host | When | Switch |
|---|---|---|---|
| MOTD (`fetchMOTD.js`) | `firebase-public.firebaseio.com:443` (`GET /cli.json`) | every run, unless cached within the last day | `CI` set to any non-empty value |
| Update check (`update-notifier-cjs`, in a detached child process) | `registry.npmjs.org:443` | once a day | `NO_UPDATE_NOTIFIER=1` (or `CI`) |
| GA4 usage tracking (`track.js`) | `www.google-analytics.com:443` (`/mp/collect`) | only when the configstore has `"usage": true`, which the `firebase login` consent prompt sets | **no environment switch**. Use a fresh configstore (`XDG_CONFIG_HOME`), or set `"usage": false` in `~/.config/configstore/firebase-tools.json` |

In the 15.34.0 source, none of the three is gated by `FIREBASE_CLI_EXPERIMENTS` or `firebase.json`.

### Exception 3: credentials

- **`FIREBASE_TOKEN`** works, but every run prints "Authenticating with `FIREBASE_TOKEN` is deprecated and will be removed in a future major version".
- **`GOOGLE_APPLICATION_CREDENTIALS`** pointing at an Aruma key file does **not** work:
  - firebase-tools signs through gtoken 7.1.0, which hardcodes `https://www.googleapis.com/oauth2/v4/token` and ignores `token_uri`.
  - Observed: 3 blocked attempts to `www.googleapis.com:443`, then "Failed to authenticate, have you run firebase login?".
  - When `FIREBASE_TOKEN` is removed, Aruma will need another path. Candidates are a CLI wrapper, or `FIREBASE_TOKEN_URL` support for service accounts upstream.
- **Token refresh** goes to `{FIREBASE_TOKEN_URL}/oauth2/v3/token` (on Aruma) and currently gets HTTP 400, because of the server issue below. On a 400, firebase-tools falls back to sending the raw `FIREBASE_TOKEN` as the bearer, so every command still works.

### FlutterFire CLI

`flutterfire configure --project=smoke-app --platforms=android,ios,web --android-package-name=com.aruma.smoke --ios-bundle-id=com.aruma.smoke --yes` exits 0. To run it, put the local `firebase` on `PATH` and set the same environment; the CLI inherits it. It runs `firebase <command> --json` for everything. In the 1.4.1 source, `configure` makes no HTTP call of its own. It ran under `sandbox-exec`, which allows only loopback, and the guard log of its `firebase` children shows only loopback connections.

It wrote the following files:

- **`lib/firebase_options.dart`**: web, android and ios options with the right app ids, the per-platform API keys, `messagingSenderId` = project number, `projectId`, `storageBucket` and `databaseURL`; `authDomain` for web and `iosBundleId` for ios. Apart from comments, it is identical to the file Aruma's console generates (`/flutterOptions`) for the same apps. That file already passes the Flutter smoke test.
- **`android/app/google-services.json`** and **`ios/Runner/GoogleService-Info.plist`**. The plist matched the console's download exactly after `plutil` normalisation.
- **`firebase.json`**.

It also changed these files:

- The Gradle files gained `com.google.gms.google-services` 4.3.15.
- `ios/Runner.xcodeproj/project.pbxproj` was modified.
- `pubspec.yaml` was not touched, so `firebase_core` must be added separately.

Notes:

- It did not create a web app. It took the first web app the list returned, "CLI Web". Pass `--web-app-id=<appId>` to choose.
- `flutterfire --version` checks pub.dev for a newer version unless `CI` is set. `configure` does not.
- **`flutterfire reconfigure` won't work.** This comes from reading the source; it was not run. It reads the refresh token from `~/.config/configstore/firebase-tools.json` and posts to the hardcoded `https://oauth2.googleapis.com/token`. It then fetches service files from the hardcoded `https://firebase.googleapis.com`. Run `configure` again instead.

### Developer instructions (T23)

1. Create a CLI token in the Aruma console.
2. In a dedicated shell or wrapper script, export the six variables shown under [Configuration used](#configuration-used), with the https URL of the Aruma server. The dedicated `XDG_CONFIG_HOME` keeps the usage opt-in and any Google login of the normal configstore out of these runs.
3. Use `firebase projects:list`, `apps:list`, `apps:create`, `apps:sdkconfig` and `flutterfire configure` as usual.
4. Don't run `firebase login` or `flutterfire reconfigure` in that shell.

## T22: Admin SDK auth libraries with `token_uri` pointing at Aruma

**Conclusion:**

- **Go works.**
- **Node works with an exception:** `cert()` does not work. Use the custom credential in [aruma-credential.mjs](../../test/experiments/admin-node/aruma-credential.mjs).

### Go

`golang.org/x/oauth2/google` honours `token_uri`. [test/acceptance/serviceaccounts_test.go](../../test/acceptance/serviceaccounts_test.go) exchanges an Aruma key file through `google.CredentialsFromJSON(...).TokenSource.Token()` and uses the `aruma_sat_` token on `/v1beta1/projects`.

The `TestServiceAccountKeyAndToken`, `TestTokenEndpointRejectsBadAssertions` and `TestRefreshTokenGrantForFirebaseCLI` tests pass at commit 26f0c93.

### Node: `cert(keyFile).getAccessToken()`

- It made 3 connection attempts, all to `oauth2.googleapis.com:443`, and none to the key file's `token_uri`.
- The error was `ECONNREFUSED | request to https://oauth2.googleapis.com/token failed`.
- The cause is gtoken 8.0.0, which hardcodes `GOOGLE_TOKEN_URL = 'https://oauth2.googleapis.com/token'` both as the request URL and as the assertion `aud`.
- Without the guard, it would send a signed assertion (Aruma service account email, scopes) to Google. The private key itself never leaves the machine. Even so, the result can never be an Aruma token.

### Node: the supported workaround

`arumaCredential(keyFileOrJson)` returns a firebase-admin `Credential`, an object with `getAccessToken(): Promise<{access_token, expires_in}>`. It does the following:

1. It signs an RFC 7523 assertion with `node:crypto`:
   - header `{alg: RS256, typ: JWT, kid: private_key_id}`
   - claims `iss = client_email`, `scope`, `aud = token_uri`, `iat = now − 10 s`, `exp = iat + 3600`
2. It POSTs `grant_type=urn:ietf:params:oauth:grant-type:jwt-bearer&assertion=…` as `application/x-www-form-urlencoded` to the key file's `token_uri`.
3. If the exchange fails, it throws with the HTTP status and the OAuth error.

It has no dependencies.

```js
import { initializeApp } from 'firebase-admin/app';
import { arumaCredential } from './aruma-credential.mjs';

const app = initializeApp({ credential: arumaCredential('service-account.json'), projectId: 'my-app' });
const { access_token } = await app.options.credential.getAccessToken();
```

Observed against the live server:

| Check | Result |
|---|---|
| Token | an `aruma_sat_…` token, with `expires_in` between 3000 and 3600 |
| Token on the Management API | `GET /v1beta1/projects` returned 200 with `["smoke-app"]` |
| Guard | recorded no non-loopback attempt |
| Assertion signed with a different RSA key | HTTP 400 `invalid_grant`, surfaced as an error |

T22 covers the token only. Whether each Admin SDK service (Auth, Firestore, Messaging, project management) can be pointed at Aruma is a separate question. For example, project management hardcodes `firebase.googleapis.com`; see [management-api.md](management-api.md).

### Developer instructions (T22)

- **Go:** use the key file as is, with `option.WithCredentialsFile` or `google.CredentialsFromJSON`.
- **Node:** copy `aruma-credential.mjs` into the project and pass `credential: arumaCredential(<key file>)` to `initializeApp`. Don't use `cert()` or `applicationDefault()` with an Aruma key. `applicationDefault()` goes through the same google-auth-library path; this follows from the source and was not run.

## Server issue found

**The `/oauth2/v3/token` refresh grant ignores `multipart/form-data` bodies.** firebase-tools 15.34.0 (`auth.js`, `refreshTokens`) builds the body with the `form-data` package. It sends `refresh_token`, `client_id`, `client_secret`, `grant_type` and `scope` as `multipart/form-data`. The handler uses `r.ParseForm()`, which reads only urlencoded bodies, so it sees no `grant_type`.

```text
POST /oauth2/v3/token   Content-Type: multipart/form-data; boundary=…
  grant_type=refresh_token, refresh_token=aruma_pat_…, client_id=…, client_secret=…, scope=
→ 400 {"error":"unsupported_grant_type","error_description":"Use refresh_token."}

POST /oauth2/v3/token   Content-Type: application/x-www-form-urlencoded   (same fields)
→ 200 {"access_token":"aruma_pat_…","expires_in":3600,"token_type":"Bearer"}
```

- **Expected:** the multipart request gets the same 200.
- **Impact today:** none visible, because on a 400 the CLI falls back to the raw token. The refresh grant itself never succeeds for the real CLI.
- **Coverage:** `TestRefreshTokenGrantForFirebaseCLI` only posts urlencoded bodies.
- **Reproduction:** `firebase-cli.sh` prints both cases.

## R5: firebase-tools against the Realtime Database

Run on 2026-10-21 with firebase-tools 15.34.0, by `test/experiments/firebase-cli-rtdb.sh` (39 checks). Aruma ran in production form: `genconfig -rtdb-domain rtdb.aruma.test -rtdb-tls ...`, so the database is `https://smoke-app-default-rtdb.rtdb.aruma.test` behind a TLS front, as behind Caddy. Inside the CLI's Node process, `hosts.cjs` resolves `*.aruma.test` to that front. The netguard and `sandbox-exec` setup is the same as T23. A second https front, [api-front.mjs](../../test/experiments/firebase-cli-rtdb/api-front.mjs), serves the management APIs and logs each request. The CLI ran with `DEBUG=true`, so its `firebase-debug.log` shows every request it sent.

**Conclusion: every command works, and all 39 checks pass with no connection leaving loopback.** The first run found two management answers missing, without which the CLI cannot find the database (below); Aruma now gives both, and the run was repeated against it.

### How firebase-tools finds the database (source, 15.34.0)

1. Without `--instance`, `requireDatabaseInstance` reads `resources.realtimeDatabaseInstance` from `GET /v1beta1/projects/{p}` (`FIREBASE_API_URL`). `deploy --only database` always does this, because `deploy` has no `--instance` flag.
2. Every command, with or without `--instance`, then calls `populateInstanceDetails`. That is `GET {FIREBASE_RTDB_MANAGEMENT_URL}/v1beta/projects/{p}/locations/-/instances/{instance}`, and its `databaseUrl` becomes the origin. Setting `FIREBASE_REALTIME_URL` overrides only the origin and does not skip this call. Only `FIREBASE_DATABASE_EMULATOR_HOST` makes a failure harmless, and it switches to emulator mode with `Bearer owner` over http.
3. `getDatabaseUrl` keeps the host when the host already contains the instance name. Otherwise it adds a subdomain (for `firebaseio`/`firebasedatabase` hosts) or `?ns=`. `database:get` and `database:remove` send only the URL's path, so a `?ns=` would be lost. **The subdomain form is the one that works**, and it is the form Aruma's `databaseUrl` already has.

### Configuration used

```sh
export FIREBASE_API_URL=https://<aruma>
export FIREBASE_TOKEN_URL=https://<aruma>
export FIREBASE_RTDB_MANAGEMENT_URL=https://<aruma>   # the instance lookup
export FIREBASE_TOKEN=aruma_pat_...
export FIREBASE_SKIP_INFORMATIONAL_IAM=1             # else testIamPermissions goes to cloudresourcemanager.googleapis.com
export CI=true NO_UPDATE_NOTIFIER=1
```

No `FIREBASE_REALTIME_URL` is needed. Without `FIREBASE_SKIP_INFORMATIONAL_IAM`, each command tries `cloudresourcemanager.googleapis.com:443` (blocked in the run) and still succeeds, because the CLI ignores the result.

### Results

| Command | Request the CLI sent | Result |
|---|---|---|
| `database:set /smoke smoke.json --force` (default instance) | `PUT https://smoke-app-default-rtdb.rtdb.aruma.test/smoke.json` | stored, `.priority` included |
| `database:set /smoke/flag --data true --instance smoke-app-default-rtdb --force` | `PUT …/smoke/flag.json` | stored |
| `database:get /smoke` | `GET …/smoke.json` | the node, without priorities |
| `database:get --shallow` | `?shallow=true` | every child `true`, leaves included (as the emulator does) |
| `database:get --pretty` / `--export` | `?print=pretty` / `?format=export` | indented / `.priority` included |
| `database:get /smoke/scores --order-by score --limit-to-first 2`, no index | `?limitToFirst=2&orderBy=%22score%22` | exit 1: `HTTP Error: 400, Index not defined, add ".indexOn": "score", for path "/smoke/scores", to the rules` |
| `--order-by-key --limit-to-last 1`; after the deploy, `--order-by score --limit-to-first 2` and `--equal-to 30` | as given | `carol`; `bob,carol`; `alice` |
| `deploy --only database --dry-run`, rules with a syntax error | `PUT …/.settings/rules.json?dryRun=true` → 400 | exit 1: `Syntax error in database rules: 1:29: Unexpected end of expression`; rules unchanged |
| `deploy --only database --dry-run` | one `PUT ?dryRun=true` only | success; `GET /.settings/rules.json` unchanged |
| `deploy --only database` | `PUT ?dryRun=true`, then `PUT` | success; `GET /.settings/rules.json` returns the file byte for byte, comment included; `?format=strict` has the index |
| `database:update /smoke --data '{"n":2,"nested/y":"z"}' --force` | `PATCH …/smoke.json` | merged |
| `database:push /smoke/list --data …` (twice) | `POST …/smoke/list.json` | push ids, stored in push order |
| `database:remove /smoke --force` | `PATCH …/smoke.json?print=silent&writeSizeLimit=tiny&disableTriggers=false` (body `null`) | success; `/smoke` is null; rules kept |

The guard log recorded only `ALLOW 127.0.0.1:<rtdb front>` (18) and `ALLOW 127.0.0.1:<api front>` (36).

`database:remove` lists children (`GET ?shallow=true&limitToFirst=N`) only when the first PATCH fails with 400. Aruma accepts every `writeSizeLimit`, so the CLI never takes that path. The script sends that listing request directly, and it returns the first keys in order.

### The two management answers (fixed in R5)

Before Aruma gave them, the CLI stopped with:

```text
database:set /smoke … --project smoke-app            (exit 1)
  It looks like you haven't created a Realtime Database instance in this project before.
  Please run firebase init database to create your default Realtime Database instance.
database:set /smoke/flag … --instance smoke-app-default-rtdb   (exit 2)
  GET /v1beta/projects/smoke-app/locations/-/instances/smoke-app-default-rtdb → 503 text/html
  Failed to get instance details for instance: smoke-app-default-rtdb.
deploy --only database [--dry-run]                   (exit 1)  the first message
```

1. **`GET /v1beta1/projects/{p}`** now returns `resources.realtimeDatabaseInstance: "<project_id>-default-rtdb"` when RTDB is enabled (`internal/admin/mgmt/resources.go`).
2. **`GET /v1beta/projects/{p}/locations/{loc}/instances/{id}`**, the Realtime Database Management API, is now served (`internal/admin/mgmt/rtdb.go`, with the list beside it). Before, `/v1beta/` fell through to the dashboard handler. The CLI needs a JSON body with:
   - `name`: `projects/{number}/locations/{loc}/instances/{id}`, matching `projects/([^/]+?)/locations/([^/]+?)/instances/([^/]*)`
   - `project`
   - `databaseUrl`: the project's `DatabaseURL`
   - `type`: `DEFAULT_DATABASE`
   - `state`: `ACTIVE`

   Other ids and locations answer 404 as a `google.rpc.Status`. Authentication is the Management API's (CLI token, service-account token). `TestRTDBManagementInstances` covers both answers.
