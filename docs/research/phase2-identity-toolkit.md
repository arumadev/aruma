# Phase 2 research: the Identity Toolkit / Secure Token contract

Status: research notes, 2026-10-18. Scope: the REST surface the **official** Firebase client SDKs call for email/password, anonymous and custom-token auth, so that Aruma can serve it.

Evidence levels used below:
- **[doc]**: official Firebase / Google Cloud documentation.
- **[sdk]**: client SDK source, which is the actual consumer of the contract.
- **[emu]**: Firebase Auth emulator source. Used only as a behavioural reference; it is *not* production. All emulator observations are paraphrased; no code was copied.

Source pins (shallow clones read on 2026-10-18): firebase-js-sdk `d7bb0e1`, firebase-ios-sdk `dc5821f`, firebase-tools `1168863`. Citation keys such as [J-index] are defined in the **References** section at the end, and each one maps to a URL.

---

## 0. Key finding: no official SDK can be pointed at Aruma by the config file alone

**Web (JS SDK).** The API hosts are compile-time constants: `identitytoolkit.googleapis.com`, `securetoken.googleapis.com`, scheme `https` [J-impl]. `registerAuth` copies only `apiKey` and `authDomain` from the app options and adds those constant hosts [J-register]. `authDomain` is used only for the popup/redirect handler (`https://<authDomain>/__/auth/handler`) and the iframe (`/__/auth/iframe`) [J-handler][J-iframe]. **Nothing in `firebaseConfig` changes the REST host.**

The only supported override is `connectAuthEmulator(auth, url)` [J-emu]:
- `url` must match `^https?://`, so **HTTPS is allowed**. Any path in it is discarded.
- Each request then goes to `<url>/<original-host><path>?key=…` [J-index] [J-emuurl]. For example, `https://auth.aruma.example/identitytoolkit.googleapis.com/v1/accounts:signUp?key=K` and `https://auth.aruma.example/securetoken.googleapis.com/v1/token?key=K`. **Aruma must serve these host-prefixed paths.** The emulator does the same [E-server].
- Side effects [J-emu][J-origin][J-handler][J-iframe]:
  - A console warning and a red "Running in emulator mode" DOM banner appear unless `{disableWarnings: true}` is passed.
  - `settings.appVerificationDisabledForTesting = true`, which affects phone auth.
  - `_validateOrigin` is skipped, so `GET /v1/projects` is never called for popup/redirect.
  - The popup/redirect handler moves to `<url>/emulator/auth/handler` and the iframe to `<url>/emulator/auth/iframe`.
  - It must be called before the first request.

**iOS.** The hosts are constants too. `Auth.useEmulator(withHost:port:)` is the only override, and it **forces `http:`** [I-itk][I-useemu]:
- Identity Toolkit requests go to `http://<host:port>/www.googleapis.com/identitytoolkit/v3/relyingparty/<rpc>?key=…`.
- v2 calls go to `http://<host:port>/identitytoolkit.googleapis.com/v2/<rpc>`.
- Token requests go to `http://<host:port>/securetoken.googleapis.com/v1/token` [I-sts].

So production iOS against Aruma means plaintext HTTP, which in turn needs an App Transport Security exception, or a TLS-terminating local proxy.

**Android.** The SDK is closed source. The docs only show `useEmulator("10.0.2.2", 9099)` [D-emu], so the scheme and path layout are **unverified**.

**Implication for the plan:** "config file only" is not achievable. The minimum is one SDK call (`connectAuthEmulator` with `disableWarnings`). On mobile, the emulator hook is plaintext-only. This needs an ADR decision.

---

## 1. Common transport rules

- **Requests.** JSON `POST`, with the API key in the `?key=` query parameter. `GET` requests (`/v1/projects`, `/v2/passwordPolicy`, `/v2/recaptchaConfig`) carry their parameters in the query string [J-index].
- **Headers sent by the JS SDK** [J-headers][J-index]: `Content-Type: application/json`, `X-Client-Version`, `X-Firebase-gmpid` (the appId), `X-Firebase-Client` (heartbeat), `X-Firebase-AppCheck`, `X-Firebase-Locale` (when `languageCode` is set). The browser preflights these, so **CORS must allow them**.
- **Timeout.** The JS SDK treats a response slower than 30 s as `auth/network-request-failed` [J-index].
- **Success detection (JS)** [J-index]:
  - The response is a success when it is HTTP 2xx **and** has no `errorMessage` key. A 200 response with `errorMessage` is treated as an error (legacy behaviour).
  - Any response containing a `needConfirmation` key throws `account-exists-with-different-credential`.
  - A `mfaPendingCredential` key throws `multi-factor-auth-required` [J-index]. **Never emit these keys by accident.**
- **Case.** Google REST mappings accept both snake_case and camelCase field names; the emulator normalises them [E-server]. Aruma should accept both.

## 2. Error envelope and client mapping

