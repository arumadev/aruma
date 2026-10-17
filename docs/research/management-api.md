# Firebase Management API v1beta1 and the firebase CLI

Research notes behind ADR-007, ADR-008 and the Management API. Gathered 2026-10-17 from the public discovery document (`https://firebase.googleapis.com/$discovery/rest?version=v1beta1`, revision 20261009), the [REST reference](https://firebase.google.com/docs/reference/firebase-management/rest), and the public firebase-tools and flutterfire_cli sources.

## Resource shapes

- **FirebaseProject**: `name` (`projects/{projectId}`), `projectId`, `projectNumber` (a string), `displayName`, `resources{hostingSite, realtimeDatabaseInstance, storageBucket, locationId}`, `state` (`ACTIVE|DELETED`), `etag`, `annotations`.
  - Responses always use the projectId in `name`, even when the request used the number.
- **WebApp**: `name`, `appId`, `displayName`, `projectId`, `appUrls[]`, `webId`, `apiKeyId`, `state`, `expireTime`, `etag`.
- **IosApp**: the WebApp fields above (except `appUrls` and `webId`) plus `bundleId` (immutable), `appStoreId`, `teamId`.
- **AndroidApp**: the same plus `packageName` (immutable), `sha1Hashes[]`, `sha256Hashes[]`.
- **WebAppConfig**: `projectId`, `appId`, `apiKey`, `authDomain`, `databaseURL`, `storageBucket`, `locationId`, `messagingSenderId`, `measurementId`, `projectNumber`, `version`.
- **IosAppConfig / AndroidAppConfig**: `{configFilename, configFileContents}`, where the contents are standard base64.
- **ShaCertificate**: `{name: projects/{p}/androidApps/{app}/sha/{id}, shaHash, certType: SHA_1|SHA_256}`.
- **FirebaseAppInfo** (searchApps): `name`, `displayName`, `platform` (`IOS|ANDROID|WEB`), `appId`, `namespace` (package, bundle or webId), `apiKeyId`, `state`, `expireTime`.
- **Operation**: `{name, done, response{"@type": "type.googleapis.com/google.firebase.service.v1beta1.WebApp", ...}, error}`. Real operation names contain slashes.
- **List wrappers**:
  - `projects.list` returns `{results[], nextPageToken}`.
  - `availableProjects` returns `{projectInfo[]}`.
  - Apps and searchApps return `{apps[]}`.
  - SHA certificates return `{certificates[]}`.

## Semantics

- Create methods return an Operation. `remove` takes `{validateOnly, allowMissing, immediate, etag}`. Without `immediate`, it moves the app to DELETED with `expireTime` 30 days ahead, and the app can be undeleted until then.
- `projects/-/{x}Apps/{appId}` is valid for get, getConfig, remove and undelete. firebase-tools always calls getConfig this way.
- Errors follow [AIP-193](https://google.aip.dev/193): `{"error":{"code":<http>,"message":...,"status":"NOT_FOUND","details":[...]}}`. firebase-tools reads only `error.message` and the HTTP status.

## firebase-tools

The notes below come from [src/api.ts](https://github.com/firebase/firebase-tools/blob/main/src/api.ts), [src/auth.ts](https://github.com/firebase/firebase-tools/blob/main/src/auth.ts) and [operation-poller.ts](https://github.com/firebase/firebase-tools/blob/main/src/operation-poller.ts).

- `FIREBASE_API_URL` replaces `https://firebase.googleapis.com`. Operation polling uses the same origin.
- `FIREBASE_TOKEN_URL` replaces the token origin (`{origin}/oauth2/v3/token`).
- `FIREBASE_TOKEN` is sent as a refresh token there. If the response is 400 or 401, the CLI uses the raw value as the bearer token. **Pointing `FIREBASE_TOKEN_URL` at Aruma is required** so the token never goes to Google.
- Commands and the calls they make:

| Command | Calls |
|---|---|
| `projects:list` | `GET /v1beta1/projects?pageSize=1000` (reads `results` and `nextPageToken`) |
| `apps:list` | `GET /v1beta1/projects/{p}:searchApps?pageSize=100`, or a per-platform list |
| `apps:sdkconfig` | `GET /v1beta1/projects/-/{x}Apps/{appId}/config`; base64-decodes the contents for iOS and Android |
| `apps:create` | `POST /v1beta1/projects/{p}/{x}Apps`, then polls `GET /v1beta1/{operation.name}` until `done` (30 s timeout) |

- For web, `apps:create` first reads `resources.hostingSite` from `projects.get`. If it is empty, it calls the Hosting API. **Aruma always fills `hostingSite`.**
- FlutterFire CLI shells out to `firebase ... --json` and inherits the environment. It needs non-null `projectId`, `projectNumber`, `name` and `state` on projects. [firebase.dart](https://github.com/invertase/flutterfire_cli/blob/main/packages/flutterfire_cli/lib/src/firebase.dart)

## Admin SDK project management

- firebase-admin-node hardcodes `firebase.googleapis.com`, uses `v1` paths and snake_case query parameters. It cannot be redirected without DNS interception, so it is out of scope.

## Routing

- Go's `ServeMux` wildcards must be whole segments. `{project}:searchApps` is an invalid pattern ([net/http patterns](https://pkg.go.dev/net/http#hdr-Patterns)).
- Aruma therefore uses a small router that splits a trailing `:verb` off the last segment.
