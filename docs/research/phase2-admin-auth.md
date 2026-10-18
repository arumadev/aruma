# Phase 2 research: Admin SDK authentication

These notes support `@aruma/admin-auth` (Node), the Aruma Go admin-auth package and Aruma's custom-token sign-in. Gathered 2026-10-18.

**Sources.** Every behaviour below was read from pinned source:

- firebase-admin-node at `a2ecf2a` (npm 14.5.0, the latest release)
- firebase-admin-go at `66a72c3` (v4.22.0, the latest release)

Short names stand in for the GitHub permalinks, which are listed at the end. "Verified by experiment" means the result came from running the released packages locally ([Experiments](#experiments)).

## 1. verifyIdToken and verifySessionCookie

### Where the keys come from

Both SDKs hard-code the key URLs and the issuer prefixes:

| | ID token | Session cookie |
|---|---|---|
| Key URL (x509 PEM map, `kid → cert`) | `https://www.googleapis.com/robot/v1/metadata/x509/securetoken@system.gserviceaccount.com` | `https://www.googleapis.com/identitytoolkit/v3/relyingparty/publicKeys` |
| `iss` | `https://securetoken.google.com/<projectId>` | `https://session.firebase.google.com/<projectId>` |

Sources: [N-tv] L271–278 and L611–650; [G-tv] L42–45.

- **Node caching.** Node's `UrlKeyFetcher` caches the keys until `Cache-Control: max-age` runs out. Without that header, the keys expire immediately and every call refetches them ([N-jwt] L101–180).
- **Go caching.** Go does the same with `findMaxAge`, and its default is also 0 s ([G-tv] L476–491).
- **Live headers.** On 2026-10-18 the ID-token URL sent `max-age≈23863, must-revalidate` and the session-cookie URL sent `max-age=86400` (verified by `curl`).
- **JWKS.** The OIDC document at `https://securetoken.google.com/<p>/.well-known/openid-configuration` points to a JWKS at `…/service_accounts/v1/jwk/securetoken@…`, which is the same keys in JWK form (verified by `curl`). Neither Admin SDK uses it for ID tokens. Node's `JwksFetcher` exists only for other products ([N-jwt] L52).

### Exact checks, in order

| Check | Node 14.5 ([N-tv] L506–566, [N-jwt]) | Go 4.22 ([G-tv] L156–308) |
|---|---|---|
| `kid` present | required. Without it, Node gives a special message if `aud` is the custom-token audience | required, with the same special case |
| header `alg` | must be `RS256`. The verifier library also allows ES256, but the header check runs first | must be `RS256` |
| `aud` | `=== projectId` | `== projectID` |
| `iss` | `=== prefix + projectId` | `== prefix + projectID` |
| `sub` | a non-empty string of at most 128 characters. Becomes `uid` | non-empty, at most 128 characters. Becomes `UID` |
| `exp` | checked by `jsonwebtoken` with **no clock tolerance**: expired at `now >= exp` | `exp + 300 < now` means expired (**300 s skew**) |
| `iat` | **not checked**. A token with `iat` one hour in the future was accepted (verified by experiment) | `iat − 300 > now` is rejected as "issued at future timestamp" |
| `nbf` | enforced if present (by `jsonwebtoken`), and reported as "invalid signature" | ignored |
| `auth_time` | **not checked**. A token without it was accepted (verified by experiment) | not checked. The field is decoded into `Token.AuthTime` |
| signature | key looked up by `kid`. An unknown `kid` gives the "kid … does not correspond to a known public key" error | a key with the matching `kid` must verify |

Error mapping:

- **Node.** An expired token gives `auth/id-token-expired` or `auth/session-cookie-expired`. Every other failure gives `auth/argument-error`, including key-fetch failures ([N-tv] L583–601). Node never raises `auth/invalid-id-token` from the verifier (verified by experiment).
- **Go.** The returned `*internal.FirebaseError` has `ErrorCode=INVALID_ARGUMENT` and `Ext["authErrorCode"]` set to `ID_TOKEN_INVALID`, `ID_TOKEN_EXPIRED`, `SESSION_COOKIE_INVALID` or `SESSION_COOKIE_EXPIRED`. A key-fetch failure gives `CERTIFICATE_FETCH_FAILED` with `ErrorCode=UNKNOWN` ([G-tv] L47–90, L186–248).
- **Go decoded token.** `Token.Claims` holds every claim except `iss/aud/exp/iat/sub/uid` ([G-tv] L297–305).

### checkRevoked

**Node** ([N-base] L217–230, L1124–1148):

1. Runs `getUser(sub)`.
2. If `disabled`, throws `auth/user-disabled`.
3. If `auth_time*1000 < Date.parse(tokensValidAfterTime)`, throws `auth/id-token-revoked` (or `auth/session-cookie-revoked` for cookies).

**Go** ([G-auth] L322–350, L423–448):

1. Runs `GetUser`.
2. If disabled, returns `USER_DISABLED`.
3. If **`IssuedAt*1000 < TokensValidAfterMillis`**, returns `ID_TOKEN_REVOKED` or `SESSION_COOKIE_REVOKED`.

Go compares `iat` while Node compares `auth_time`. The two agree only if Aruma refuses to mint new ID tokens from refresh tokens issued before `validSince`.

`validSince` has 1-second resolution and the comparison is strict `<`, so a sign-in in the same second as the revocation counts as still valid.

### Emulator mode (`FIREBASE_AUTH_EMULATOR_HOST`)

**Node** ([N-base] L156–169, [N-jwt] L247–252, [N-req] L232–247, L2385–2395). Verified by experiment:

- The env var is read when the `Auth` object is constructed.
- The `kid` and `alg` checks are skipped. `aud`, `iss`, `sub` and `exp` are still enforced against the **Google** issuer.
- The signature check calls `jwt.verify` with `algorithms:['none']` and no key. **Unsigned `alg:none` tokens are accepted.** A properly signed RS256 token is *rejected* ("invalid signature").
- Revocation checking is **forced on**: `getUser` goes to `http://<host>/identitytoolkit.googleapis.com/v1/projects/<p>/accounts:lookup` with `Authorization: Bearer owner`. A disabled user still gives `auth/user-disabled`.
- `createCustomToken` emits `{"alg":"none","typ":"JWT"}` with `iss/sub = firebase-auth-emulator@example.com` and an empty signature.

**Go** ([G-auth] L76–80, L124–146, L342; [G-tv] L172–175). Verified by experiment:

- Header checks are skipped and timestamps are still checked.
- **Signature verification is skipped entirely.** `alg:none`, RS256 signed by an unknown key and HS256 with a garbage signature were all accepted.
- `GetUser` is then forced against the emulator with `Bearer owner`.

**Consequence.** Setting the variable in production turns ID-token verification into "trust any token whose `sub` exists in whatever server the variable points at". The official SDKs also send the literal admin token `owner` to that server.

### Can the official verifier be re-targeted without forking?

**No supported way exists.**

- **Node.** `FirebaseTokenVerifier`, `createIdTokenVerifier` and the key fetchers are `@internal`. The package `exports` map blocks deep imports: `require('firebase-admin/lib/auth/token-verifier')` fails with `ERR_PACKAGE_PATH_NOT_EXPORTED` (verified by experiment). `TenantAwareAuth` only adds a tenant check. App options give no issuer or key-URL override.
- **Go.** `tokenVerifier` and the `baseClient` endpoint fields are unexported, and only the emulator variable changes them ([G-auth] L69–171).
- **A runtime monkeypatch does work in Node 14.5.** Overwriting the private fields `auth.idTokenVerifier.issuer` and `.signatureVerifier.keyFetcher` made `verifyIdToken` accept Aruma-signed tokens, which produced the Node column above (verified by experiment). It depends on private field names, so it is **not recommended** beyond test fixtures.

### Reusing the official types and errors

**Node, firebase-admin ≥ 14.0.0.**

- `firebase-admin/auth` exports `FirebaseAuthError` with a public `constructor(info: ErrorInfo, message?)`. It also exports `AuthErrorCode`, a const map of **unprefixed** codes such as `ID_TOKEN_EXPIRED: 'id-token-expired'` ([N-err] L24–125, L652–700).
- `new FirebaseAuthError({code: AuthErrorCode.ID_TOKEN_EXPIRED, message})` gives `code === 'auth/id-token-expired'`, `instanceof FirebaseAuthError === true` and `hasCode('id-token-expired') === true`. This type-checks under `tsc --strict` (verified by experiment). I checked 14.0.0, 14.3.0 and 14.5.0.
- In 13.x (13.10.0 checked), the export is `AuthClientErrorCode` and the `FirebaseAuthError` constructor is not declared publicly.
- `DecodedIdToken` is a plain interface.
- `UserRecord` is an exported class with an `@internal` constructor taking the raw `GetAccountInfo` user JSON ([N-ur] L591–640). `new UserRecord(json)` works at runtime and gives `instanceof UserRecord` (verified by experiment). TypeScript rejects it with TS2554 "Expected 0 arguments" (verified by experiment), so the library needs `Reflect.construct(UserRecord, [json])` behind a typed wrapper.

**Go.**

- `auth.Token`, `auth.UserRecord`, `auth.ExportedUserRecord`, `auth.UserInfo`, `auth.UserMetadata`, `GetUsersResult` and `DeleteUsersResult` are structs with exported fields. A separate package can build them directly.
- `auth.UserIterator` has only unexported fields ([G-exp] L51–57), so the package needs its own iterator that yields `*auth.ExportedUserRecord`.
- The error helpers do `err.(*internal.FirebaseError)` and compare `Ext["authErrorCode"]` ([G-auth] L450–458). `errorutils.Is*` uses `HasPlatformErrorCode` with a direct type assertion as well ([G-ierr] L87–102). The `internal` package cannot be imported from another module.
- **Workaround, verified by experiment:**
  1. Capture the concrete type from an error that the SDK produces offline, such as `client.VerifyIDToken(ctx, "")`.
  2. Build values with `reflect.New(t.Elem())`, setting the exported fields `ErrorCode`, `String` and `Ext`.
  3. `auth.IsIDTokenExpired`, `IsIDTokenInvalid`, `IsUserDisabled`, `IsIDTokenRevoked` and `IsUserNotFound` then all return true.
- Because the type assertion is direct, an error wrapped with `fmt.Errorf("%w")` is **not** recognised (verified by experiment). Errors must be returned unwrapped.
- A cheaper trick also works offline: Go checks expiry before the signature, so an unsigned token with the Google `iss`, the right `aud` and a past `exp` makes the official client itself return a real `ID_TOKEN_EXPIRED` error (verified by experiment). This only covers expiry and invalid-token errors.

## 2. createCustomToken

**Node** ([N-tg] L124–176, [N-cs] L60–90, L203–210):

- **Header:** `{"alg":"RS256","typ":"JWT"}` with **no `kid`**.
- **Payload:**
  - `aud = https://identitytoolkit.googleapis.com/google.identity.identitytoolkit.v1.IdentityToolkit`
  - `iat = floor(now)` and `exp = iat + 3600`
  - `iss = sub = client_email`
  - `uid`
  - `claims` (only if non-empty)
  - `tenant_id` (only for a `TenantAwareAuth`)
- **Signing.** With a `cert()` credential (`ServiceAccountCredential`), Node signs locally with `RSA-SHA256` and the key file's `private_key`. With any other credential it uses the IAM `signBlob` API at `iamcredentials.googleapis.com`.
- **Validation.** `uid` must be 1–128 characters. The forbidden `claims` keys are `acr amr at_hash aud auth_time azp cnf c_hash exp iat iss jti nbf nonce`. **`sub` and `firebase` are allowed** in Node's custom-token list (verified by experiment), although both are in `RESERVED_CLAIMS` for `setCustomUserClaims` ([N-req] L57–60).
- **Verified by experiment** with `cert()`: no socket connection was opened, the signature verifies with the key file's public key, and `exp − iat = 3600`.

**Go** ([G-auth] L198–235, [G-tg] L43–58, L104–152):

- **Header:** `{"alg":"RS256","typ":"JWT"}` with no `kid`. `jwtHeader.KeyID` exists but is never set.
- **Payload:** the same claims (`iss, aud, exp, iat, sub, uid, tenant_id, claims`).
- **Signing.** Signs locally with `rsa.SignPKCS1v15` when the credentials JSON contains `private_key` and `client_email`. Otherwise it uses IAM.
- **Validation.** The reserved list also includes **`firebase` and `sub`**.
- **Verified by experiment:** the signature checks out and no traffic reached `token_uri`.

**What Aruma's `signInWithCustomToken` must accept:**

- RS256 with no `kid`. Find the service account by `iss` (equal to `sub`) and try all of that account's active public keys.
- The exact `aud` above, `exp − iat ≤ 3600`, `uid` of 1–128 characters, `claims` as an object, and an optional `tenant_id`.
- **Reject `alg:none`**, which is what emulator-mode SDKs produce.
- Reject or strip `claims.firebase` and `claims.sub`, because Node lets them through.
- IAM-signed tokens use Google-held keys that Aruma cannot verify, so they are out of scope.

## 3. User-management REST surface

- **Base URL.** `https://identitytoolkit.googleapis.com/v1/projects/{p}` (`/tenants/{t}` for tenants) is hard-coded in both SDKs.
- **Only override.** The emulator variable, which switches to `http://{host}/identitytoolkit.googleapis.com/v1/…` ([N-req] L92–105, [G-auth] L36, L144–146).
- **Proxies.** Node's `httpAgent` option and Go's `option.WithHTTPClient` can carry traffic, but do not change the host.

| SDK method | Call | Body and response |
|---|---|---|
| getUser / ByEmail / ByPhone | `POST /accounts:lookup` | `{localId:[…]}`, `{email:[…]}`, `{phoneNumber:[…]}` or `federatedUserId`. Empty `users` gives user-not-found |
| getUsers (≤100) | `POST /accounts:lookup` | same arrays. Missing users are worked out by the client |
| listUsers (≤1000) | `GET /accounts:batchGet?maxResults=&nextPageToken=` | `{users?, nextPageToken?}` |
| createUser | `POST /accounts` | `photoURL→photoUrl`, `uid→localId`. Response `{localId}`, then the SDK calls getUser |
| updateUser / setCustomUserClaims / revokeRefreshTokens | `POST /accounts:update` | `localId`, `deleteAttribute:["DISPLAY_NAME","PHOTO_URL"]`, `deleteProvider:["phone"]`, `customAttributes` as a **JSON string** (≤1000 characters; `{}` clears it), `validSince` = now in **seconds** (Node sends a number, Go a string) |
| deleteUser | `POST /accounts:delete` | `{localId}` |
| deleteUsers (≤1000) | `POST /accounts:batchDelete` | `{localIds, force:true}`, response `{errors:[{index, localId, message}]}` |
| generate*Link | `POST /accounts:sendOobCode` | `{requestType: PASSWORD_RESET\|VERIFY_EMAIL\|EMAIL_SIGNIN\|VERIFY_AND_CHANGE_EMAIL, email, returnOobLink:true, …ActionCodeSettings}`, response `{oobLink}` |
| createSessionCookie | `POST /projects/{p}:createSessionCookie` | `{idToken, validDuration}`, response `{sessionCookie}` |

Sources: [N-req] L583–870, L1142–1700; [G-um] L843–859, L1370–1553.

**UserRecord mapping** ([N-ur] L26–43, L388–396, L591–640; [G-um] L49, L1224–1290):

- `localId→uid` and `photoUrl→photoURL`.
- `disabled` defaults to false.
- `providerUserInfo[].rawId→providerData[].uid`.
- A `passwordHash` equal to base64("REDACTED") = `UkVEQUNURUQ=` is cleared.
- `salt→passwordSalt`.
- `customAttributes` (a JSON string) is parsed into `customClaims`.
- `validSince` (seconds) becomes `tokensValidAfterTime` (Node, a UTC string) or `TokensValidAfterMillis` (Go).
- `createdAt` and `lastLoginAt` are milliseconds since the epoch.
- `lastRefreshAt` is an RFC 3339 string.
- **Go is strict.** `createdAt`, `lastLoginAt` and `validSince` use the `,string` JSON tag, so Aruma **must send them as quoted integers**. A bare number makes Go's unmarshal fail. Node's `parseInt` accepts either form (verified by experiment for Node).

**Error mapping.**

- **Node.** Reads `error.message`, splits `"CODE : detail"`, and maps the code through `AUTH_SERVER_TO_CLIENT_CODE`. Unknown codes become `auth/internal-error` ([N-err] L518–650, L666–690; [N-req] L1068, L1988–1998). Examples:
  - `USER_NOT_FOUND`→`auth/user-not-found`
  - `EMAIL_EXISTS`/`DUPLICATE_EMAIL`→`auth/email-already-exists`
  - `DUPLICATE_LOCAL_ID`→`auth/uid-already-exists`
  - `PHONE_NUMBER_EXISTS`→`auth/phone-number-already-exists`
  - `INVALID_ID_TOKEN`→`auth/invalid-id-token`
  - `TOKEN_EXPIRED`→`auth/id-token-expired`
  - `USER_DISABLED`→`auth/user-disabled`
  - `INVALID_DURATION`→`auth/invalid-session-cookie-duration`
  - `EMAIL_NOT_FOUND`→`auth/email-not-found`
  - `WEAK_PASSWORD`→`auth/invalid-password`
  - `FORBIDDEN_CLAIM`→`auth/reserved-claim`
  - `CLAIMS_TOO_LARGE`→`auth/claims-too-large`
  - `INSUFFICIENT_PERMISSION`→`auth/insufficient-permission`
- **Go.** Has only 11 entries ([G-um] L1597–1689), for example `USER_NOT_FOUND`→(`NOT_FOUND`, `USER_NOT_FOUND`) and `EMAIL_EXISTS`→(`ALREADY_EXISTS`, `EMAIL_ALREADY_EXISTS`). Every other code falls back to the HTTP status: 400→`INVALID_ARGUMENT`, 404→`NOT_FOUND`, and so on.

## 4. Session cookies

**createSessionCookie:**

- **Node** sends `validDuration = floor(expiresIn_ms/1000)` and checks it client-side against 300..1209600 s, raising `auth/invalid-session-cookie-duration` ([N-req] L83–86, L583–606, L1142–1150).
- **Go** checks `5m ≤ expiresIn ≤ 14×24h` before sending `int64(seconds)` ([G-um] L1532–1560).
- A server `INVALID_DURATION` maps to the same Node code.

**The cookie itself:**

- It is a JWT with the ID token's claims and `iss = https://session.firebase.google.com/<projectId>`.
- It is verified with the session-cookie key URL and the same checks as an ID token.
- Expiry gives `auth/session-cookie-expired` or `SESSION_COOKIE_EXPIRED`. Revocation gives `auth/session-cookie-revoked` or `SESSION_COOKIE_REVOKED`.
- The Node source notes that its key URL "will be updated to a different URL soon" ([N-tv] L277).

## Recommended design

**Shape.** Both libraries are thin, self-contained clients for Aruma's Identity-Toolkit-compatible API, plus a JWT verifier. They return the official types, so the only line developers change is the call site (`getAuth()` → `getArumaAuth()`). They never wrap, patch or fork firebase-admin.

**API surface.** Each library mirrors `BaseAuth` / `auth.Client` names, signatures and semantics:

- `verifyIdToken(token, checkRevoked?)` and `verifySessionCookie(cookie, checkRevoked?)`
- `createSessionCookie(idToken, {expiresIn})`
- `getUser`, `getUserByEmail`, `getUserByPhoneNumber`, `getUsers`, `listUsers`
- `createUser`, `updateUser`, `deleteUser`, `deleteUsers`
- `setCustomUserClaims`, `revokeRefreshTokens`
- `generatePasswordResetLink`, `generateEmailVerificationLink`, `generateSignInWithEmailLink`
- `tenantAware(tenantId)` later

`createCustomToken` stays on the **official** SDK: it is local, and Aruma accepts it (§2).

**Node: `@aruma/admin-auth`.**

- **Dependencies.** `firebase-admin@^14` as a **peer dependency**, for the `DecodedIdToken`, `UserRecord`, `ListUsersResult`, `GetUsersResult` and `DeleteUsersResult` types and the runtime `FirebaseAuthError`, `AuthErrorCode` and `UserRecord`. Also `jose` for JWT work.
- **Errors.** Built with `new FirebaseAuthError({code: AuthErrorCode.X, message})`, keeping Node parity: expiry gives `*-expired`, all other verification failures `argument-error`.
- **UserRecord.** Built with `Reflect.construct(UserRecord, [json])`.
- **Startup self-test.** Fail loudly if either constructor stops working.

**Go package.**

- It imports `firebase.google.com/go/v4/auth` for the types.
- Errors are made by the reflection factory from §1, with the type captured once at init, and are always returned unwrapped.
- An `init` self-test checks `auth.IsIDTokenExpired(forged)`.
- CI runs against several firebase-admin-go versions.

**Credentials.**

- Load the service-account key file.
- Sign an RFC 7523 assertion: `RS256`, `kid=private_key_id`, `iss=client_email`, `aud=token_uri`, scope `…/auth/cloud-platform`.
- POST it to `token_uri` and cache the access token until `expires_in − 60 s`.
- Node must do this itself, because google-auth-library/gtoken ignores `token_uri` (`cert()` with a local `token_uri` still went to Google: verified by experiment).
- Go could reuse `golang.org/x/oauth2/google`, which honours `token_uri`. The official Go SDK posted the assertion to the local `token_uri` with `aud=token_uri` (verified by experiment).

**Discovery.**

- `origin = new URL(key.token_uri).origin`, unless an explicit `arumaUrl` option is given.
- Refuse Google origins (`*.googleapis.com`), since that means a Google key was supplied.
- Fetch `{origin}/issuers/{projectId}/.well-known/openid-configuration`, the same shape as Google's ([live doc](https://securetoken.google.com/demo-project/.well-known/openid-configuration)).
- Require the discovered `issuer` to equal the configured expectation, and take `jwks_uri` from it.
- Use a separate issuer for session cookies, for example `{origin}/session-issuers/{projectId}`, so an ID token can never pass as a cookie.
- Cache JWKS by `Cache-Control`, refetch on an unknown `kid` with a cooldown, and require HTTPS except on loopback.

**Verification rules.**

- Accept only `RS256`, with `kid` required.
- Check `aud == projectId`, exact `iss`, and `sub` of 1–128 characters.
- Use skew **0 s for `exp` in Node and 300 s in Go**, matching each official SDK, and reject a future `iat` beyond the skew in both.
- For `checkRevoked`, compare `auth_time` in Node and `iat` in Go, matching each SDK. The server must invalidate refresh tokens at `validSince` so the two agree.

**Emulator guard.**

- Throw at construction if `FIREBASE_AUTH_EMULATOR_HOST` is set, unless the caller explicitly passes `allowEmulator: true` (and never when `NODE_ENV=production`).
- Never honour `alg:none`.
- **Aruma's server must reject `Authorization: Bearer owner`** with 401, because emulator-mode official SDKs send it.

**Wire compatibility.**

- The server responds with the exact field names and types listed in §3, including quoted integers for Go.
- Errors use the `{"error":{"code":400,"message":"CODE : detail","status":…}}` shape, so the official mapping tables apply.

## Open questions

1. **Paths.** (Decided, see below.) Should Aruma serve `/identitytoolkit.googleapis.com/v1/projects/…`, which is emulator-compatible and convenient, but tempts people to point `FIREBASE_AUTH_EMULATOR_HOST` at production? Or a neutral `/v1/projects/…`?
2. **Custom-token claims.** Should `claims.firebase` and `claims.sub` in custom tokens be rejected or stripped? How the real Firebase backend handles them is unverified.
3. **Key URLs.** Should Aruma also publish an x509 PEM map (the `robot/v1/metadata/x509` format) for third-party verifiers? The libraries need only JWKS.
4. **Skew.** (Decided.) Is a 0 s `exp` tolerance acceptable for Node users, or should the library deliberately loosen it? Doing so departs from official behaviour.
5. **firebase-admin 13.x.** (Decided: 14 only.) Support it (`AuthClientErrorCode`, constructor not declared) through casts, or require 14.x?
6. **Go reflection factory.** (Decided: reflection, self-tested.) It relies on the field names `ErrorCode/String/Ext` and the Ext key `"authErrorCode"`. Is a CI version matrix enough, or should we upstream a public error constructor request?
7. **Key rotation.** Should signing keys be per project or shared, and how often should they rotate? `Cache-Control` max-age must stay below the rotation overlap.

## Decisions taken (T17, T18)

Recorded 2026-10-18 with the Admin API, session cookies and the two libraries. Code:
`internal/identity/api/admin.go`, `internal/identity/core/cookies.go`, `sdk/node/admin-auth`,
`sdk/go/adminauth`. Tests: `test/acceptance/auth_admin_test.go` and each library's suite
(`test/smoke/run.sh admin-auth-node admin-auth-go`).

### Admin API

- **Paths (open question 1).** Served under `/identitytoolkit.googleapis.com/v1/projects/{p}/…` as
  ADR-014 lists them. The risk of pointing `FIREBASE_AUTH_EMULATOR_HOST` at Aruma is closed on both
  ends: the server refuses `Bearer owner`, and the libraries refuse to start with the variable set.
- **No unprefixed forms.** Every user call of both SDKs goes through `/v1/projects/{p}`
  ([N-req] `ApiSettings` with `FIREBASE_AUTH_BASE_URL_FORMAT`; [G-um] `makeUserMgtURL`).
  `accounts:query` serves only Go's `QueryUsers`, which is not P0, so it is not served.
- **Credentials.** A service account token of the project (`aruma_sat_`) or the owner's CLI token
  (`aruma_pat_`), checked by `authn` before any handler. Anything else is 401 `UNAUTHENTICATED`
  (`google.rpc.Status`): no header, `Bearer owner`, ID tokens, refresh tokens, unsigned JWTs, API
  keys (header or `?key=`), console cookies. A project the caller may not act on (another
  project's service account, another owner, unknown or deleted project) is one uniform 403
  `PERMISSION_DENIED`. `{project}` is the id or the number. No CORS.
- **`validSince`.** Any value revokes at the server's clock. Moving it back would bring revoked
  sessions to life, and the SDKs only ever send "now".
- **`accounts:batchDelete`.** Without `force`, enabled accounts fail with `NOT_DISABLED`, which
  Node maps to `user-not-disabled` ([N-base] `deleteUsers`). The SDKs always send `force: true`.
- **`accounts:batchGet`.** Default page 20 (the Identity Toolkit REST default), 1 to 1000, else
  `INVALID_PAGE_SELECTION`. Order is by uid in the database collation; the page token is opaque.
- **`accounts:lookup`.** No match leaves `users` out; the SDKs turn that into `user-not-found`.
  Phone numbers and federated ids match nobody until those providers exist.
- **Deferred.** Phone numbers, MFA and provider linking answer `OPERATION_NOT_ALLOWED`.
  `accounts:sendOobCode` (the `generate*Link` calls) answers `OPERATION_NOT_ALLOWED` until the
  email action links land (a TODO marks the place).

### Session cookies

- **Claims.** Those of the ID token, with `iss = <public_url>/session-issuers/<project>`, `iat` now
  and `exp = iat + validDuration`, signed with the project's session cookie keys (ADR-004).
- **Duration.** `validDuration` 300 to 1 209 600 seconds, as a number or a string, else
  `INVALID_DURATION`. A missing value is `INVALID_DURATION` too (defensive: no silent default).
- **ID token.** It must pass the same check as `accounts:lookup`: genuine, current, of the
  project, its user live and enabled, not revoked (else `TOKEN_EXPIRED`). How recent the sign-in
  must be is left to the developer, as Firebase documents: "To only allow session cookie setting
  on recent sign-in, auth_time in ID token can be checked" ([D-cookies]).
- **Revocation.** The server's `VerifySessionCookie` compares `auth_time` with `validSince`.

### Libraries

- **Configuration.** Server: option, then `ARUMA_URL`, then the key file's `token_uri` minus
  `/oauth2/v1/token`; `https` except on loopback; Google hosts refused. Key, Node: option, then the
  `cert()` credential's source (`serviceAccountPathOrObject`, a non-public field of
  firebase-admin's `ServiceAccountCredential`, with `clientEmail`/`privateKey` as fallback), then
  `GOOGLE_APPLICATION_CREDENTIALS`. Key, Go: `firebase.App` exposes no credentials, so
  `WithCredentialsFile`/`WithCredentialsJSON`, then `GOOGLE_APPLICATION_CREDENTIALS`; the app is
  kept for `CustomToken`.
- **Emulator guard.** Refuse to start with `FIREBASE_AUTH_EMULATOR_HOST` set. Opt-in
  (`allowEmulatorHost` / `AllowEmulatorHost()`) is ignored in production: `NODE_ENV=production`
  in Node; `GO_ENV` or `NODE_ENV` equal to `production` in Go.
- **Verification.** RS256 with `kid` only; exact `aud` and `iss`; `sub` 1 to 128 characters;
  `iat`, `exp` and `auth_time` required as numbers. Keys come only from the issuer's discovery
  document, whose `issuer` must match and whose `jwks_uri` must be on the same origin; JWKS cached
  by `max-age`, refetched on an unknown `kid` at most every 30 s. Expiry is reported only for an
  otherwise genuine token.
- **Skew (open question 4).** `exp`: 0 s in Node, 300 s in Go, as each official SDK. `iat` and
  `auth_time` in the future: 300 s in both, so a few seconds of clock drift never rejects a fresh
  token.
- **checkRevoked.** `auth_time` in Node, `iat` in Go, as each official SDK. Both agree because
  refresh tokens issued before `validSince` answer `TOKEN_EXPIRED`. `validSince` has one-second
  resolution: a token from the same second as the revocation still passes, as in Firebase; the
  tests wait for the next second before revoking.
- **firebase-admin 13 (open question 5).** Not supported: `firebase-admin@^14` is a peer
  dependency, and a self-test at the first `getAuth()` checks the two constructors used.
- **Go errors (open question 6).** The reflection factory, with the type captured from an
  offline `VerifyIDToken(ctx, "")` of a throwaway app; `errors_test.go` proves `auth.Is*` and
  `errorutils.Is*` recognise the results, and that wrapping breaks them. Server codes map as
  `serverError` in [G-um]; client-side argument errors are plain errors, as in the Go SDK.
  `CreateUser`/`UpdateUser` take the official `UserToCreate`/`UserToUpdate` and read their
  unexported `params` map by reflection (read only). `NewClient` self-tests both. CI pins
  firebase-admin-go v4.22.0; a version matrix stays open.
- **Node errors.** `new FirebaseAuthError({code, message})` with `AuthErrorCode` values; server
  codes map as `AUTH_SERVER_TO_CLIENT_CODE` in [N-err] for the codes Aruma answers. Verification
  failures other than expiry are `argument-error`, as in Node.

## Experiments

Scratch code lived under `.cache/tmp/research-admin` and is not committed. It ran against firebase-admin 14.5.0 (plus 14.0.0, 14.3.0 and 13.10.0 typings) and firebase-admin-go v4.22.0, with Node 24 and Go 1.27.

- **E1. Node `cert()` custom token.** I hooked `net.Socket.connect` to watch for network use, decoded the JWT, tried reserved claims and a deep import, constructed `FirebaseAuthError` and `UserRecord`, and ran a monkeypatched verifier through 11 token cases.
- **E2. Node emulator mode.** A local fake emulator logged the requests.
- **E3. Node `token_uri`.** `cert()` with a local `token_uri` got `invalid_grant` from Google, and the local server got no request.
- **E4. Go custom token, reserved claims, error forging and wrapping, offline expiry error, and `token_uri`.** The local token server received the assertion.
- **E5. Go emulator mode.**
- **E6. `tsc --strict` against the 14.5 typings.**

## Source permalinks

- [N-tv]: https://github.com/firebase/firebase-admin-node/blob/a2ecf2a09e7469f0ab886ee2daf9faac37c4ebff/src/auth/token-verifier.ts
- [N-jwt]: https://github.com/firebase/firebase-admin-node/blob/a2ecf2a09e7469f0ab886ee2daf9faac37c4ebff/src/utils/jwt.ts
- [N-base]: https://github.com/firebase/firebase-admin-node/blob/a2ecf2a09e7469f0ab886ee2daf9faac37c4ebff/src/auth/base-auth.ts
- [N-req]: https://github.com/firebase/firebase-admin-node/blob/a2ecf2a09e7469f0ab886ee2daf9faac37c4ebff/src/auth/auth-api-request.ts
- [N-err]: https://github.com/firebase/firebase-admin-node/blob/a2ecf2a09e7469f0ab886ee2daf9faac37c4ebff/src/auth/error.ts
- [N-ur]: https://github.com/firebase/firebase-admin-node/blob/a2ecf2a09e7469f0ab886ee2daf9faac37c4ebff/src/auth/user-record.ts
- [N-tg]: https://github.com/firebase/firebase-admin-node/blob/a2ecf2a09e7469f0ab886ee2daf9faac37c4ebff/src/auth/token-generator.ts
- [N-cs]: https://github.com/firebase/firebase-admin-node/blob/a2ecf2a09e7469f0ab886ee2daf9faac37c4ebff/src/utils/crypto-signer.ts
- [G-tv]: https://github.com/firebase/firebase-admin-go/blob/66a72c371b4dbc592317ab7e7f99ce00d76c62ae/auth/token_verifier.go
- [G-auth]: https://github.com/firebase/firebase-admin-go/blob/66a72c371b4dbc592317ab7e7f99ce00d76c62ae/auth/auth.go
- [G-tg]: https://github.com/firebase/firebase-admin-go/blob/66a72c371b4dbc592317ab7e7f99ce00d76c62ae/auth/token_generator.go
- [G-um]: https://github.com/firebase/firebase-admin-go/blob/66a72c371b4dbc592317ab7e7f99ce00d76c62ae/auth/user_mgt.go
- [G-exp]: https://github.com/firebase/firebase-admin-go/blob/66a72c371b4dbc592317ab7e7f99ce00d76c62ae/auth/export_users.go
- [G-ierr]: https://github.com/firebase/firebase-admin-go/blob/66a72c371b4dbc592317ab7e7f99ce00d76c62ae/internal/errors.go
- [D-cookies]: https://firebase.google.com/docs/auth/admin/manage-cookies