Official shape [D-rest]:
```json
{"error":{"code":400,"message":"EMAIL_EXISTS","errors":[{"message":"EMAIL_EXISTS","domain":"global","reason":"invalid"}]}}
```
"The error code is obtained from the message field" [D-rest]. The emulator emits exactly this for business errors: HTTP 400, no `status` field, `reason:"invalid"`, `domain:"global"` [E-errors]. It adds `status` (`INVALID_ARGUMENT`, `PERMISSION_DENIED`, …) only for non-business errors.

**Detail suffix.** The message may be `CODE : detail`, for example `WEAK_PASSWORD : Password should be at least 6 characters` [E-ops signUp].
- JS splits on the exact string `" : "`. It uses the detail as the error message, and the code for the mapping [J-index].
- iOS splits at the **first `:`** and trims both halves [I-backend].
- Emitting `CODE : detail`, with spaces, therefore works for both.

**JS mapping** [J-errors][J-index]:
- The client first looks up `SERVER_ERROR_MAP`.
- If the code is not there, it falls back to `auth/` + the code lower-cased with `_` replaced by `-`. For example, `WEAK_PASSWORD` becomes `auth/weak-password` and `INVALID_REFRESH_TOKEN` becomes `auth/invalid-refresh-token`.
- `EMAIL_EXISTS`, `USER_DISABLED` and `FEDERATED_USER_ID_ALREADY_LINKED` are special-cased: they carry the server response as `customData`.

Relevant map entries (server code → `auth/*`) [J-errors][J-codes]:

| Server code | `auth/*` code |
|---|---|
| `EMAIL_EXISTS` | `email-already-in-use` |
| `INVALID_LOGIN_CREDENTIALS` | `invalid-credential` |
| `INVALID_PASSWORD` | `wrong-password` |
| `EMAIL_NOT_FOUND` | `user-not-found` |
| `MISSING_PASSWORD` | `missing-password` |
| `PASSWORD_LOGIN_DISABLED` | `operation-not-allowed` |
| `INVALID_IDENTIFIER` | `invalid-email` |
| `INVALID_ID_TOKEN` | `invalid-user-token` |
| `TOKEN_EXPIRED` | `user-token-expired` |
| `USER_NOT_FOUND` | `user-token-expired` (not `user-not-found`) |
| `CREDENTIAL_TOO_OLD_LOGIN_AGAIN` | `requires-recent-login` |
| `CREDENTIAL_MISMATCH` | `custom-token-mismatch` |
| `MISSING_CUSTOM_TOKEN` | `internal-error` |
| `MISSING_CONTINUE_URI` | `internal-error` |
| `MISSING_REQ_TYPE` | `internal-error` |
| `MISSING_OOB_CODE` | `internal-error` |
| `EXPIRED_OOB_CODE` | `expired-action-code` |
| `INVALID_OOB_CODE` | `invalid-action-code` |
| `UNAUTHORIZED_DOMAIN` | `unauthorized-continue-uri` |
| `TOO_MANY_ATTEMPTS_TRY_LATER` | `too-many-requests` |
| `RESET_PASSWORD_EXCEED_LIMIT` | `too-many-requests` |
| `PASSWORD_DOES_NOT_MEET_REQUIREMENTS` | `password-does-not-meet-requirements` |

These codes reach the client through the fallback rule instead: `WEAK_PASSWORD`, `INVALID_EMAIL`, `OPERATION_NOT_ALLOWED`, `INVALID_CUSTOM_TOKEN`, `USER_DISABLED`.

**Forced sign-out (JS).** Only `auth/user-disabled` and `auth/user-token-expired` sign the user out [J-invalid]. The consequence: a revoked or deleted session must return `TOKEN_EXPIRED`, `USER_NOT_FOUND` or `USER_DISABLED`, **not** `INVALID_REFRESH_TOKEN` or `INVALID_ID_TOKEN`. Those last two leave a zombie session.

**Invalid API key.** Production returns the message `API key not valid. Please pass a valid API key.` [D-rest]. JS has no map entry for it, so the fallback mangles it into a code. Exact parity needs a production capture (see the open list).

---

## 3. Endpoints (v1, as called by the web SDK)

The JS endpoint table [J-index] contains:
- `/v1/accounts:{createAuthUri, delete, resetPassword, signUp, signInWithCustomToken, signInWithEmailLink, signInWithIdp, signInWithPassword, signInWithPhoneNumber, sendVerificationCode, sendOobCode, update, lookup}`
- `/v1/recaptchaParams`, `/v1/projects`, `/v1/token` (on the securetoken host)
- `/v2/{passwordPolicy, recaptchaConfig, accounts:revokeToken, accounts/mfaEnrollment:*, accounts/mfaSignIn:*}`

`expiresIn` is always a **string** of seconds (`"3600"`) [D-rest].

**After every sign-in, JS immediately calls `accounts:lookup`** to populate the user object [J-user][J-reload]. Lookup must therefore work with a token that was issued milliseconds earlier.

### 3.1 `accounts:signUp`
**Request** [D-rest][J-signup]: `email`, `password`, `returnSecureToken`, `displayName`, `idToken` (to link an email/password credential to the current user, for example an anonymous one), `tenantId`, `captchaResponse`, `clientType` (`CLIENT_TYPE_WEB`), `recaptchaVersion`. An empty body is anonymous sign-up.

