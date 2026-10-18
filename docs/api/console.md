# Console API (`/console/v1/`)

The private JSON API behind the dashboard. It is not a public contract: it changes with the dashboard. The Management API (`/v1beta1/`) is the stable, Firebase-compatible surface.

## Conventions

- **JSON:** request and response bodies are JSON, camelCase. Unknown request fields are rejected (`INVALID_ARGUMENT`). Bodies are capped at 1 MiB (413).
- **Errors:** every error is a `google.rpc.Status`:
  ```json
  {"error": {"code": 404, "message": "Requested entity was not found.", "status": "NOT_FOUND",
             "details": [{"@type": "type.googleapis.com/google.rpc.BadRequest",
                          "fieldViolations": [{"field": "projectId", "description": "..."}]}]}}
  ```
  Validation errors carry `BadRequest` field violations. Anything the caller does not own is `NOT_FOUND`.
- **Authentication:**
  - **Session.** The cookie `__Host-aruma_session` (`aruma_session` on plain-http non-loopback hosts) is set by setup, login and signup. Every request that changes state must send `X-Aruma-CSRF: <csrfToken>`. The token comes from the login, setup and signup responses and from `GET /me`. Cross-origin browser writes are refused.
  - **Bearer.** `Authorization: Bearer aruma_pat_…` uses a personal CLI token. It needs no CSRF header. Routes marked *session only* refuse it.
- **Pagination:** `pageSize` (default 100, max 1000) and `pageToken`. Responses carry `nextPageToken`, which is empty on the last page. `showDeleted=true` includes soft-deleted items.
- **Concurrency:** resources carry an `etag`. Send it back on `PATCH` to reject stale writes with `ABORTED` (409). Omitted, the write is unconditional.
- **Times:** RFC 3339 UTC. Project numbers are strings.

## Bootstrap and authentication

| Method | Path | Auth | Body → Response |
|---|---|---|---|
| GET | `/status` | public | → `{setupRequired, signupOpen, version, limits: {servers, maxServiceAccountKeys, deletedRetentionDays}}` |
| POST | `/setup` | public | `{setupCode, email, password, displayName?, locale?}` → `Login`. Only while no account exists; the code is printed in the server log |
| POST | `/auth:login` | public | `{email, password}` → `Login`. One message for every failure; 429 with `Retry-After` when throttled |
| POST | `/auth:signup` | public | `{email, password, displayName?, locale?}` → `Login`. `PERMISSION_DENIED` unless the operator opened sign-up |
| POST | `/auth:logout` | session only | → 204, clears the cookie |

`Login` = `{account: Account, csrfToken, expireTime}`, plus the session cookie.

`Account` = `{id, email, displayName, locale: "ar"|"en", createTime}`.

## The signed-in account

| Method | Path | Auth | Body → Response |
|---|---|---|---|
| GET | `/me` | any | → `{account, csrfToken?}`. `csrfToken` is present for sessions |
| PATCH | `/me` | session only | `{displayName?, locale?}` → `{account}` |
| POST | `/me:changePassword` | session only | `{currentPassword, newPassword}` → 204. Signs out every other session and revokes every CLI token. Wrong current passwords are throttled (429) |
| GET | `/me/sessions` | session only | → `{sessions: [{id, current, ip, userAgent, createTime, lastSeenTime, expireTime}]}` |
| DELETE | `/me/sessions/{id}` | session only | → 204 |
| GET | `/me/tokens` | session only | → `{tokens: [Token]}` |
| POST | `/me/tokens` | session only | `{name, currentPassword, expireDays?}` (0 = no expiry, max 366) → `Token`. Needs the current password, since a token outlives the session; throttled like change-password. Only here does `token` hold the plaintext |
| DELETE | `/me/tokens/{id}` | session only | → 204. The token stops working at once |

`Token` = `{id, name, prefix, createTime, lastUsedTime?, expireTime?, token?}`.

## Projects

