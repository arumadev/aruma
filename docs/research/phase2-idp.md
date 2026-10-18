# Google and Apple sign-in on the web (T15, T20)

Research and decisions for sign-in with Google and Apple through the official web SDK: popup, redirect and direct ID token, link, re-authentication and Apple token revocation. Gathered 2026-10-18. It builds on [phase2-web-sdk-auth.md](phase2-web-sdk-auth.md) §2 and §5 and uses its abbreviations: `SDK/` is firebase-js-sdk at `d7bb0e1` (`packages/`), `FT/` is firebase-tools at `1168863` (`src/emulator/auth/`). The emulator was read for behaviour only; Aruma's pages and code are its own.

## 1. The SDK's side of the protocol

**Handler URL.** `signInWithPopup` and `signInWithRedirect` open `<emulator URL>/emulator/auth/handler` after `connectAuthEmulator`, otherwise `https://<authDomain>/__/auth/handler` ([SDK/auth/src/core/util/handler.ts](https://github.com/firebase/firebase-js-sdk/blob/d7bb0e16f2b13ba15f74b690596eb70ef145d4f6/packages/auth/src/core/util/handler.ts)). Query: `apiKey`, `appName`, `authType` (`signInViaPopup|signInViaRedirect|linkViaPopup|linkViaRedirect|reauthViaPopup|reauthViaRedirect`), `redirectUrl` (the current page), `v`, `eventId` (popups only), `providerId`, `customParameters` (JSON), `scopes` (comma list), `tid`.

**Iframe URL.** `<emulator URL>/emulator/auth/iframe` or `https://<authDomain>/__/auth/iframe`, with `apiKey`, `appName`, `v`, `eid=p` and optionally `fw` ([SDK/auth/src/platform_browser/iframe/iframe.ts](https://github.com/firebase/firebase-js-sdk/blob/d7bb0e16f2b13ba15f74b690596eb70ef145d4f6/packages/auth/src/platform_browser/iframe/iframe.ts)). The parent opens it through `gapi.iframes` and ([SDK/auth/src/platform_browser/popup_redirect.ts](https://github.com/firebase/firebase-js-sdk/blob/d7bb0e16f2b13ba15f74b690596eb70ef145d4f6/packages/auth/src/platform_browser/popup_redirect.ts)):

- pings it (5 s timeout);
- registers `authEvent`, expecting `{authEvent}` and answering `{status: 'ACK'|'ERROR'}`;
- sends `webStorageSupport` and reads `result[0].webStorageSupport`.

**AuthEvent.** `{type, eventId, urlResponse, sessionId, postBody, tenantId, error}`. `error.code` of the form `auth/<code>` becomes that SDK error; `{type: 'unknown', error: {code: 'auth/no-auth-event'}}` means "nothing pending" ([SDK/auth/src/core/auth/auth_event_manager.ts](https://github.com/firebase/firebase-js-sdk/blob/d7bb0e16f2b13ba15f74b690596eb70ef145d4f6/packages/auth/src/core/auth/auth_event_manager.ts)).

**From event to REST.** The SDK posts `accounts:signInWithIdp {requestUri: urlResponse, sessionId, postBody, tenantId, pendingToken, returnSecureToken: true, returnIdpCredential: true}`, plus `idToken` for a link. Re-authentication sends no `idToken` and no `autoCreate`; the SDK then requires the returned `localId` to equal the user's uid and turns `auth/user-not-found` into `auth/user-mismatch` ([SDK/auth/src/core/strategies/idp.ts](https://github.com/firebase/firebase-js-sdk/blob/d7bb0e16f2b13ba15f74b690596eb70ef145d4f6/packages/auth/src/core/strategies/idp.ts)).

**Direct credentials.** `signInWithCredential(GoogleAuthProvider.credential(idToken))` and `OAuthProvider('apple.com').credential({idToken, rawNonce})` send `requestUri: 'http://localhost'` (a constant) and `postBody = id_token=…&providerId=…[&access_token=…][&nonce=<raw nonce>]` ([SDK/auth/src/core/credentials/oauth.ts](https://github.com/firebase/firebase-js-sdk/blob/d7bb0e16f2b13ba15f74b690596eb70ef145d4f6/packages/auth/src/core/credentials/oauth.ts)).

**Response handling.** A `needConfirmation` key throws `account-exists-with-different-credential`. A 200 body with `errorMessage` is an error. `FEDERATED_USER_ID_ALREADY_LINKED` and `EMAIL_EXISTS` keep the body as `customData`, which is how `credentialFromError` recovers the provider credential ([phase2-identity-toolkit.md](phase2-identity-toolkit.md) §1, §2).

**revokeAccessToken.** `POST /v2/accounts:revokeToken {providerId: 'apple.com', tokenType: 'ACCESS_TOKEN', token, idToken}` ([SDK/auth/src/core/auth/auth_impl.ts](https://github.com/firebase/firebase-js-sdk/blob/d7bb0e16f2b13ba15f74b690596eb70ef145d4f6/packages/auth/src/core/auth/auth_impl.ts), `revokeAccessToken`).

**How the emulator delivers the event** ([FT/handlers.ts](https://github.com/firebase/firebase-tools/blob/11688637a1a210147fdcec0a1198045196b781cc/src/emulator/auth/handlers.ts), [FT/widget_ui.ts](https://github.com/firebase/firebase-tools/blob/11688637a1a210147fdcec0a1198045196b781cc/src/emulator/auth/widget_ui.ts)):

- **Popup.** The handler walks `window.opener.frames`, finds the same-origin iframe whose `apiKey` and `appName` match, and posts `{eventType: 'sendAuthEvent', data: {authEvent, storageKey}}` to it. The iframe forwards it to the parent with `gapi … send('authEvent', …)`.
- **Redirect.** The handler writes `sessionStorage['firebase:redirectEvent:<apiKey>:<appName>']` and navigates to `redirectUrl`. The iframe reads and clears the key when the app reloads, or sends "no event".

## 2. Aruma's flow

**Routes.** `/__/auth/handler`, `/__/auth/iframe`, `/emulator/auth/handler`, `/emulator/auth/iframe` and `/__/firebase/init.json`. The entry point sends all of `/__/` and `/emulator/` to the Auth router, where nothing else is registered, so every other `/emulator/*` path is 404 (ADR-014).

**Starting a sign-in.** The handler page receives the SDK's query and checks it:

- `authType`, `appName` and the API key must be valid, and `tid` must be empty;
- the provider must be enabled;
- `redirectUrl` must be on an authorized domain.

It then creates an `auth.idp_sessions` row (migration `00003_idp.sql`) that lives 10 minutes and holds:

- `state` (32 random bytes): goes to the provider and back. Stored as an HMAC.
- `sessionId` (32 random bytes): stays in the starting tab's `sessionStorage`, under `aruma:idp:<state>`. Stored as an HMAC.
- `nonce`: the provider receives `hex(sha256(nonce))`, and its ID token must carry exactly that.
- For Google, a PKCE verifier, envelope-encrypted with AAD `auth.idp_sessions|code_verifier_enc|<hex state hash>`. Google's discovery document lists `S256` ([accounts.google.com/.well-known/openid-configuration](https://accounts.google.com/.well-known/openid-configuration)). Apple's lists no PKCE ([appleid.apple.com/.well-known/openid-configuration](https://appleid.apple.com/.well-known/openid-configuration)), so Apple gets none.
- The provider `redirect_uri` (`<page origin>/__/auth/handler`), the app's `redirectUrl`, `authType`, `eventId`, `appName` and `apiKey`.

Then the page stores the sessionId and replaces itself with the provider's URL.

- **Google:** `response_type=code`, scopes `openid email profile` plus those requested, `nonce`, PKCE `S256`.
- **Apple:** `response_type=code`, `response_mode=form_post`, `scope=name email`, `nonce`.
- **Custom parameters:** only an allow-list passes through (Google: `prompt`, `login_hint`, `hd`, `hl`, `include_granted_scopes`, `access_type`; Apple: `locale`), so an app cannot override `client_id`, `redirect_uri`, `state` or `nonce`.

**The callback.** Google answers with a GET carrying `state` and `code`; Apple with a cross-site form POST carrying `code`, `state` and `user` (first consent only). The handler looks the session up by state without redeeming it, then renders the page data. The page script:

- reads and removes the sessionId from `sessionStorage`;
- builds the AuthEvent: `urlResponse` is the callback URL for GET, or the handler URL with `postBody` set to the raw POST body;
- delivers it: for popups, `postMessage` to the same-origin `/auth/iframe` frame of the opener whose `apiKey`/`appName` match, with target origin = Aruma's origin; for redirects, `sessionStorage` and then a navigation to the `redirectUrl` stored at start.

A provider error (`access_denied`, `user_cancelled_authorize`) becomes `auth/user-cancelled`. An unknown or used state shows an error page and delivers nothing.

**Redeeming.** `signInWithIdp` with a state marks the session used (`UPDATE … WHERE used_at IS NULL AND expires_at > now()`), then checks the HMAC of `sessionId`. A wrong sessionId burns the session too, so neither value can be guessed at leisure. It then checks:

- `requestUri` has the scheme, host and path of the stored callback;
- the code is exchanged server-to-server (Google: client secret plus PKCE verifier; Apple: an ES256 client secret JWT, `iss` = team, `sub` = Services ID, `aud` = `https://appleid.apple.com`, `kid` = key ID, 5-minute lifetime) ([Apple: Creating a client secret](https://developer.apple.com/documentation/accountorganizationaldatasharing/creating-a-client-secret));
- the returned ID token is verified (§4), and its `nonce` must equal the session's.

**Direct ID tokens** need no session. The token is verified the same way. If either the token or the request carries a nonce, `sha256(rawNonce)` must equal the token's `nonce` ([Firebase: Apple on the web](https://firebase.google.com/docs/auth/web/apple)). The constant `requestUri` (`http://localhost`) is not a trust decision, because nothing is redirected.

**createAuthUri for a provider** starts the same kind of session with `continueUri` as the provider's `redirect_uri`. `continueUri` must be on an authorized domain. The call returns `{authUri, providerId, sessionId}`; the caller keeps the sessionId and later sends it to `signInWithIdp`.

**Authorized domains** are the project's list plus its `authDomain` and Aruma's public host. Hosts match exactly, with any port, as in Firebase.

**Page security.**

- Both pages are static. The request data travels in a `<script type="application/json">` block (JSON-escaped, so it cannot close the element).
- The handler CSP is `default-src 'none'; script-src 'sha256-…'; style-src 'sha256-…'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'`, with `X-Frame-Options: DENY`, `Referrer-Policy: no-referrer` (the code never leaks in a Referer) and `Cache-Control: no-store`.
- The handler sets `Cross-Origin-Opener-Policy: unsafe-none`. The entry point's `same-origin` default would cut the popup off from its opener, which is on the app's origin.
- The iframe page has no `X-Frame-Options`. Its `frame-ancestors` lists `http://<d>:*` and `https://<d>:*` for each authorized domain `d`, or `'none'` without a valid API key: the iframe hands sign-in results to whoever embeds it.
- Its `script-src` is its own hash plus `https://apis.google.com` (§6). The acceptance test hashes every inline block of both pages against the CSP.

**Storage partitioning.** The redirect relay needs the iframe (third-party when the app is on another site) to read `sessionStorage` written by the top-level handler. Partitioned storage breaks that unless the app and Aruma are same-site ([redirect best practices](https://firebase.google.com/docs/auth/web/redirect-best-practices)). Popups use `postMessage` and do not depend on it. The browser run below confirms both.

## 3. Accounts (one account per email)

| Case | Result |
|---|---|
| The provider identity is linked to an account | Sign in to it |
| The provider vouches for the email (`email_verified`) and an account has it | Sign in to that account and link the provider. If that account's email was **unverified**, its password and other providers are removed and its sessions revoked first (takeover protection: whoever registered the address first loses the way in), and the name and photo are replaced with the provider's. Then `emailVerified = true` |
| An account has the email, but the provider does not vouch for it | `needConfirmation: true` with the provider fields and credential, and no tokens or `localId`. `verifiedProvider` lists the account's providers only when enumeration protection is off |
| Neither | New account with the provider's email (and its verified state), name and photo. `ADMIN_ONLY_OPERATION` when client sign-up is off. `USER_NOT_FOUND` for a re-authentication flow (session `authType` `reauth*`) or `autoCreate: false` |
| `idToken` present (link) | `FEDERATED_USER_ID_ALREADY_LINKED` if another account has the identity; `PROVIDER_ALREADY_LINKED` if the user has another identity of that provider; `EMAIL_EXISTS` if another account has the email. Otherwise the provider is added: the uid is kept (an anonymous user is upgraded in place), the email is set if the user had none, and it is marked verified if the provider vouches for the same address |

These follow the emulator's `signInWithIdp`, `handleIdpSigninEmailRequired` and `handleLinkIdp` ([FT/operations.ts](https://github.com/firebase/firebase-tools/blob/11688637a1a210147fdcec0a1198045196b781cc/src/emulator/auth/operations.ts)), with three defensive changes:

- The emulator returns the existing account's `localId` with `needConfirmation`, which tells a stranger which account holds an address. Aruma never does, and hides `verifiedProvider` under enumeration protection.
- Profile fields are replaced only on takeover protection; otherwise only empty ones are filled.
- Re-authentication creates no account. The SDK would report `user-mismatch` anyway, but the emulator creates the stray account first.

With `returnIdpCredential`, `FEDERATED_USER_ID_ALREADY_LINKED` and `EMAIL_EXISTS` come back as HTTP 200 with `errorMessage` and the provider fields, as the emulator does, so `credentialFromError` works. Other errors use the normal 400 envelope.

**Response fields:** `kind`, `providerId`, `federatedId` (`https://accounts.google.com/<sub>` for Google, `<sub>` for Apple, as the emulator does), `localId`, `email`, `emailVerified`, `firstName`, `lastName`, `fullName`, `displayName`, `photoUrl`, `rawUserInfo` (Google: the userinfo shape; Apple: the verified token claims), `oauthIdToken`, `oauthAccessToken`, `oauthExpireIn`, `idToken`, `refreshToken`, `expiresIn`, `isNewUser`. The ID token's `firebase.sign_in_provider` is the provider id.

## 4. Verifying provider tokens

- **Algorithm.** RS256 with a `kid` only, through the same strict codec as Aruma's own tokens (`tokens.KidOf`, `tokens.VerifyWith`).
- **Keys.** From the provider's JWKS (Google `https://www.googleapis.com/oauth2/v3/certs`, Apple `https://appleid.apple.com/auth/keys`, per the discovery documents above). They are cached for the response's `max-age`, clamped to 5 minutes to 24 hours. An unknown `kid` refetches at most once a minute. Keys under 2048 bits are ignored. When the provider is unreachable, known keys keep working.
- **Issuer.** `https://accounts.google.com` or `accounts.google.com` for Google ([Google OpenID Connect](https://developers.google.com/identity/openid-connect/openid-connect)), `https://appleid.apple.com` for Apple.
- **Audience.** It must contain the configured client ID (Google web client ID, Apple Services ID); with several audiences, `azp` must be that ID. A Google token minted for another client is refused.
- **Time.** `exp` and `iat` allow 60 s of skew.
- **Claims.** Apple sends `email_verified` as `"true"`, which is accepted like `true`. Apple's `user` POST field is unsigned, so only the name is taken from it, never the email.
- **Errors.** Every failure is `INVALID_IDP_RESPONSE : <reason>` (`auth/invalid-credential`), except the nonce (`MISSING_OR_INVALID_NONCE`).

**Tokens from Google on Aruma's own endpoints** (the "Google iss" tamper case) fail Aruma's issuer and key checks: `accounts:lookup` and the `idToken` of `signInWithIdp` answer `INVALID_ID_TOKEN`.

## 5. Apple token revocation

App Store review requires apps that support account creation to let users delete their account, and Sign in with Apple tokens to be revoked when they do ([App Review Guidelines 5.1.1(v)](https://developer.apple.com/app-store/review/guidelines/#data-collection-and-storage); [Apple: Revoke tokens](https://developer.apple.com/documentation/sign_in_with_apple/revoke_tokens)).

- **Kept token.** The refresh token from Apple's code exchange is kept in `auth.idp_tokens`, envelope-encrypted with AAD `auth.idp_tokens|refresh_token_enc|<project>/<tenant>/<uid>/apple.com`. It cascades with the provider link.
- **On delete.** Deleting an account (client or Admin) reads the token in the same call and revokes it at Apple after the delete commits. This happens even if Apple sign-in was since disabled. A failure is logged and audited, never returned, because the account is already gone.
- **On request.** `POST /v2/accounts:revokeToken` revokes the given `ACCESS_TOKEN` or `REFRESH_TOKEN` for a signed-in user who has Apple linked. It answers `{}`.

## 6. What loads from Google

This is ADR-013's declared exception for popup and redirect only:

| Who | Requests to Google | Why |
|---|---|---|
| The app page (the SDK) | `https://apis.google.com/js/api.js`, then one `https://apis.google.com/_/scs/abc-static/_/js/k=gapi.lb…` chunk | The SDK opens its iframe with `gapi.iframes` and loads gapi unless `window.gapi.iframes.Iframe` exists ([SDK/auth/src/platform_browser/iframe/gapi.ts](https://github.com/firebase/firebase-js-sdk/blob/d7bb0e16f2b13ba15f74b690596eb70ef145d4f6/packages/auth/src/platform_browser/iframe/gapi.ts)). Aruma cannot change this without changing the app |
| Aruma's iframe page | The same two scripts | The parent talks gapi's own cross-frame RPC to the iframe. Its wire format is undocumented, so the iframe uses gapi's `getParentIframe().register/send`, as the emulator's does |
| The handler page | none | Plain script, no third-party resources |
| Google sign-in itself | `accounts.google.com` (the user's browser), `oauth2.googleapis.com/token` and `www.googleapis.com/oauth2/v3/certs` (Aruma's server) | Google is the provider |

**Why not avoid it.** The only way to avoid gapi in the iframe is to reimplement gapi's private RPC protocol. That is undocumented, could change in any gapi release, and would remove nothing anyway, because the parent page still loads the same script from Google. Email/password, anonymous, custom-token and refresh flows stay at zero requests to Google (ADR-013). Zero requests for popups would need an Aruma-served `window.gapi.iframes` shim loaded before the SDK, which is a second line in the app (open, below).

## 7. Tests, and what needs the real providers

**Run in CI** (`go test ./...`):

- `test/acceptance/auth_idp_test.go`. A fake OpenID provider (`httptest`, its own RSA key and JWKS) stands in for Google and Apple. The server reaches it only through the test-only `app.WithIdPEndpoints` option; no configuration or request can change the provider endpoints. The suite covers:
  - Google popup and redirect, and Apple `form_post`, through the handler pages at the HTTP level (start page data, provider redirect, callback page, the AuthEvent the script would build), then `signInWithIdp` v1 and v3;
  - direct ID tokens, and Apple's nonce;
  - linking, takeover protection, `needConfirmation`, re-authentication without account creation, anonymous upgrade;
  - `revokeToken`, and revocation on delete;
  - tampering: replayed state, swapped state and sessionId, missing sessionId, wrong nonce, token for another client (direct and by code), foreign issuer, unknown key, expired token, a Google token on Aruma's endpoints, `requestUri` on another host, unauthorized `redirectUrl` and `continueUri`, `/emulator/*` 404s, disabled provider;
  - CSP hashes, iframe `frame-ancestors`, `init.json`, the console provider endpoints, and no raw secrets in the database.
- `internal/identity/core/idp_keys_test.go`: JWKS caching (rotation, refetch limit, weak keys, provider down) and claim shapes.

**Run by hand in a real browser**: `test/experiments/idp-browser`, driven by `go test -tags browser -run TestAuthIdPBrowser -v ./test/acceptance/` after `npm ci` there. It needs network access to `apis.google.com`. It loads the official `firebase@13.0.0` in the full Chromium build in its new headless mode (Playwright 1.64, `channel: 'chromium'`; not the default headless shell, see "Flaky first popup" below) against Aruma and the fake providers, which consent at once, and runs:

- Google popup, Apple popup (`form_post`) and Google redirect;
- anonymous then `linkWithPopup`;
- `reauthenticateWithPopup`.

It reports every non-local request and every CSP violation. The results of the run on 2026-10-18 are under "Browser run" below.

**Not testable locally:**

- **Real Google.** It needs an OAuth web client ID and secret from a Google Cloud project, with `<redirectUri>` from the console registered as an authorized redirect URI. Google accepts `http://localhost` redirect URIs for testing, so this works on a laptop with real credentials.
- **Real Apple.** It needs an Apple Developer account, a Services ID with Sign in with Apple and its Return URL, a key (`.p8`) and the team ID. Apple accepts only **https** Return URLs on a registered domain (no IP addresses, no localhost), so it needs a public https host for Aruma, which also makes it AC-A9's "when an https domain is available".
- Cross-site behaviour in Safari and Firefox, whose storage partitioning differs from Chromium's.

**Manual check with the real providers:**

1. Run Aruma on an https host (for Apple) and open the project in the console. Copy the redirect URI from **Sign-in methods**.
2. Google: in Google Cloud console → APIs & Services → Credentials, create an OAuth client ID of type *Web application*. Add the redirect URI. Paste the client ID and secret into Aruma and enable Google.
3. Apple: in Certificates, Identifiers & Profiles, create a Services ID with Sign in with Apple, set the domain and the Return URL (the redirect URI), and create a key with Sign in with Apple. Paste the Services ID, team ID, key ID and the `.p8` contents into Aruma and enable Apple.
4. Add the app's host to the authorized domains. In the app: `connectAuthEmulator(auth, "<aruma URL>", {disableWarnings: true})`, then try `signInWithPopup` and `signInWithRedirect` with `GoogleAuthProvider` and `OAuthProvider('apple.com')`, `linkWithPopup` from an anonymous user, and `reauthenticateWithPopup`.
5. Delete the Apple user with `deleteUser`. The auth audit log should show `idp.revoke ok` with `onDelete`, and the app should disappear from the Apple ID's "Sign in with Apple" list.

## Browser run

`TestAuthIdPBrowser` on 2026-10-18 (Chromium 1248 headless shell, `firebase@13.0.0`, fake providers consenting at once):

| App page | Google popup | Apple popup | Google redirect | Anonymous → `linkWithPopup` | `reauthenticateWithPopup` |
|---|---|---|---|---|---|
| Same site as Aruma (`127.0.0.1`) | ✓ | ✓ (form_post, name kept) | ✓ | ✓ uid kept, `operationType: link` | ✓ |
| Other site (`localhost`), Playwright default (partitioning off) | ✓ | ✓ | ✓ | ✓ | ✓ |
| Other site, third-party storage partitioning on, as in Chrome | ✓ | ✓ | ✗ `getRedirectResult` → `null` | ✓ | ✓ |

- **Requests to Google.** In every run the only non-local requests were `https://apis.google.com/js/api.js` and `https://apis.google.com/_/scs/abc-static/_/js/k=gapi.lb…/m=gapi_iframes/…` (one build for the app page, one for the iframe). There were no CSP violations.
- **Error reports blocked.** When gapi hits an error inside the iframe it tries to report it to `https://apis.google.com/_/jserror`, and the iframe CSP blocks that. This was seen while debugging.
- **Two bugs the run found, both fixed:**
  - The fake provider answers in milliseconds, so the handler page could finish before the SDK's iframe existed. The handler now retries the relay for up to 10 s.
  - A popup result could reach the iframe before gapi had connected. The emulator's iframe stores such an event and sends it as its first message, which takes the place of the "no event" answer that the SDK's startup redirect check waits for. The sign-in then never proceeds. Aruma's iframe always sends the stored redirect result or "no event" first, and holds early popup results in memory until after it.
- **Partitioned storage across sites.** The redirect relay cannot work: the top-level handler and the iframe on the app's site get different `sessionStorage` partitions. The fix is the one Firebase documents for the same problem: put Aruma (the `connectAuthEmulator` URL, or the custom `authDomain`) on the app's site, for example `auth.example.com` for `app.example.com`, or use popups.

### Flaky first popup in the headless shell (2026-10-18)

**Symptom.** On cross-site runs, the first Google popup of a run sometimes never settled. Agent D saw it in 3 of 4 runs, then in 5 of 5. Every later popup in the same run passed. The SDK answered the AuthEvent with `ERROR`.

**Cause.** The test browser, not Aruma or the SDK. Logging inside the SDK's event manager and Aruma's iframe showed the failing runs always had the same order:

1. The SDK's gapi ping resolved and the popup consumer registered.
2. Only then was Aruma's relay iframe navigated (in passing runs it was navigated first).
3. The iframe's document then ran no script at all, not even its first line, until the *next* `signInWithPopup` called `window.open` about 30 s later.

By then the first popup operation had been cancelled (`auth/cancelled-popup-request`) or had timed out (`auth/popup-closed-by-user`), so its consumer was gone and the late event got `ERROR`.

**What ruled things out:**
- Turning site isolation off did not help.
- Neither did opening another tab or closing the popup.
- Same-site runs, where the iframe shares the app's process, never failed.

**Fix.** Running the same code in the full Chromium binary (new headless, Chrome's own code path) removed the failure. The control run, with the default `chrome-headless-shell` and identical code, failed 3 of 4. After switching `run.mjs` to `channel: 'chromium'`: cross-site 10/10, cross-site-partitioned 5/5, same-site 3/3. Production Firebase always runs this flow with a cross-site iframe on `*.firebaseapp.com`, which supports reading the failure as a headless-shell artefact rather than a browser behaviour apps meet.

**For developers.** If your own Playwright tests drive `signInWithPopup` against Aruma, launch with `channel: 'chromium'` (or real Chrome), not the default headless shell.

## Open

- **Optional gapi shim** (a second, opt-in line) for zero Google requests in popup and redirect flows; it needs gapi's iframe RPC documented first.
- **Apple's server-to-server notifications** (`consent-revoked`, `account-delete`, email changes) are not handled.
- **Unlinking Apple** (`deleteProvider`) drops the kept refresh token without revoking it at Apple. Only account deletion revokes.
- **Other client IDs.** Google tokens minted for the project's Android or iOS client IDs are refused. Only the configured web client ID is accepted, until a list of extra client IDs is added.