**Response:**
- `kind` (`identitytoolkit#SignupNewUserResponse`), `localId`, `email` (`""` for anonymous users), `displayName`, `idToken`, `refreshToken`, `expiresIn` [D-rest][E-ops signUp].
- iOS ignores `localId` and takes the uid from the follow-up lookup [I-fields].

**Errors** [D-rest]: `EMAIL_EXISTS`, `OPERATION_NOT_ALLOWED`, `TOO_MANY_ATTEMPTS_TRY_LATER`, `WEAK_PASSWORD`, `INVALID_EMAIL`, `MISSING_PASSWORD`, `MISSING_EMAIL`, and `ADMIN_ONLY_OPERATION` (anonymous sign-up disabled, per the emulator; the REST doc says `OPERATION_NOT_ALLOWED`).

**Validation order [emu only]** [E-ops signUp]: API key, then `MISSING_EMAIL`, `MISSING_PASSWORD`, `OPERATION_NOT_ALLOWED`, `INVALID_EMAIL`, `EMAIL_EXISTS`, `WEAK_PASSWORD`, and only after that the `idToken` checks (`INVALID_ID_TOKEN` / `USER_NOT_FOUND` / `TOKEN_EXPIRED` / `USER_DISABLED`).
- **The plan's order matches the emulator exactly.** No official doc states an order, so production must be compared.
- Under enumeration protection, sign-up **still returns `EMAIL_EXISTS`** [D-enum].

### 3.2 `accounts:signInWithPassword`
**Request** [D-rest][J-pw]: `email`, `password`, `returnSecureToken`, `tenantId`, `captchaResponse`, `clientType`, `recaptchaVersion`.

**Response** [D-rest]:
- `kind` (`identitytoolkit#VerifyPasswordResponse`), `localId`, `email`, `displayName` (`""` in the doc sample), `idToken`, `registered: true`, `refreshToken`, `expiresIn`.
- When the password policy is in notify mode, the response also carries `userNotifications[{notificationCode, notificationMessage}]` [D-pwpol].

**Errors** [D-rest][D-enum]:
- Without enumeration protection: `EMAIL_NOT_FOUND`, `INVALID_PASSWORD`, `USER_DISABLED`.
- With protection on (the default for projects created on or after 2023-09-15): invalid sign-ins return `INVALID_LOGIN_CREDENTIALS`.
- When email/password sign-in is disabled: `PASSWORD_LOGIN_DISABLED`, not `OPERATION_NOT_ALLOWED` [E-ops signIn][J-errors].

**Validation order [emu only]** [E-ops signIn]: `PASSWORD_LOGIN_DISABLED`, `MISSING_EMAIL`, `INVALID_EMAIL`, `MISSING_PASSWORD`, then:
- With protection on: no user gives `INVALID_LOGIN_CREDENTIALS`, then `USER_DISABLED`, then a missing or wrong hash gives `INVALID_LOGIN_CREDENTIALS`.
- With protection off: `EMAIL_NOT_FOUND`, then `USER_DISABLED`, then `INVALID_PASSWORD`.
- ⚠ The emulator reports `USER_DISABLED` *before* checking the password, which leaks account state. Production must be compared before copying this.

### 3.3 `accounts:signInWithCustomToken`
**Request:** `token`, `returnSecureToken`, `tenantId` [D-rest].

**Response:** `kind` (`…VerifyCustomTokenResponse`), `idToken`, `refreshToken`, `expiresIn`, `isNewUser`. There is no `localId` [D-rest][E-ops custom]; the uid comes from the lookup.

**Errors** [D-rest][E-ops custom]:
- `INVALID_CUSTOM_TOKEN`: bad format, expired, or bad signature.
- `CREDENTIAL_MISMATCH`: the token belongs to another project.
- `MISSING_CUSTOM_TOKEN`.
- `USER_DISABLED`: the uid exists and is disabled.
- [emu] `MISSING_IDENTIFIER` (no `uid`), `INVALID_CLAIMS`, and `FORBIDDEN_CLAIM : <name>`.

The token format is covered in §5.

### 3.4 `accounts:lookup`
**Request:** `{idToken}` [J-account].

**Response** [D-rest]: `kind` and `users[]`. Each user has:
- `localId`, `email`, `emailVerified`, `displayName`, `photoUrl`, `phoneNumber`
- `providerUserInfo[{providerId, rawId, federatedId, email, displayName, photoUrl}]`
- `passwordHash`, `passwordUpdatedAt` (a number in ms)
- `validSince`, as a string of seconds
- `disabled`
- `lastLoginAt` and `createdAt`, as **strings of ms**
- `customAuth`

Client requirements:
- iOS parses `createdAt` and `lastLoginAt` as strings [I-getacct].
- ⚠ JS treats the user as anonymous unless `email && passwordHash` are present or a provider is linked [J-reload]. **Aruma must return a non-empty placeholder `passwordHash`** for password users and never the real hash. The production placeholder value is open.

