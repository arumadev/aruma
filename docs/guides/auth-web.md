# Connecting a web app to Aruma Auth

Your app uses the official `firebase` package as it is, with the config file from Aruma. There are two ways to connect:

| Mode | What you add | When |
|---|---|---|
| **Production mode (recommended)** | The `@aruma/web` package and one line in `vite.config`. **Nothing in the app code** | Always, and especially in production |
| Emulator-function mode | One `connectAuthEmulator` line after `getAuth()`, with Aruma's address | Development, or a project whose build tool you cannot change |

## Production mode (recommended)

The SDK stays in its normal mode, exactly as with Firebase. `@aruma/web` redirects only the Auth requests to your server.

1. **Install the package:** `npm install --save-dev /path/to/aruma/sdk/web/aruma-web`, using your clone of this repository. The package is not published to npm yet.
2. **Add the line to `vite.config`:**

```js
import { defineConfig } from 'vite';
import aruma from '@aruma/web/vite';

export default defineConfig({
  plugins: [aruma({ url: 'https://aruma.example.com' })], // your server's public_url
});
```

3. **The app does not change:** `initializeApp(firebaseConfig)` with the Aruma config file, then `getAuth(app)`, with no `connectAuthEmulator`.

**Warning: Cloud Functions.** Do not call Cloud Functions with `httpsCallable` from an app that signs in with Aruma Auth. Aruma does not serve Functions, so the SDK sends the user's Aruma ID token to Google. The package refuses requests to Google's Storage and Firestore hosts, but not to Functions. See [known differences](../reference/known-differences.md).

**How it works:**
- Aruma serves Google's own paths under its domain (ADR-014), so the redirect only swaps the host:
  - `https://identitytoolkit.googleapis.com/<path>` becomes `<url>/identitytoolkit.googleapis.com/<path>`.
  - `https://securetoken.googleapis.com/<path>` becomes `<url>/securetoken.googleapis.com/<path>`.
- The SDK sends every Auth request with `fetch`, and looks `fetch` up on each request.
- So the plugin injects a shim before your app scripts. The shim wraps `fetch` once and changes only these two hosts. Every other request passes through untouched.

**Why it is recommended:**
- `connectAuthEmulator` puts the SDK in emulator mode, and the SDK then drops its own checks:
  - App verification: `appVerificationDisabledForTesting`.
  - The page-origin check for Google and Apple sign-in by popup or redirect.
  - Aruma enforces all of this on the server (ADR-014), but production mode keeps the checks in the client too.
- In production mode the `authDomain` pages go to `https://<authDomain>/__/auth/handler` and `/__/auth/iframe`, and the SDK calls `getProjectConfig`. Aruma serves all of them.

**Safety of the package:**
- https only, except for `localhost`, `127.0.0.0/8` and `::1`, or with `allowInsecure` for development.
- **It fails closed.** A refused URL stops the build. At run time Auth requests fail, so they never reach Google carrying the user's password.
- With no URL it does nothing.
- The log (off by default) never contains the query, headers, tokens or bodies.

**Other build tools (webpack and others):** make this the first import in the app entry:

```js
import { install } from '@aruma/web';
install({ url: 'https://aruma.example.com' });
```

**Build mode (`mode: 'build'`, Vite 8 or later):**
- There is no run-time code. The plugin replaces the two Google URL literals inside `@firebase/auth` during the build.
- The build fails if it cannot find them, for example when a new Firebase release changes them.

**Tested:** `ARUMA_E2E_MODE=production test/e2e/web-auth/run.sh`, and `production-build` for build mode. All flows pass on firebase 11, 12 and 13 with zero requests to Google. A further test installs the package after the SDK is imported. For details and risks, see `sdk/web/aruma-web/README.md`.

## Emulator-function mode (development)

### Steps

