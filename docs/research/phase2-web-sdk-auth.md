# Web SDK auth flows against a self-hosted server

Research notes for Phase 2 (Authentication). Gathered 2026-10-18 from public sources and local experiments only. The goal: an app on the official modular Firebase JS SDK (v11 to v13) signs up, signs in, and uses Google and Apple sign-in against Aruma with the smallest possible change, and ideally makes no requests to Google.

The source links point at firebase-js-sdk commit `d7bb0e1` (2026-10-15, `@firebase/auth` 1.13.7 = `firebase@13.0.0`). `SDK/` below is short for `https://github.com/firebase/firebase-js-sdk/blob/d7bb0e16f2b13ba15f74b690596eb70ef145d4f6/packages/`. The emulator reference is firebase-tools commit `1168863`, written as `FT/` for `https://github.com/firebase/firebase-tools/blob/11688637a1a210147fdcec0a1198045196b781cc/src/emulator/auth/`.

The experiments were bundled with esbuild and run in headless Chromium through Playwright 1.64. The app was served at `http://127.0.0.1:9177` and an Aruma stub at `http://127.0.0.1:9188` logged every request. All requests to other hosts were logged and aborted unless a test says otherwise. The same results came from built `firebase@11.10.0`, `@12.19.0` and `@13.0.0`: grepping each `@firebase/auth` ESM build finds the same host constants, handler and iframe paths, gapi URL, and emulator branches.

## 1. How the SDK picks its hosts