**Errors:** `INVALID_ID_TOKEN`, `USER_NOT_FOUND` [D-rest]; [emu] `TOKEN_EXPIRED` (when `iat` is before `validSince`), `USER_DISABLED` [E-ops parseIdToken].

### 3.5 `accounts:update`
This one endpoint carries several operations [D-rest][J-acctpw][J-profile]:
- Change email: `{idToken, email, returnSecureToken}`. Blocked under enumeration protection [D-enum].
- Change password: `{idToken, password}`.
- Profile: `{idToken, displayName, photoUrl, deleteAttribute:["DISPLAY_NAME"|"PHOTO_URL"]}`. JS sends a literal JSON `null` to clear a field [J-profile].
- Unlink: `{idToken, deleteProvider:[…]}`.
- Apply action code: `{oobCode}`, used for `VERIFY_EMAIL`, `VERIFY_AND_CHANGE_EMAIL` and `RECOVER_EMAIL` [E-ops setAccountInfo].

**Response** [D-rest]:
- `kind`, `localId`, `email`, `displayName`, `photoUrl`, `passwordHash`, `providerUserInfo`, `emailVerified`.
- When the email or password changed, it also returns `idToken`, `refreshToken` and `expiresIn`. Those changes revoke older sessions [D-sessions].

**Errors:** `EMAIL_EXISTS`, `INVALID_ID_TOKEN`, `WEAK_PASSWORD`, `CREDENTIAL_TOO_OLD_LOGIN_AGAIN`. For the oobCode branch: `EXPIRED_OOB_CODE`, `INVALID_OOB_CODE`, `USER_DISABLED`, `EMAIL_NOT_FOUND` [D-rest]. Without an `idToken` or `oobCode`, the emulator returns `INVALID_REQ_TYPE` [E-ops setAccountInfo].

### 3.6 `accounts:delete`
**Request:** `{idToken}`. **Response:** `{kind}`. **Errors:** `INVALID_ID_TOKEN`, `USER_NOT_FOUND`, plus `CREDENTIAL_TOO_OLD_LOGIN_AGAIN` (recent login required, mapped in JS) [D-rest][J-errors].

### 3.7 `accounts:sendOobCode`
**Request** [D-oob][J-pw]:
- `requestType`: one of `PASSWORD_RESET`, `VERIFY_EMAIL`, `VERIFY_AND_CHANGE_EMAIL`, `EMAIL_SIGNIN`.
- `email`: required for `PASSWORD_RESET`, `EMAIL_SIGNIN` and `VERIFY_EMAIL`. `VERIFY_EMAIL` uses the idToken user.
- `idToken`: required for `VERIFY_EMAIL` and `VERIFY_AND_CHANGE_EMAIL`.
- `newEmail`, `continueUrl`, `canHandleCodeInApp`, `iOSBundleId`, `androidPackageName`, `androidInstallApp`, `androidMinimumVersion`, `dynamicLinkDomain`, `linkDomain`, `tenantId`.
- `captchaResp`, `clientType`, `recaptchaVersion`.
- `returnOobLink`: privileged callers only.
- Header `X-Firebase-Locale` localises the email [D-rest].

**Response:** `{kind:"identitytoolkit#GetOobConfirmationCodeResponse", email}` [D-rest].

**Errors:**
- `EMAIL_NOT_FOUND` [D-rest]; `INVALID_EMAIL`, `MISSING_REQ_TYPE`, `INVALID_CONTINUE_URI`, `MISSING_CONTINUE_URI`, `UNAUTHORIZED_DOMAIN`, `INVALID_ID_TOKEN`.
- `EMAIL_EXISTS` for `newEmail` [J-errors][E-ops sendOob].
- Under enumeration protection, errors are removed for `PASSWORD_RESET` and `VERIFY_AND_CHANGE_EMAIL`. The email is simply not sent, and the server returns 200 [D-enum].
- The emulator silently succeeds for an unknown address on `PASSWORD_RESET`, but still returns `EMAIL_EXISTS` for `VERIFY_AND_CHANGE_EMAIL`. That is a gap versus the doc [E-ops sendOob].

### 3.8 `accounts:resetPassword`
**Request:** `oobCode`, optional `newPassword`, and `email` + `oldPassword` (a legacy path), `tenantId` [D-reset].
- **Without `newPassword`**, the call only inspects the code: `verifyPasswordResetCode` / `checkActionCode`. It does not consume it.
- With `newPassword`, it consumes the code. Only `PASSWORD_RESET` codes can be consumed this way [D-reset][J-pwstrat].

**Response:** `kind`, `email`, `newEmail`, `requestType`, `mfaInfo` [D-reset]. The emulator omits `email` for `EMAIL_SIGNIN` codes [E-ops reset].

**Errors:** `OPERATION_NOT_ALLOWED`, `EXPIRED_OOB_CODE`, `INVALID_OOB_CODE`, `USER_DISABLED`, `WEAK_PASSWORD` [D-rest][E-ops reset].