1. **Create a web app** in the project from the dashboard, and copy the config file (`firebaseConfig`).
2. **Enable the sign-in methods** you want on the "Authentication > Sign-in methods" screen. Email and password is on by default. Anonymous sign-in is off, as in Firebase.
3. **Add your app's domain** to "Authorised domains" if you use email links or Google and Apple sign-in. `localhost` is authorised by default.
4. **Add the line:**

```js
import { initializeApp } from "firebase/app";
import { getAuth, connectAuthEmulator } from "firebase/auth";

const app = initializeApp(firebaseConfig); // from Aruma, unchanged
const auth = getAuth(app);
connectAuthEmulator(auth, "https://aruma.example.com", { disableWarnings: true }); // the only line
```

Replace `https://aruma.example.com` with your Aruma server's public address (`public_url`). After that the SDK functions work as they are: `createUserWithEmailAndPassword`, `signInWithEmailAndPassword`, `signInAnonymously`, `linkWithCredential`, `signInWithCustomToken`, `updateProfile`, `updatePassword`, `deleteUser`, `onAuthStateChanged`, and automatic token refresh.

### Why `connectAuthEmulator`?

The web Auth SDK hard-codes two Google hosts in its code, and no field in the config file changes them. The only documented way to point it at another server without a build tool is this function. Aruma serves the Auth paths in the shape the SDK requests in this mode (ADR-014) and verifies everything on the server, so security is not weakened:

- **Tokens:** Aruma signs them with per-project RS256 keys. It never accepts `alg:none` or `Bearer owner`.
- **reCAPTCHA:** it is reported as off, and server-side rate limits protect against flooding.
- **CORS:** open for client routes only, with no cookies. Identity is the API key and the token, not the page origin.

## Restricting the API key

The key is not a secret, but you can limit where it is accepted, on the "API keys" screen in the dashboard. Aruma applies the restrictions the way Google does:

- **Websites:** one line per site, such as `example.com/*` and `*.example.com/*`. A `*` stands in only for a subdomain at the start of the host, or at the end of the path. Browsers often send only the page origin (`https://example.com/`) on cross-site requests, so a restriction to one page path may not match.
- **Also add Aruma's host** (or the `authDomain`), such as `aruma.example.com/*`. The email-link pages and the Google and Apple sign-in window check the key against their own address, just as Google requires you to add `firebaseapp.com`.
- **IP addresses:** for servers only. If a reverse proxy sits in front of Aruma, list it in `trusted_proxies`. Otherwise Aruma sees the proxy's address.
- **API restrictions:** if you restrict the key to APIs (through the dashboard's programmatic API), list both `identitytoolkit.googleapis.com` and `securetoken.googleapis.com`. Otherwise token refresh fails.

A refused request returns 403 `PERMISSION_DENIED`. The web SDK reports a message such as `auth/requests-from-referer-https://other.example/-are-blocked.`.

## Not supported yet

| Feature | Status |
|---|---|
| Phone sign-in, MFA, and email-link sign-in | Returns `auth/operation-not-allowed`, like a project that disabled the provider. Planned for v1.x |
| Google and Apple sign-in by popup or redirect | The SDK loads one script from `apis.google.com`. This is a declared exception (ADR-013) |
| Tenants | Not available. A non-empty `tenantId` is refused with `auth/invalid-tenant-id` |
| `signInWithRedirect` when the app is on a different site than Aruma | `getRedirectResult` returns `null` in browsers that partition third-party storage, such as Chrome. Firebase documents the same limit. Serve Aruma or the `authDomain` on the app's own site, or use `signInWithPopup` |

Playwright tests that call `signInWithPopup`: run them with `channel: 'chromium'` or with Chrome, not the default headless shell. The headless shell sometimes freezes the hidden cross-site iframe when the popup opens, so the first popup never completes.

## Verifying tokens on your server

Your server verifies user tokens with Aruma's verification library. You swap one import line. See [auth-admin.md](auth-admin.md).