| Method | Path | Body → Response |
|---|---|---|
| GET | `/projects` | → `{projects: [Project], nextPageToken}` |
| POST | `/projects` | `{projectId, displayName}` → `Project` |
| GET | `/projects:checkId?projectId=` | → `{projectId, valid, available}` |
| GET | `/projects:suggestId?displayName=` | → `{projectId}`. An available id derived from the name |
| GET | `/projects/{project}` | → `Project`. `{project}` is the id or the number; deleted projects are included |
| PATCH | `/projects/{project}` | `{displayName?, authDomain?, etag?}` → `Project` |
| POST | `/projects/{project}:delete` | `{confirmProjectId}` → `Project` (state `DELETED`, `expireTime` = +30 days) |
| POST | `/projects/{project}:restore` | → `Project`. Within 30 days |
| GET | `/projects/{project}/auditLog` | → `{entries: [{id, time, actorType, actorId, action, target, result, ip, details}], nextPageToken}`, newest first |

`Project` = `{name, projectId, projectNumber, displayName, state: "ACTIVE"|"DELETED", etag, authDomain, storageBucket, createTime, updateTime, deleteTime?, expireTime?}`.

Project ids are 6–30 characters of `a-z0-9-`. They start with a letter, don't end with `-`, never change, and are never reused.

## Apps and config files

Every path below starts with `/projects/{project}`.

| Method | Path | Body → Response |
|---|---|---|
| GET | `/apps?platform=` | → `{apps: [App], nextPageToken}` |
| POST | `/apps` | `{platform: "WEB"\|"IOS"\|"ANDROID", displayName?, packageName? (Android), bundleId? (iOS)}` → `App` |
| GET | `/apps/{app}` | → `App` |
| PATCH | `/apps/{app}` | `{displayName?, etag?}` → `App`. Changing `packageName` or `bundleId` is `INVALID_ARGUMENT` |
| POST | `/apps/{app}:remove` | `{immediate?, etag?}` → `App`. Soft delete unless `immediate` |
| POST | `/apps/{app}:undelete` | → `App` |
| GET | `/apps/{app}/config` | → the file as a download: `firebase-config.json`, `google-services.json` (all Android apps of the project) or `GoogleService-Info.plist` |
| GET | `/apps/{app}/config?format=snippet` | → `const firebaseConfig = {…};` (web apps only) |
| GET | `/apps/{app}/sha` | → `{certificates: [{name, id, shaHash, certType}]}` (Android only) |
| POST | `/apps/{app}/sha` | `{shaHash}` → certificate. Hex with or without colons; the length decides SHA-1 or SHA-256 |
| DELETE | `/apps/{app}/sha/{sha}` | → 204 |
| GET | `/flutterOptions?web=&android=&ios=&macos=` | → `firebase_options.dart` built from the chosen app ids (at least one) |
| GET | `/adminSdkConfig` | → `{projectId, databaseURL, storageBucket}` |

`App` = `{name, appId, platform, displayName, packageName?, bundleId?, apiKeyId, state, etag, createTime, updateTime, deleteTime?, expireTime?}`.

## API keys

Every path below starts with `/projects/{project}`. Keys are not secrets. Each platform gets one key, created with its first app.

| Method | Path | Body → Response |
|---|---|---|
| GET | `/apiKeys` | → `{keys: [Key]}` |
| POST | `/apiKeys` | `{displayName?, restrictions?}` → `Key` |
| GET | `/apiKeys/{key}` | → `Key` |
| PATCH | `/apiKeys/{key}` | `{displayName?, restrictions?, etag?}` → `Key` |
| POST | `/apiKeys/{key}:rotate` | → the new `Key`. It takes over the platform and its apps; the old key works until disabled |
| POST | `/apiKeys/{key}:disable` | → `Key` |
| POST | `/apiKeys/{key}:enable` | → `Key` |

`Key` = `{name, uid, keyString, displayName, restrictions, state: "ACTIVE"|"DISABLED", platform?, rotatedFrom?, etag, createTime, updateTime}`.

`restrictions` contains at most one of the following:
- `browserKeyRestrictions.allowedReferrers[]`
- `androidKeyRestrictions.allowedApplications[{sha1Fingerprint, packageName}]`
- `iosKeyRestrictions.allowedBundleIds[]`

It may also carry `apiTargets[{service}]`.

## Service accounts

Every path below starts with `/projects/{project}`.