**Side effects:** a password reset revokes existing tokens [D-sessions]. The emulator also sets `emailVerified=true` [E-ops reset]; production must be compared.

### 3.9 `accounts:createAuthUri` (fetchSignInMethodsForEmail)
**Request:** `identifier`, `continueUri`, plus IdP fields [D-authuri]. JS sends the current URL, or `http://localhost` when it is not running over http(s) [J-email].

**Response** [D-authuri]: `kind`, `registered`, `allProviders` (deprecated), `sessionId`, `signinMethods` (any of `password`, `emailLink`, or an IdP id).
- Under enumeration protection the list is **empty** [D-enum][D-authuri].
- The emulator returns only `{kind, sessionId}` in that case [E-ops authuri].

**Errors:** `INVALID_EMAIL` [D-rest]; `INVALID_IDENTIFIER`, `MISSING_CONTINUE_URI`, `INVALID_CONTINUE_URI` [E-ops authuri].

### 3.10 `GET /v1/projects` (getProjectConfig)
**Response:** `{projectId, authorizedDomains[]}` [J-projcfg][I-fields]. The emulator returns the **project number** as `projectId` [E-ops projects].

When it is called:
- JS calls it only from `_validateOrigin` (popup/redirect) and in Cordova [J-origin], and **skips it in emulator mode**.
- iOS calls the v3 `getProjectConfig` with `GET` [I-proj].

### 3.11 `GET /v2/passwordPolicy`
**Response:** `customStrengthOptions{minPasswordLength, maxPasswordLength, containsLowercaseCharacter, containsUppercaseCharacter, containsNumericCharacter, containsNonAlphanumericCharacter}`, `allowedNonAlphanumericCharacters[]`, `enforcementState` (`OFF` / `ENFORCE`), `forceUpgradeOnSignin`, `schemaVersion` [D-pwpolref][J-pwpol].

**When it is called.** JS fetches it only when the app calls `validatePassword()`, or to re-cache after a `PASSWORD_DOES_NOT_MEET_REQUIREMENTS` error when a policy is already cached [J-pwstrat]. iOS has no client for this endpoint [I-search].

### 3.12 `GET /v2/recaptchaConfig`
**Query:** `clientType`, `version`, `tenantId`. **Response:** `recaptchaKey`, `recaptchaEnforcementState[{provider, enforcementState}]` [D-recap][J-recap].

JS fetches it only on `initializeRecaptchaConfig()`, or after an `auth/missing-recaptcha-token` error, and then retries with a token [J-recapflow]. **Aruma can return `OFF` for `EMAIL_PASSWORD_PROVIDER` and `PHONE_PROVIDER`, and must never emit `MISSING_RECAPTCHA_TOKEN`.**

### 3.13 `POST /v2/accounts:revokeToken`
**Request:** `{providerId:"apple.com", tokenType, token, idToken, tenantId, redirectUri}`. **Response:** empty [D-revoke][J-token]. This is Apple-only, so it is low priority.

### 3.14 Secure Token: `POST https://securetoken.googleapis.com/v1/token?key=…`
**Request.** The doc and JS send form-urlencoded `grant_type=refresh_token&refresh_token=…` [D-rest][J-token]. **iOS sends JSON `{"grantType":"refresh_token","refreshToken":"…"}`** [I-sts]. Aruma must therefore accept both encodings and both casings.

**Response** (snake_case) [D-rest]:
- `access_token`, `expires_in` (string), `token_type: "Bearer"`, `refresh_token`, `id_token`, `user_id`, `project_id`.
- `project_id`: the doc text says "project ID", but its sample is `"1234567890"` and the API spec says "project number" [E-spec]. **Use the project number.**
- JS reads only `access_token`, `expires_in` and `refresh_token` [J-token]. iOS requires a non-empty `access_token` [I-ststresp]. So set `access_token = id_token`, as the emulator does [E-ops grant].

**Errors** [D-rest]: `TOKEN_EXPIRED` (credential revoked, for example after a password change), `USER_DISABLED`, `USER_NOT_FOUND` (deleted), `INVALID_REFRESH_TOKEN`, `INVALID_GRANT_TYPE`, `MISSING_REFRESH_TOKEN`, `PROJECT_NUMBER_MISMATCH`, an invalid API key, and an "Invalid JSON payload… Unknown name" error for unknown fields.

**Order [emu]:** `MISSING_GRANT_TYPE`, `INVALID_GRANT_TYPE`, `MISSING_REFRESH_TOKEN`, `INVALID_REFRESH_TOKEN`, `USER_DISABLED` [E-ops grant].
- ⚠ For a deleted user the emulator returns `INVALID_REFRESH_TOKEN`, but the docs say `USER_NOT_FOUND`. **Follow the docs**: per §2, only `USER_NOT_FOUND` signs the client out.

**Refresh timing.** JS refreshes when the token is within 30 s of expiry [J-tm].

---

## 4. v3 relyingparty (iOS)