- **The hosts are hard-coded constants.** `DefaultConfig` holds `TOKEN_API_HOST = 'securetoken.googleapis.com'`, `API_HOST = 'identitytoolkit.googleapis.com'` and `API_SCHEME = 'https'`. [SDK/auth/src/core/auth/auth_impl.ts#L92-L96](https://github.com/firebase/firebase-js-sdk/blob/d7bb0e16f2b13ba15f74b690596eb70ef145d4f6/packages/auth/src/core/auth/auth_impl.ts#L92-L96)
- The auth component builds its config only from `app.options.apiKey` and `app.options.authDomain`, plus those constants. `apiKey` must be non-empty and must not contain `:`. [SDK/auth/src/core/auth/register.ts#L70-L86](https://github.com/firebase/firebase-js-sdk/blob/d7bb0e16f2b13ba15f74b690596eb70ef145d4f6/packages/auth/src/core/auth/register.ts#L70-L86)
- **`initializeAuth` cannot override them.** `Dependencies` has only `persistence`, `popupRedirectResolver` and `errorMap`. `firebaseConfig` has no `apiHost` field. [SDK/auth/src/model/public_types.ts#L1241-L1263](https://github.com/firebase/firebase-js-sdk/blob/d7bb0e16f2b13ba15f74b690596eb70ef145d4f6/packages/auth/src/model/public_types.ts#L1241-L1263)
- **How a request URL is built.** `_getFinalTarget` makes `${apiHost}${path}?key=…`. With `config.emulator` set, it returns `<emulatorUrl>` + that string, so `http(s)://host:port/identitytoolkit.googleapis.com/v1/accounts:signUp?key=…`. Otherwise it returns `${apiScheme}://…`. [SDK/auth/src/api/index.ts#L280-L308](https://github.com/firebase/firebase-js-sdk/blob/d7bb0e16f2b13ba15f74b690596eb70ef145d4f6/packages/auth/src/api/index.ts#L280-L308), [core/util/emulator.ts](https://github.com/firebase/firebase-js-sdk/blob/d7bb0e16f2b13ba15f74b690596eb70ef145d4f6/packages/auth/src/core/util/emulator.ts)
- **Refresh goes to `tokenApiHost`.** `requestStsToken` uses `tokenApiHost` with path `/v1/token`, a form-encoded body `grant_type=refresh_token&refresh_token=…`, and a snake_case response. [SDK/auth/src/api/authentication/token.ts](https://github.com/firebase/firebase-js-sdk/blob/d7bb0e16f2b13ba15f74b690596eb70ef145d4f6/packages/auth/src/api/authentication/token.ts)

### Ways to reach Aruma without modifying the SDK

**(a) `connectAuthEmulator(auth, url, { disableWarnings: true })`** ([SDK/auth/src/core/auth/emulator.ts#L45-L106](https://github.com/firebase/firebase-js-sdk/blob/d7bb0e16f2b13ba15f74b690596eb70ef145d4f6/packages/auth/src/core/auth/emulator.ts#L45-L106)). Everything this changes:

- **URL handling.** `http` and `https` are both accepted. Any path in the URL is dropped (`url = protocol//host:port/`), so **Aruma must serve these routes at the root of a host**. Mounting them under a path prefix does not work.
- **Must be called early.** It has to run before the first network call (`_canInitEmulator`). Calling it synchronously right after `getAuth()` works (verified by experiment).
- **App verification is disabled.** It sets `settings.appVerificationDisabledForTesting = true`. `RecaptchaVerifier` then uses a mock loader, and reCAPTCHA Enterprise returns the fake token `NO_RECAPTCHA`. [recaptcha_verifier.ts#L114](https://github.com/firebase/firebase-js-sdk/blob/d7bb0e16f2b13ba15f74b690596eb70ef145d4f6/packages/auth/src/platform_browser/recaptcha/recaptcha_verifier.ts#L114), [recaptcha_enterprise_verifier.ts#L40](https://github.com/firebase/firebase-js-sdk/blob/d7bb0e16f2b13ba15f74b690596eb70ef145d4f6/packages/auth/src/platform_browser/recaptcha/recaptcha_enterprise_verifier.ts#L40)
- **Origin validation is skipped.** The client no longer calls `getProjectConfig` to check authorized domains. [core/util/validate_origin.ts#L27-L31](https://github.com/firebase/firebase-js-sdk/blob/d7bb0e16f2b13ba15f74b690596eb70ef145d4f6/packages/auth/src/core/util/validate_origin.ts#L27-L31)
- **The handler and iframe move off `authDomain`.** They go to `<emulatorUrl>/emulator/auth/handler` and `/emulator/auth/iframe`. `authDomain` must still be non-empty. [core/util/handler.ts#L130-L136](https://github.com/firebase/firebase-js-sdk/blob/d7bb0e16f2b13ba15f74b690596eb70ef145d4f6/packages/auth/src/core/util/handler.ts#L130-L136), [platform_browser/iframe/iframe.ts#L53-L58](https://github.com/firebase/firebase-js-sdk/blob/d7bb0e16f2b13ba15f74b690596eb70ef145d4f6/packages/auth/src/platform_browser/iframe/iframe.ts#L53-L58)
- **Warnings.** Without `disableWarnings` the SDK logs `console.info("WARNING: You are using the Auth Emulator…")` and adds a fixed red `<p class="firebase-emulator-warning">` banner. With `disableWarnings: true`, neither appears. Verified by experiment: the banner is present with the default and absent with the flag.
- **Exposure.** `auth.emulatorConfig` becomes public: `{host, port, protocol, options}`.
- **What it does not change.** The client never checks token signatures in any mode (see §6). Cookies are sent (`credentials: 'include'`) only for `*.cloudworkstations.dev` hosts, so **REST calls arrive without cookies** ([api/index.ts#L185-L187](https://github.com/firebase/firebase-js-sdk/blob/d7bb0e16f2b13ba15f74b690596eb70ef145d4f6/packages/auth/src/api/index.ts#L185-L187)). The gapi script is still loaded (§2).
- **Documented emulator behaviour.** The docs say the emulator "issues unsigned ID tokens", that SDKs "disable these verification methods" (reCAPTCHA/APNs), and that popup and redirect use "a local version of the corresponding sign-in page". [connect_auth](https://firebase.google.com/docs/emulator-suite/connect_auth) None of this is enforced by the client against a real server. Aruma can issue signed RS256 tokens and the SDK will not notice either way.

**(b) Config fields.** There is no supported route. `Auth.config` is typed `readonly` but is a plain mutable object. Setting `auth.config.apiHost = 'aruma.example/identitytoolkit.googleapis.com'`, `tokenApiHost` and `apiScheme` right after `getAuth()` works (verified by experiment: the email/password flow hit the stub with `emulatorConfig: null`). This is undocumented and could break in any release. It also leaves the handler and iframe on `https://${authDomain}/__/auth/*`.

**(c) Automatic defaults (`__FIREBASE_DEFAULTS__`).** In the browser, `getAuth()` calls `connectAuthEmulator(auth, 'http://' + emulatorHosts.auth)` when defaults exist. [platform_browser/index.ts#L110-L113](https://github.com/firebase/firebase-js-sdk/blob/d7bb0e16f2b13ba15f74b690596eb70ef145d4f6/packages/auth/src/platform_browser/index.ts#L110-L113)

- **Where defaults come from.** In order: the postinstall-baked file, `globalThis.__FIREBASE_DEFAULTS__`, `process.env.__FIREBASE_DEFAULTS__`, or a base64-JSON cookie named `__FIREBASE_DEFAULTS__`. [SDK/util/src/defaults.ts](https://github.com/firebase/firebase-js-sdk/blob/d7bb0e16f2b13ba15f74b690596eb70ef145d4f6/packages/util/src/defaults.ts)
- **Not usable in production.** The scheme is hard-coded `http://`, which fails as mixed content on an `https` app. It also shows the banner, because `disableWarnings` is not passed.
- **Install-time call to Google.** The `@firebase/util` postinstall script calls `firebase.googleapis.com/.../webConfig` at npm-install time when `FIREBASE_WEBAPP_CONFIG` is set and `projectId` does not start with `demo-`. [SDK/util/postinstall.js](https://github.com/firebase/firebase-js-sdk/blob/d7bb0e16f2b13ba15f74b690596eb70ef145d4f6/packages/util/postinstall.js)

**(d) DNS or a TLS-intercepting proxy for `*.googleapis.com`.** This would need a trusted CA on every client device. It is not viable for public web apps, so it is not considered further.

**Conclusion.** The documented one-line change is:

```js
connectAuthEmulator(auth, "https://auth.<aruma-host>", { disableWarnings: true });
```

Everything else stays in `firebaseConfig`: `apiKey` and `projectId` from Aruma, plus a non-empty `authDomain` (only asserted in emulator mode).

### What Aruma must accept (verified by experiment)

- **Request format.** `POST /identitytoolkit.googleapis.com/v1/accounts:*?key=<apiKey>` with a JSON body. Bodies seen: `signUp`/`signInWithPassword` sent `{returnSecureToken:true, email, password, clientType:"CLIENT_TYPE_WEB"}`. Every sign-in was followed by `POST …/v1/accounts:lookup {idToken}`. Refresh is `POST /securetoken.googleapis.com/v1/token?key=…`, form-encoded.
- **CORS.** Requests are cross-origin. Preflights asked for `content-type,x-client-version,x-firebase-client,x-firebase-gmpid`, and the token endpoint asked for `x-client-version,x-firebase-gmpid`. The SDK can also send `X-Firebase-Locale` and `X-Firebase-AppCheck` ([api/index.ts#L47-L54](https://github.com/firebase/firebase-js-sdk/blob/d7bb0e16f2b13ba15f74b690596eb70ef145d4f6/packages/auth/src/api/index.ts#L47-L54)). Example `X-Client-Version`: `Chrome/JsCore/13.0.0/FirebaseCore-web`.
- **Error format.** Errors are parsed from `error.message` as `"CODE : detail"`, and the code is mapped to `auth/<kebab-code>`. Network failures are the only errors that do not sign the user out. [api/index.ts#L196-L255](https://github.com/firebase/firebase-js-sdk/blob/d7bb0e16f2b13ba15f74b690596eb70ef145d4f6/packages/auth/src/api/index.ts#L196-L255)
- **No Google traffic.** With connectAuthEmulator, the sign-up, sign-out, sign-in, `getIdToken(true)` and `authStateReady` flows made **zero non-local requests** on desktop Chromium, in both v11 and v13.

## 2. Popup and redirect: `/__/auth/handler` and `/__/auth/iframe`

### What the SDK sends to the handler

`_getRedirectUrl` builds `https://${authDomain}/__/auth/handler?…`, or `<emulator>/emulator/auth/handler?…` in emulator mode. [core/util/handler.ts#L66-L128](https://github.com/firebase/firebase-js-sdk/blob/d7bb0e16f2b13ba15f74b690596eb70ef145d4f6/packages/auth/src/core/util/handler.ts#L66-L128)

- **Query parameters:** `apiKey`, `appName`, `authType` (`signInViaPopup|signInViaRedirect|linkViaPopup|linkViaRedirect|reauthViaPopup|reauthViaRedirect`), `redirectUrl` (the current page URL), `v` (SDK version), `eventId`, `providerId`, `customParameters` (JSON), `scopes` (comma-separated) and `tid`. Undefined values are removed.
- **Fragment:** `#fac=<AppCheck token>` when App Check is present.
- **Observed popup URL:** `/emulator/auth/handler?apiKey=…&appName=[DEFAULT]&authType=signInViaPopup&redirectUrl=http://127.0.0.1:9177/…&v=13.0.0&eventId=4760880973&providerId=google.com&customParameters={"prompt":"select_account"}&scopes=profile,email`. `GoogleAuthProvider` adds `profile` by default.
- **Redirect differences (observed):** there was no `eventId` and `scopes=profile`.

### The iframe is loaded through gapi

- `getIframeUrl` builds `https://${authDomain}/__/auth/iframe?apiKey&appName&v[&eid=p][&fw]`. `eid=p` is sent whenever `apiHost` is the production constant, which includes emulator mode. [iframe.ts#L44-L75](https://github.com/firebase/firebase-js-sdk/blob/d7bb0e16f2b13ba15f74b690596eb70ef145d4f6/packages/auth/src/platform_browser/iframe/iframe.ts#L44-L75)
- The iframe is opened with **`gapi.iframes`**. The SDK loads `https://apis.google.com/js/api.js?onload=…` unless `window.gapi.iframes.Iframe` already exists. The URL is a module constant set via `_setExternalJSProvider`, with no public override. [iframe/gapi.ts#L60-L100](https://github.com/firebase/firebase-js-sdk/blob/d7bb0e16f2b13ba15f74b690596eb70ef145d4f6/packages/auth/src/platform_browser/iframe/gapi.ts#L60-L100), [platform_browser/index.ts#L140-L143](https://github.com/firebase/firebase-js-sdk/blob/d7bb0e16f2b13ba15f74b690596eb70ef145d4f6/packages/auth/src/platform_browser/index.ts#L140-L143)
- **The parent's side of the iframe protocol** ([popup_redirect.ts#L130-L177](https://github.com/firebase/firebase-js-sdk/blob/d7bb0e16f2b13ba15f74b690596eb70ef145d4f6/packages/auth/src/platform_browser/popup_redirect.ts#L130-L177)):
  - `iframe.ping()` must answer within 5 s (15 s in some cases), or the SDK fails with `network-request-failed`.
  - The parent registers an `authEvent` handler. It expects `{authEvent}` and answers `{status:'ACK'|'ERROR'}`.
  - For popups it sends `webStorageSupport` and expects `[{webStorageSupport:true}]`.
- **Verified by experiment:** with Google hosts blocked, `signInWithPopup` failed with `auth/internal-error` before Aruma got any request. The only attempted Google request was `apis.google.com/js/api.js`. With Google allowed, both the parent and the Aruma-served iframe loaded `apis.google.com/js/api.js` plus one `apis.google.com/_/scs/abc-static/…gapi.lb…` chunk. Nothing else went to Google.
- **Loaded at startup on mobile and Safari.** `getAuth()` installs `browserPopupRedirectResolver`. On mobile browsers, Safari and iOS, `_shouldInitProactively` is true, so the iframe (and gapi) load during auth initialization, before any sign-in. [auth_impl.ts#L184-L192](https://github.com/firebase/firebase-js-sdk/blob/d7bb0e16f2b13ba15f74b690596eb70ef145d4f6/packages/auth/src/core/auth/auth_impl.ts#L184-L192), [popup_redirect.ts#L179-L182](https://github.com/firebase/firebase-js-sdk/blob/d7bb0e16f2b13ba15f74b690596eb70ef145d4f6/packages/auth/src/platform_browser/popup_redirect.ts#L179-L182)
  - Verified by experiment: with an iPhone Safari user agent, `getAuth()` + `authStateReady()` requested `apis.google.com/js/api.js`. With a desktop Chrome user agent, nothing went to Google.
  - Failure is swallowed, so auth still works.
  - On desktop the iframe loads only for popup or redirect, or when a `firebase:pendingRedirect:<apiKey>:<appName>` flag exists in sessionStorage.

**Consequence: "zero requests to Google" cannot be met for popup or redirect with config changes alone.** The parent page always pulls gapi from `apis.google.com`. A self-hosted iframe can speak the gapi protocol, but only if it also loads gapi; the firebase-tools emulator iframe does exactly that ([FT/handlers.ts#L240-L320](https://github.com/firebase/firebase-tools/blob/11688637a1a210147fdcec0a1198045196b781cc/src/emulator/auth/handlers.ts#L240-L320)). Ways to reach zero, each requiring an app-side change:

- A `<script>` served by Aruma, loaded before the SDK, that defines a `window.gapi.iframes` shim (`getContext().open/register/send/ping/restyle`, `CROSS_ORIGIN_IFRAMES_FILTER`). The SDK then skips the Google load. Its own iframe page would speak plain `postMessage`. Feasible, but it is a second line and a reverse-engineered contract.
- `initializeAuth` without `popupRedirectResolver`, then `signInWithCredential` using an ID token from the provider's own JS. This means Google Identity Services or Apple JS, which are themselves loaded from Google or Apple.

### The AuthEvent and how the handler delivers it

The `AuthEvent` is `{type, eventId, urlResponse, sessionId, postBody, tenantId, error?}`. [model/popup_redirect.ts](https://github.com/firebase/firebase-js-sdk/blob/d7bb0e16f2b13ba15f74b690596eb70ef145d4f6/packages/auth/src/model/popup_redirect.ts)

**How the `AuthEventManager` filters events** ([core/auth/auth_event_manager.ts](https://github.com/firebase/firebase-js-sdk/blob/d7bb0e16f2b13ba15f74b690596eb70ef145d4f6/packages/auth/src/core/auth/auth_event_manager.ts)):

- **Matching:** an event goes to consumers whose filter includes `type` and whose `eventId` matches; a `null` consumer `eventId` matches any.
- **Duplicates:** events are de-duplicated for 10 minutes by `type-eventId-sessionId-tenantId`.
- **Errors:** an `error.code` of the form `auth/<code>` becomes that error.
- **No event:** `{type:'unknown', error:{code:'auth/no-auth-event'}}` means "no pending redirect". The iframe should send this on load when it has nothing.

**How the emulator handler delivers the event** ([FT/widget_ui.ts](https://github.com/firebase/firebase-tools/blob/11688637a1a210147fdcec0a1198045196b781cc/src/emulator/auth/widget_ui.ts), [FT/handlers.ts](https://github.com/firebase/firebase-tools/blob/11688637a1a210147fdcec0a1198045196b781cc/src/emulator/auth/handlers.ts)):

- **Popup.** It loops over `window.opener.frames`, finds the same-origin iframe whose `apiKey`/`appName` match, and `postMessage`s `{eventType:'sendAuthEvent', data:{authEvent, storageKey}}`. The iframe forwards it to the parent via `gapi … send('authEvent', …)`.
- **Redirect.** It writes `sessionStorage['firebase:redirectEvent:<apiKey>:<appName>']`, then navigates to `redirectUrl`. After the app reloads, the iframe (same origin as the handler) reads the stored event and sends it.

**What Google's hosted handler does.** Production `/__/auth/handler` and `/__/auth/iframe` on `*.firebaseapp.com` are tiny HTML pages that load `handler.js`/`experiments.js` and `iframe.js` from the same path. The handler page contains `var POST_BODY = '{{POST_BODY}}'`, which Hosting fills with the IdP's POST body. Verified by fetching `https://fir-ui-demo-84a6c.firebaseapp.com/__/auth/{handler,iframe,handler.js}`. The minified `handler.js` shows:

- `createAuthUri {providerId, continueUri, customParameter, oauthScope, sessionId}`, with `authFlowType:"CODE_FLOW"` added for `google.com`;
- a `getProjectConfig` lookup;
- storage keys `authEvent` (localStorage) and `redirectEvent` (sessionStorage).

The real handler also calls Identity Toolkit and recaptcha hosts, so self-hosting Google's files does not remove Google traffic. Firebase documents self-hosting these files: `handler`, `handler.js`, `experiments.js`, `iframe`, `iframe.js`, `links`, `links.js`, plus `/__/firebase/init.json`. It notes this "doesn't work for Apple sign-in or SAML". [redirect-best-practices](https://firebase.google.com/docs/auth/web/redirect-best-practices)

### From AuthEvent to signInWithIdp

`AbstractPopupRedirectOperation.onAuthEvent` maps the event to an `IdpCredential`. It calls `POST /v1/accounts:signInWithIdp {requestUri: urlResponse, sessionId, postBody, tenantId, pendingToken, returnSecureToken:true, returnIdpCredential:true}`, plus `idToken` for link or reauth. [abstract_popup_redirect_operation.ts](https://github.com/firebase/firebase-js-sdk/blob/d7bb0e16f2b13ba15f74b690596eb70ef145d4f6/packages/auth/src/core/strategies/abstract_popup_redirect_operation.ts), [idp.ts#L69-L86](https://github.com/firebase/firebase-js-sdk/blob/d7bb0e16f2b13ba15f74b690596eb70ef145d4f6/packages/auth/src/core/strategies/idp.ts#L69-L86)

**Verified end to end by experiment.** A stub handler and an emulator-style iframe ran on Aruma's origin, with gapi allowed.

- **Google popup:** `signInWithIdp {"requestUri":"http://127.0.0.1:9188/emulator/auth/handler?state=s1&code=google-auth-code","sessionId":"stub-session-id","returnSecureToken":true,"returnIdpCredential":true}`. The SDK then called `accounts:lookup` and the sign-in resolved.
- **Apple popup with a simulated `form_post`:** `signInWithIdp {"requestUri":"http://127.0.0.1:9188/emulator/auth/handler","sessionId":"…","postBody":"code=apple-code&id_token=…&state=s1&user=%7B…%7D",…}`. The SDK then called `accounts:lookup` and the sign-in resolved.
- **Google redirect:** the handler went out and back, then the app reloaded. The iframe read the stored event, the SDK called `signInWithIdp` then `accounts:lookup`, and `getRedirectResult` returned `operationType:"signIn"`.

**Who owns `sessionId` and `requestUri`.** The handler chooses `sessionId` and `urlResponse` (Aruma's callback URL), so Aruma controls both ends. The documented semantics: `requestUri` is "the URL to which the IdP redirects the user back", and `sessionId` "is verified against that session ID to prevent session fixation attacks". [signInWithIdp](https://docs.cloud.google.com/identity-platform/docs/reference/rest/v1/accounts/signInWithIdp), [createAuthUri](https://docs.cloud.google.com/identity-platform/docs/reference/rest/v1/accounts/createAuthUri)

**The user record comes from `lookup`.** In the experiments `user.uid` was overwritten by the `localId` from `accounts:lookup`, because every sign-in reloads the user ([user_impl.ts#L325-L345](https://github.com/firebase/firebase-js-sdk/blob/d7bb0e16f2b13ba15f74b690596eb70ef145d4f6/packages/auth/src/core/user/user_impl.ts#L325-L345)). The `localId` from `signInWithIdp` and from `lookup` must agree.

**Third-party storage partitioning.** The redirect relay depends on the iframe reading sessionStorage written by the top-level handler. Browsers that partition third-party storage break this unless the app and `authDomain` are same-site; Chrome M115+ is affected. [redirect-best-practices](https://firebase.google.com/docs/auth/web/redirect-best-practices) The experiment passed only because `127.0.0.1:9177` and `:9188` are same-site. The cross-site case was **not tested**. Popups use `opener.frames` + `postMessage` and should not depend on storage (inferred, not verified cross-site).

## 3. Other endpoints and when they are called

All of these go to `apiHost`, so to Aruma once redirected ([api/index.ts#L56-L81](https://github.com/firebase/firebase-js-sdk/blob/d7bb0e16f2b13ba15f74b690596eb70ef145d4f6/packages/auth/src/api/index.ts#L56-L81)):

- **`GET /v1/projects?key=…` (`getProjectConfig`, expects `authorizedDomains`).** Called before opening a popup or redirect, but **only outside emulator mode**. [validate_origin.ts](https://github.com/firebase/firebase-js-sdk/blob/d7bb0e16f2b13ba15f74b690596eb70ef145d4f6/packages/auth/src/core/util/validate_origin.ts)
- **`GET /v2/recaptchaConfig`.** Called only from `initializeRecaptchaConfig(auth)` or phone auth. The email flows run reCAPTCHA only if a config is cached or the server returns `MISSING_RECAPTCHA_TOKEN`; Aruma never returns it. [recaptcha_enterprise_verifier.ts#L290-L415](https://github.com/firebase/firebase-js-sdk/blob/d7bb0e16f2b13ba15f74b690596eb70ef145d4f6/packages/auth/src/platform_browser/recaptcha/recaptcha_enterprise_verifier.ts#L290-L415)
  - If the config enables a provider and app verification is on, the SDK loads `https://www.google.com/recaptcha/enterprise.js?render=<siteKey>`.
  - **To keep it off:** return no enabled providers (`OFF`), or rely on emulator mode's mock token.
- **`GET /v1/recaptchaParams` and `www.google.com/recaptcha/api.js`.** Used for phone auth with a real `RecaptchaVerifier`. Emulator mode replaces it with a mock, so no script is loaded.
- **`GET /v2/passwordPolicy`.** Called on `validatePassword()`, or after a `PASSWORD_DOES_NOT_MEET_REQUIREMENTS` failure when a policy is already cached. [email_and_password.ts#L55-L70](https://github.com/firebase/firebase-js-sdk/blob/d7bb0e16f2b13ba15f74b690596eb70ef145d4f6/packages/auth/src/core/strategies/email_and_password.ts#L55-L70)
- **Google-hosted scripts.** Only gapi (`apis.google.com`, §2) and the two reCAPTCHA scripts above. `gstatic` is not referenced by the auth package (grep of `packages/auth/src`).
- **Cookie persistence (beta).** `browserCookiePersistence` reroutes the sign-in and token endpoints through `${location.origin}/__cookies__?finalTarget=…` on the **app's** origin, so a middleware there must forward them to Aruma. [cookie_storage.ts#L52-L60](https://github.com/firebase/firebase-js-sdk/blob/d7bb0e16f2b13ba15f74b690596eb70ef145d4f6/packages/auth/src/platform_browser/persistence/cookie_storage.ts#L52-L60)

## 4. Other uses of `authDomain`

- **Handler and iframe URLs**, always `https://` outside emulator mode, and the auth instance key `${authDomain}:${apiKey}:${appName}`.
- **Redirect gating.** Redirect handling at startup runs only if `authDomain` is set. [auth_impl.ts#L290](https://github.com/firebase/firebase-js-sdk/blob/d7bb0e16f2b13ba15f74b690596eb70ef145d4f6/packages/auth/src/core/auth/auth_impl.ts#L290)
- **Serialization.** It is included in `toJSON()`.
- **`/__/firebase/init.json`.** The auth SDK never fetches it (no reference in auth, app or util source). Firebase's docs say to keep it deployed for the hosted helper ([redirect-best-practices](https://firebase.google.com/docs/auth/web/redirect-best-practices)), and the real file returns the project's web config (fetched from the demo project above). Aruma should serve it on `authDomain` for compatibility with Google's helper files and Hosting-style auto-init, but the SDK does not need it.
- **Origin check on the event channel.** The SDK accepts events only through the gapi channel with `CROSS_ORIGIN_IFRAMES_FILTER` from the iframe it opened. It does not compare the event's origin with `authDomain` itself. That trust boundary lives inside gapi (inferred from source; not examined further).

## 5. Google and Apple through the handler

- **Google.** Google's handler asks Identity Toolkit for `authFlowType=CODE_FLOW` (observed in minified `handler.js`). The browser receives only an authorization code, and the server exchanges it at signInWithIdp time. Aruma's handler should do the same: authorization code, plus `state`, `nonce` and PKCE where supported. Exchanging the code with Google's token endpoint is a server-to-Google call, unavoidable when Google is the IdP, as is the user's visit to `accounts.google.com`.
- **Apple.**
  - Firebase registers `https://<authDomain>/__/auth/handler` as the Apple Return URL and requests `email name` scopes by default when one-account-per-email is set. [auth/web/apple](https://firebase.google.com/docs/auth/web/apple)
  - Apple requires `response_mode=form_post` when `name` or `email` is requested ([Apple forum 121760](https://developer.apple.com/forums/thread/121760); not confirmed in Apple's reference docs).
  - So Aruma's handler must **accept a cross-site POST**: `code`, `id_token`, `state`, and `user` only on first consent. It puts the raw body into `AuthEvent.postBody`, and `signInWithIdp` receives it (verified by experiment with a simulated POST).
  - The handler page should not require cookies set with `SameSite=Lax/Strict` on that POST. Keep handler state in sessionStorage, or bind it via `state`.
  - The `user` name JSON appears only on first sign-in ([auth/web/apple](https://firebase.google.com/docs/auth/web/apple)). Aruma must persist it at that moment.
  - For `signInWithCredential(OAuthProvider('apple.com').credential({idToken, rawNonce}))`, Aruma must check `sha256(rawNonce)` against the token's `nonce` claim. This describes the documented Firebase pattern; the nonce section of the page was not re-read here.

## 6. Persistence, refresh and client-side token checks

- **Persistence.** `getAuth()` tries indexedDB, then localStorage, then sessionStorage. The key is `firebase:authUser:<apiKey>:<appName>`, in database `firebaseLocalStorageDb`, store `firebaseLocalStorage`, with an 800 ms polling interval. [platform_browser/index.ts#L83-L92](https://github.com/firebase/firebase-js-sdk/blob/d7bb0e16f2b13ba15f74b690596eb70ef145d4f6/packages/auth/src/platform_browser/index.ts#L83-L92), [persistence_user_manager.ts#L36-L42](https://github.com/firebase/firebase-js-sdk/blob/d7bb0e16f2b13ba15f74b690596eb70ef145d4f6/packages/auth/src/core/persistence/persistence_user_manager.ts#L36-L42), [indexed_db.ts#L45-L47](https://github.com/firebase/firebase-js-sdk/blob/d7bb0e16f2b13ba15f74b690596eb70ef145d4f6/packages/auth/src/platform_browser/persistence/indexed_db.ts#L45-L47)
  - Because the key includes `apiKey`, switching to an Aruma API key starts with no saved sessions.
- **Lazy refresh.** `getIdToken()` refreshes when `now > expirationTime − 30 s`. `expirationTime` is the client clock plus `expiresIn` from the response, or `exp − iat` from the token if absent, so clock skew does not matter. [token_manager.ts#L31-L50](https://github.com/firebase/firebase-js-sdk/blob/d7bb0e16f2b13ba15f74b690596eb70ef145d4f6/packages/auth/src/core/user/token_manager.ts#L31-L50)
- **Proactive refresh (5 minutes before expiry).** This runs only when another product registers an internal token listener (Firestore, Storage, Functions and others via `addAuthTokenListener`). Network errors back off from 30 s to 16 min. [proactive_refresh.ts](https://github.com/firebase/firebase-js-sdk/blob/d7bb0e16f2b13ba15f74b690596eb70ef145d4f6/packages/auth/src/core/user/proactive_refresh.ts), [firebase_internal.ts#L54](https://github.com/firebase/firebase-js-sdk/blob/d7bb0e16f2b13ba15f74b690596eb70ef145d4f6/packages/auth/src/core/auth/firebase_internal.ts#L54)
- **Client token checks.**
  - `_parseToken` only base64url-decodes the payload. `exp` and `iat` must exist (`_tokenExpiresIn`), and `getIdTokenResult` also needs `auth_time`. `firebase.sign_in_provider` feeds `additionalUserInfo`.
  - **No signature, `iss` or `aud` check** happens in the client. [id_token_result.ts#L48-L130](https://github.com/firebase/firebase-js-sdk/blob/d7bb0e16f2b13ba15f74b690596eb70ef145d4f6/packages/auth/src/core/user/id_token_result.ts#L48-L130)
  - Server-side verifiers (Admin SDKs) do check `alg=RS256`, `kid`, `aud=projectId` and `iss=https://securetoken.google.com/<projectId>`. [verify-id-tokens](https://firebase.google.com/docs/auth/admin/verify-id-tokens)

## Decisions this implies for Aruma

1. **The documented line is `connectAuthEmulator(auth, "https://<aruma-auth-host>", { disableWarnings: true })`.** Serve `/identitytoolkit.googleapis.com/…` and `/securetoken.googleapis.com/…` at the **root** of that host over TLS, with CORS for the headers listed in §1. Do not rely on cookies for REST.
2. **Implement `/emulator/auth/handler` and `/emulator/auth/iframe`** for the emulator path. Also serve `/__/auth/handler`, `/__/auth/iframe` and `/__/firebase/init.json` on `authDomain`, for apps that change only config and accept Google REST hosts, or for a future proxy mode. Both handler paths need the same logic: real OAuth (Google code flow, Apple `form_post` POST), an AuthEvent built with Aruma-chosen `sessionId`, and delivery via `opener.frames` + `postMessage` for popups and sessionStorage + redirect for redirects.
3. **Re-implement the server-side checks the client skips.** Emulator mode drops authorized-domain checks and app verification, so Aruma's handler and `signInWithIdp` must validate `redirectUrl`/`requestUri` against authorized domains. They must also bind `sessionId` to `state`, and add SMS abuse protection that does not rely on reCAPTCHA.
4. **Make the "zero Google requests" goal honest.** It holds for email/password, custom-token, anonymous and refresh flows on desktop. Popup and redirect, and plain `getAuth()` on mobile or Safari, load `apis.google.com/js/api.js`. Document this, and offer an optional Aruma-hosted gapi-shim `<script>` as a second opt-in line (needs a separate design spike).
5. **Issue real RS256 tokens** with Firebase-shaped claims (`iss`, `aud`, `sub`, `user_id`, `auth_time`, `iat`, `exp`, `firebase.sign_in_provider`). Return `expiresIn`, and make `accounts:lookup` authoritative and consistent with sign-in responses.
6. **Recommend same-site `authDomain` and app** (or popups) to avoid storage partitioning breaking redirect.

## Open questions

- Can a self-hosted iframe talk to the parent's gapi without loading gapi itself? This would need the `gapi.iframes` wire format documented. And is a page-level gapi shim acceptable as the second line?
- Cross-site behaviour (app and Aruma on different registrable domains) of popup and redirect in Chrome, Safari and Firefox with partitioned storage. Not tested here.
- Does any popular library (FirebaseUI, AngularFire, ReactFire) branch on `auth.emulatorConfig` and change behaviour in production?
- Whether to support `browserCookiePersistence`'s `/__cookies__` proxy contract.
- The exact `createAuthUri` and `CODE_FLOW` semantics that Google's own `handler.js` relies on. These were seen only in minified code, which matters if Aruma ever serves Google's helper files unmodified.
