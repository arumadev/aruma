# @aruma/admin-auth

Verify Aruma ID tokens and session cookies, and manage Aruma Auth users, from a Node.js server
that already uses `firebase-admin`. Results are the official `firebase-admin` types
(`DecodedIdToken`, `UserRecord`, …) and errors are the official `FirebaseAuthError` codes
(`auth/id-token-expired`, `auth/user-not-found`, …).

Aruma is not affiliated with or endorsed by Google. Firebase is a trademark of Google LLC.

## The one line

```diff
  import { initializeApp, cert } from 'firebase-admin/app';
- import { getAuth } from 'firebase-admin/auth';
+ import { getAuth } from '@aruma/admin-auth';

  initializeApp({ credential: cert('service-account.json') }); // the key file Aruma issued
  const decoded = await getAuth().verifyIdToken(idToken, true);
```

`createCustomToken` is the official one, unchanged: `getAuth().createCustomToken()` calls
`firebase-admin` itself, which signs locally with the key file.

## Install

```sh
npm install @aruma/admin-auth firebase-admin@^14
```

`firebase-admin` 14 is a peer dependency. Node.js 20 or later.

## Configuration

| What | Where it comes from, first match wins |
|---|---|
| Aruma server | `getAuth(app, { url })`, then `ARUMA_URL`, then the key file's `token_uri` (Aruma writes `<server>/oauth2/v1/token` there) |
| Service account key | `getAuth(app, { credential })` (path or object), then the app's `cert()` credential, then `GOOGLE_APPLICATION_CREDENTIALS` |
| Project | the app's `projectId`, then the key file's `project_id` |

The server URL must be `https` (plain `http` only on loopback), and never a Google address.

**Emulator guard.** `getAuth()` throws if `FIREBASE_AUTH_EMULATOR_HOST` is set: with it,
`firebase-admin` in the same process accepts unsigned tokens and sends the literal admin token
`owner`. Pass `{ allowEmulatorHost: true }` to start anyway; this is ignored when
`NODE_ENV=production`.

## API

The `BaseAuth` methods, with the same signatures:

- `verifyIdToken(idToken, checkRevoked?)`, `verifySessionCookie(cookie, checkRevoked?)`
- `createSessionCookie(idToken, { expiresIn })` (5 minutes to 14 days, in milliseconds)
- `createUser`, `getUser`, `getUserByEmail`, `getUsers`, `updateUser`, `deleteUser`, `deleteUsers`, `listUsers`
- `setCustomUserClaims`, `revokeRefreshTokens`
- `createCustomToken` (the official implementation)

Verification fetches the keys through the issuer's discovery document
(`<server>/issuers/<project>/.well-known/openid-configuration`; session cookies use
`/session-issuers/`), accepts RS256 with a `kid` only, and checks `aud`, `iss`, `sub`, `exp` (no
tolerance), `iat` and `auth_time` (at most 5 minutes in the future). `checkRevoked` reads the user
and compares `auth_time` with `tokensValidAfterTime`, as `firebase-admin` does. `validSince` has
one-second resolution, so a token issued in the same second as a revocation still passes.

## Tests

`npm test` type-checks the declarations and runs the unit tests and the live tests. The live
tests start a real Aruma server (`test/smoke/genconfig -serve`), so they need Go and the local
Postgres (`ARUMA_TEST_DATABASE_URL`). Set `ARUMA_URL` and `ARUMA_TEST_DIR` to use a running one.

## License

Apache-2.0.