The current iOS SDK **still uses v3** for all core RPCs [I-itk][I-endpoints]: `https://www.googleapis.com/identitytoolkit/v3/relyingparty/<rpc>?key=…`. Only `recaptchaConfig` and `accounts:revokeToken` go to `identitytoolkit.googleapis.com/v2`.

The emulator maps v3 to v1 one-to-one, with the same bodies, which supports the idea of a single implementation behind two routers [E-server]:

| v3 path | v1 path |
|---|---|
| `signupNewUser` | `accounts:signUp` |
| `verifyPassword` | `accounts:signInWithPassword` |
| `verifyCustomToken` | `accounts:signInWithCustomToken` |
| `verifyAssertion` | `accounts:signInWithIdp` |
| `getAccountInfo` | `accounts:lookup` |
| `setAccountInfo` | `accounts:update` |
| `deleteAccount` | `accounts:delete` |
| `getOobConfirmationCode` | `accounts:sendOobCode` |
| `resetPassword` | `accounts:resetPassword` |
| `createAuthUri` | `accounts:createAuthUri` |
| `getProjectConfig` | `projects` (GET) |
| `emailLinkSignin` | `accounts:signInWithEmailLink` |
| `getRecaptchaParam` | `recaptchaParams` |

Field differences seen in the iOS request and response classes [I-fields]:
- Field names are the same camelCase as v1: `email`, `password`, `returnSecureToken`, `idToken`, `token`, `oobCode`, `newPassword`, `requestType`, `deleteAttribute`, `deleteProvider`, `photoUrl`, `captchaResponse`, `clientType`.
- `deleteAccount` sends `localId` as well as `idToken`.
- Responses: iOS reads `idToken`, `refreshToken` and `expiresIn`. It ignores `localId` on sign-up, and parses lookup timestamps as strings.
- `kind` values are the legacy `identitytoolkit#…Response` names that v1 also returns [J-kinds].

Android is unverified (closed source). Supporting both path families covers either outcome.

---

## 5. Tokens

**ID token (what Firebase issues)** [D-verify][D-rules][E-ops jwt]:
- Header: `alg: RS256`, and a `kid` that matches a published key.
- Payload:
  - `iss = https://securetoken.google.com/<projectId>`
  - `aud = <projectId>`, the alphanumeric project id, not the number
  - `sub = user_id = uid`
  - `iat`, `exp` (equal to `iat + 3600`) and `auth_time` (time of the last real sign-in), all in seconds
  - `email`, `email_verified`, `phone_number`, `name`, `picture`
  - `firebase.identities`: for example `{"email":[…],"google.com":[…]}`
  - `firebase.sign_in_provider`: one of `custom`, `password`, `phone`, `anonymous`, or an IdP id
  - `firebase.sign_in_second_factor` and `firebase.tenant`, when they apply
  - Custom claims, merged at the top level.
  - The emulator also sets `provider_id:"anonymous"` for anonymous users only, citing production parity [E-ops jwt].
- `getIdTokenResult` throws `internal-error` if `exp`, `iat` or `auth_time` is missing [J-idtr]. JS never verifies the signature.

**Custom token accepted by `signInWithCustomToken`** [D-custom]:
- `alg: RS256`
- `iss = sub =` the service-account email
- `aud = https://identitytoolkit.googleapis.com/google.identity.identitytoolkit.v1.IdentityToolkit`
- `iat`, and an `exp` that is **at most** `iat + 3600`
- `uid`: a string of 1–128 characters
- optional `claims`

Claim limits:
- Reserved claim names are not allowed in `claims`: `acr amr at_hash aud auth_time azp cnf c_hash exp iat iss jti nbf nonce sub firebase user_id` [D-custom]. The emulator rejects these with `FORBIDDEN_CLAIM` [E-ops claims].
- The custom-claims payload is limited to ≤1000 bytes [D-claims].
- An expired token or a bad signature gives `INVALID_CUSTOM_TOKEN`. A token for another project gives `CREDENTIAL_MISMATCH` [D-rest].

**Refresh tokens** expire only when the user is deleted or disabled, or after a major account change such as a password or email update [D-sessions]. ID tokens last 1 h [D-sessions].

---

## 6. OOB codes and action links

- **Request type → link `mode`**, as the emulator generates it [E-ops oob]:

  | `requestType` | `mode` |
  |---|---|
  | `PASSWORD_RESET` | `resetPassword` |
  | `VERIFY_EMAIL` | `verifyEmail` |
  | `VERIFY_AND_CHANGE_EMAIL` | `verifyAndChangeEmail` |
  | `EMAIL_SIGNIN` | `signIn` |

  `recoverEmail` is sent to the old address after an email change. The docs list `resetPassword`, `recoverEmail` and `verifyEmail` for custom handlers [D-handler].