| Method | Path | Body → Response |
|---|---|---|
| GET | `/serviceAccounts` | → `{serviceAccounts: [{name, id, email, uniqueId, displayName, disabled, createTime}]}` |
| POST | `/serviceAccounts` | `{accountId, displayName?}` → service account. Email: `<accountId>@<projectId>.sa.<domain>` |
| DELETE | `/serviceAccounts/{sa}` | → 204. Its keys and tokens stop working |
| GET | `/serviceAccounts/{sa}/keys` | → `{keys: [{keyId, keyAlgorithm, validAfterTime, validBeforeTime?, disabled, createTime}]}` |
| POST | `/serviceAccounts/{sa}/keys` | → the key file as a download. **Shown once; Aruma does not keep the private key.** At most 10 keys |
| DELETE | `/serviceAccounts/{sa}/keys/{keyId}` | → 204 |

## Auth: users, sign-in settings and signing keys

Every path below starts with `/projects/{project}/auth`. They act on Aruma Auth for a project the caller owns. Auth errors keep the Identity Toolkit code at the start of the message (`WEAK_PASSWORD : Password should be at least 6 characters`, `CLAIMS_TOO_LARGE`, `FORBIDDEN_CLAIM : sub`), inside the usual `google.rpc.Status`: `USER_NOT_FOUND` is `NOT_FOUND`, `EMAIL_EXISTS` and `DUPLICATE_LOCAL_ID` are `ALREADY_EXISTS`, and the rest are `INVALID_ARGUMENT`.

| Method | Path | Body → Response |
|---|---|---|
| GET | `/users?pageSize=&pageToken=&query=` | → `{users: [User], nextPageToken, totalSize}`. Ordered by uid. `query` matches an email prefix (case-insensitive) or an exact uid; `totalSize` counts the matches |
| POST | `/users` | `{uid?, email?, password?, displayName?, emailVerified?, disabled?}` → `User`. A uid is generated when omitted; the password follows the project's policy |
| GET | `/users/{uid}` | → `User` |
| PATCH | `/users/{uid}` | `{email?, password?, displayName?, photoUrl?, emailVerified?, disabled?, customClaims?}` → `User`. `customClaims` is a JSON object as a string, at most 1000 characters, without reserved names; `""` clears them. A new email or password revokes the user's sessions |
| DELETE | `/users/{uid}` | → 204 |
| POST | `/users/{uid}:revokeSessions` | → `User`. Refresh tokens and the ID tokens issued so far stop working (`tokensValidAfterTime` moves to now) |
| GET | `/config` | → `AuthConfig` |
| PATCH | `/config` | the `AuthConfig` fields to change (not `limits` or `updateTime`) → `AuthConfig`. `passwordMinLength` is 6 to 30, `passwordEnforcement` `OFF` or `ENFORCE`; `authorizedDomains` are bare host names or IP addresses (no scheme, port, path or wildcard, at most 100), stored in lower case without duplicates |
| GET | `/signingKeys` | → `{keys: [{kid, purpose: "id_token"\|"session_cookie", state: "next"\|"active"\|"retired", createTime, activateTime?, retireTime?}]}`. Public data only; empty until the project issues its first token |
| POST | `/signingKeys:rotate` | `{purpose, emergency?}` → `{keys}`. Normal: next becomes active and active retired, so issued tokens verify until they expire. Emergency: the active key is destroyed, and every token it signed fails at once |

`User` = `{uid, email?, emailVerified, displayName?, photoUrl?, disabled, providers: [{providerId, email?}], customClaims?, hasPassword, createTime, lastSignInTime?, lastRefreshTime?, tokensValidAfterTime}`. The password hash is never returned. An anonymous user has no providers and no email.

`AuthConfig` = `{passwordEnabled, anonymousEnabled, clientSignupEnabled, enumerationProtection, authorizedDomains, passwordMinLength, passwordRequireLowercase, passwordRequireUppercase, passwordRequireNumeric, passwordRequireSymbol, passwordEnforcement, appName, updateTime, limits}`. `limits` is read-only: the values no setting changes, `{idTokenLifetimeSeconds, sessionCookieMaxSeconds, signingKeyRotationDays, requestsPerMinute, requestBurst, signUpsPerMinute, signUpBurst, passwordFailures, passwordFailureWindowSeconds, passwordBackoffMaxSeconds}` (client calls and sign-ups are counted per address, password failures per project and email).