- **Link format.** Firebase appends `mode`, `oobCode`, `apiKey`, `continueUrl` (optional) and `lang` (optional) [D-handler]. The default handler path is `/__/auth/action` [D-templates]. So the shape is `https://<authDomain>/__/auth/action?mode=…&oobCode=…&apiKey=…&continueUrl=…&lang=…`. The parameter order is not significant.
- **`continueUrl` validation.** Its domain must be authorized ("whitelisted in the Firebase Console") [D-actionlinks].
  - A failed check returns `UNAUTHORIZED_DOMAIN`, which JS shows as `unauthorized-continue-uri`.
  - Malformed URLs return `INVALID_CONTINUE_URI` [J-errors].
  - The emulator checks only that the URL is absolute [E-ops sendOob].
- **Inspect vs consume.** `resetPassword {oobCode}` inspects a code; adding `newPassword` consumes it (§3.8). Verification codes are applied with `accounts:update {oobCode}` (§3.5). Codes are single-use (`INVALID_OOB_CODE` "already been used") [D-rest].
- **Expiry durations: not found in official docs.** Community figures such as "1 h for reset, 3 days for verify" are unverified. This needs a production measurement.
- Aruma's choices for lifetimes, the action page and delivery: [phase2-email-actions.md](phase2-email-actions.md).

## 7. Email templates

The documented placeholders are `%DISPLAY_NAME%`, `%APP_NAME%` (the public-facing name), `%LINK%`, `%EMAIL%`, and `%NEW_EMAIL%` (email-change template only) [D-templates].

Customisable fields are the sender name, sender address, reply-to, subject, and the message body — the doc says the body is editable "for password reset emails" [D-templates]. In the console the template bodies follow this structure (from inspection, not from any doc):
1. A greeting, with the display name where known.
2. One sentence on the action.
3. The `%LINK%`.
4. A "you can ignore this email" disclaimer.
5. A sign-off from "Your %APP_NAME% team".

The texts themselves must be written for Aruma, not copied.

## 8. Password policy and disabled users

- **Password policy.**
  - Default minimum length is 6, configurable from 6 to 30; maximum is 4096 [D-pwpol][D-pwpolref].
  - The legacy rule returns `WEAK_PASSWORD : Password should be at least 6 characters` [E-ops signUp][D-rest]; the REST doc phrases it as "must be 6 characters long or more".
  - When a policy is set to `ENFORCE`, failing passwords return `PASSWORD_DOES_NOT_MEET_REQUIREMENTS`. Notify mode instead adds `userNotifications` [D-pwpol]. The detail format of that error is unverified.
- **Disabled users.**
  - Sign-in, refresh and password-reset confirmation return `USER_DISABLED` [D-rest].
  - Refresh tokens stop working [D-sessions].
  - [emu] Every idToken-bearing call and custom-token sign-in also returns `USER_DISABLED` [E-ops parseIdToken][E-ops custom].
  - JS signs out on `user-disabled` [J-invalid].

---

## 9. Open / needs production comparison

1. **ADR:** web needs `connectAuthEmulator(auth, "https://…", {disableWarnings:true})`. iOS `useEmulator` is HTTP-only. Android's scheme and paths are unknown and need a test app. Is "config-only" dropped?
2. Production validation order for signUp, signInWithPassword and token refresh. The plan's signUp order matches only the emulator.
3. Whether production returns `USER_DISABLED` before checking the password.
4. The production `passwordHash` placeholder in lookup and update responses (JS anonymous detection depends on it).
5. `project_id` in the token response: number or id (the doc contradicts itself).
6. The exact invalid-API-key envelope (`status`, `details[]`) and the error for a missing `key`.
7. OOB code expiry per type; the action-link parameter order; whether `lang` is always present.
8. The error code when changing email via `accounts:update` under enumeration protection. `EMAIL_CHANGE_NEEDS_VERIFICATION` is in the JS enum, which suggests it, but this is unverified.
9. Whether `resetPassword` with `newPassword` sets `emailVerified=true` in production.
10. Whether a JSON `null` for `displayName`/`photoUrl` in `accounts:update` clears the field. JS sends null; the emulator ignores it.
11. The `PASSWORD_DOES_NOT_MEET_REQUIREMENTS` detail-message format; the error name when custom claims exceed 1000 bytes.
12. Whether `accounts:lookup` returns `lastRefreshAt`, `initialEmail` or other fields that the SDKs might start to read.
13. How Admin SDKs verify Aruma ID tokens: the hard-coded Google certificate URL versus the emulator env var. This belongs with Phase 2 token signing.

---

## References

**JS SDK** (base `https://github.com/firebase/firebase-js-sdk/blob/d7bb0e16f2b13ba15f74b690596eb70ef145d4f6/packages/auth/src/`):
- [J-index] `api/index.ts` (endpoints L56–81, error handling L196–250, `_getFinalTarget` L280–310): …/api/index.ts
- [J-errors] `api/errors.ts` L134ff: …/api/errors.ts
- [J-codes] `core/errors.ts`: …/core/errors.ts
- [J-impl] `core/auth/auth_impl.ts` L92–96: …/core/auth/auth_impl.ts
- [J-headers] the same file, `_getAdditionalHeaders` L866: …/core/auth/auth_impl.ts
- [J-register] `core/auth/register.ts` L70–86: …/core/auth/register.ts
- [J-emu] `core/auth/emulator.ts` L45–105, L144: …/core/auth/emulator.ts
- [J-emuurl] `core/util/emulator.ts`: …/core/util/emulator.ts
- [J-handler] `core/util/handler.ts` L34–41, L130: …/core/util/handler.ts
- [J-iframe] `platform_browser/iframe/iframe.ts` L31–58: …/platform_browser/iframe/iframe.ts
- [J-origin] `core/util/validate_origin.ts` L27–33: …/core/util/validate_origin.ts
- [J-token] `api/authentication/token.ts`: …/api/authentication/token.ts
- [J-signup] `api/authentication/sign_up.ts`
- [J-pw] `api/authentication/email_and_password.ts`
- [J-account] `api/account_management/account.ts`
- [J-acctpw] `api/account_management/email_and_password.ts`
- [J-profile] `api/account_management/profile.ts` and `core/user/account_info.ts`
- [J-projcfg] `api/project_config/get_project_config.ts`
- [J-pwpol] `api/password_policy/get_password_policy.ts`
- [J-recap] `api/authentication/recaptcha.ts`
- [J-recapflow] `platform_browser/recaptcha/recaptcha_enterprise_verifier.ts` L290–360
- [J-pwstrat] `core/strategies/email_and_password.ts` L64, L144–191
- [J-email] `core/strategies/email.ts` L61
- [J-user] `core/user/user_impl.ts` L330–342
- [J-reload] `core/user/reload.ts` L30–75
- [J-invalid] `core/user/invalidation.ts`
- [J-idtr] `core/user/id_token_result.ts`
- [J-tm] `core/user/token_manager.ts` L32
- [J-kinds] `model/id_token.ts`

**iOS SDK** (base `https://github.com/firebase/firebase-ios-sdk/blob/dc5821fd8c986596363fef8feec4b0e8c1f0ded3/FirebaseAuth/Sources/Swift/`):
- [I-itk] `Backend/IdentityToolkitRequest.swift` L24–105
- [I-sts] `Backend/RPC/SecureTokenRequest.swift` L120–145
- [I-ststresp] `Backend/RPC/SecureTokenResponse.swift`
- [I-backend] `Backend/AuthBackend.swift` L255–330
- [I-useemu] `Auth/Auth.swift` L1453
- [I-getacct] `Backend/RPC/GetAccountInfoResponse.swift` L111–122
- [I-proj] `Backend/RPC/GetProjectConfigRequest.swift`, `Backend/AuthRPCRequest.swift`
- [I-endpoints] / [I-fields] `Backend/RPC/*Request.swift` / `*Response.swift`
- [I-search] the `Backend/` tree contains no password-policy RPC

**Emulator** (base `https://github.com/firebase/firebase-tools/blob/11688637a1a210147fdcec0a1198045196b781cc/src/emulator/auth/`):
- [E-ops] `operations.ts`: signUp L158, createAuthUri L602, getProjects L736, resetPassword L828, sendOobCode L875, setAccountInfoImpl L1046, signInWithCustomToken L1369, signInWithPassword L1791, grantToken L1960, parseIdToken L2507, generateJwt L2549, forbidden claims L2683
- [E-errors] `errors.ts`
- [E-server] `server.ts` L407–435 (v3 routes), L636–650 (case normalisation)
- [E-spec] `apiSpec.ts`, GrantTokenResponse

**Official docs:**
- [D-rest] https://firebase.google.com/docs/reference/rest/auth
- [D-enum] https://cloud.google.com/identity-platform/docs/admin/email-enumeration-protection
- [D-verify] https://firebase.google.com/docs/auth/admin/verify-id-tokens
- [D-custom] https://firebase.google.com/docs/auth/admin/create-custom-tokens
- [D-claims] https://firebase.google.com/docs/auth/admin/custom-claims
- [D-rules] https://firebase.google.com/docs/rules/rules-and-auth
- [D-sessions] https://firebase.google.com/docs/auth/admin/manage-sessions
- [D-handler] https://firebase.google.com/docs/auth/custom-email-handler
- [D-templates] https://support.google.com/firebase/answer/7000714
- [D-actionlinks] https://firebase.google.com/docs/auth/admin/email-action-links
- [D-emu] https://firebase.google.com/docs/emulator-suite/connect_auth
- [D-pwpol] https://cloud.google.com/identity-platform/docs/password-policy
- [D-pwpolref] https://cloud.google.com/identity-platform/docs/reference/rest/v2/TopLevel/getPasswordPolicy
- [D-recap] https://cloud.google.com/identity-platform/docs/reference/rest/v2/TopLevel/getRecaptchaConfig
- [D-revoke] https://cloud.google.com/identity-platform/docs/reference/rest/v2/accounts/revokeToken
- [D-oob] https://cloud.google.com/identity-platform/docs/reference/rest/v1/accounts/sendOobCode
- [D-reset] https://cloud.google.com/identity-platform/docs/reference/rest/v1/accounts/resetPassword
- [D-authuri] https://cloud.google.com/identity-platform/docs/reference/rest/v1/accounts/createAuthUri
