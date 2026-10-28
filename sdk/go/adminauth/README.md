# adminauth (Go)

Verify Aruma ID tokens and session cookies, and manage Aruma Auth users, from a Go server that
already uses `firebase.google.com/go/v4`. Results are the official `auth` types (`*auth.Token`,
`*auth.UserRecord`, …) and errors satisfy the official helpers (`auth.IsIDTokenExpired`,
`auth.IsIDTokenRevoked`, `auth.IsUserNotFound`, `errorutils.IsNotFound`, …).

Aruma is not affiliated with or endorsed by Google. Firebase is a trademark of Google LLC.

## The one line

```go
app, _ := firebase.NewApp(ctx, nil, option.WithCredentialsFile("service-account.json"))

// client, err := app.Auth(ctx)
client, err := adminauth.NewClient(ctx, app, adminauth.WithCredentialsFile("service-account.json"))

token, err := client.VerifyIDTokenAndCheckRevoked(ctx, idToken)
```

`firebase.App` does not expose its credentials, so the key file is named again (or comes from
`GOOGLE_APPLICATION_CREDENTIALS`, which `firebase.NewApp` reads by default too). The app is used
for custom tokens: `client.CustomToken` calls the official implementation unchanged.

```sh
go get github.com/arumadev/aruma/sdk/go/adminauth
```

## Configuration

| What | Where it comes from, first match wins |
|---|---|
| Aruma server | `WithURL`, then `ARUMA_URL`, then the key file's `token_uri` |
| Service account key | `WithCredentialsFile` / `WithCredentialsJSON`, then `GOOGLE_APPLICATION_CREDENTIALS` |
| Project | `WithProjectID`, then the key file's `project_id` |

The server URL must be `https` (plain `http` only on loopback), and never a Google address.

**Emulator guard.** `NewClient` fails if `FIREBASE_AUTH_EMULATOR_HOST` is set: with it, the
official client in the same process skips signature checks. `AllowEmulatorHost()` lets it start,
except when `GO_ENV` or `NODE_ENV` is `production`.

## API

The `auth.Client` methods, with the same signatures: `VerifyIDToken`,
`VerifyIDTokenAndCheckRevoked`, `VerifySessionCookie`, `VerifySessionCookieAndCheckRevoked`,
`SessionCookie`, `CreateUser`, `GetUser`, `GetUserByEmail`, `GetUsers`, `UpdateUser`,
`DeleteUser`, `DeleteUsers`, `Users` (an iterator of `*auth.ExportedUserRecord`),
`SetCustomUserClaims`, `RevokeRefreshTokens`, `CustomToken`, `CustomTokenWithClaims`.

Verification follows firebase-admin-go: RS256 with a `kid` only, exact `aud` and `iss`, `sub` of
1 to 128 characters, 300 seconds of clock skew on `exp` and `iat`, and `auth_time` required. The
CheckRevoked methods compare `iat` with `TokensValidAfterMillis`. Keys come from the issuer's
discovery document (`<server>/issuers/<project>/.well-known/openid-configuration`, and
`/session-issuers/` for cookies).

**How the errors work.** The official helpers type-assert an error type of firebase-admin-go's
`internal` package. The library captures that type from an error the official client produces
offline and builds its errors as values of it; `errors_test.go` proves every helper recognises
them. Errors are returned unwrapped, because the helpers do not unwrap. `CreateUser` and
`UpdateUser` take the official `*auth.UserToCreate` and `*auth.UserToUpdate` and read their
setters' values by reflection, read only. `NewClient` self-tests both and fails if a
firebase-admin-go release changes them.

## Tests

`go test ./...` runs the unit tests and the live tests. The live tests start a real Aruma server
(`test/smoke/genconfig -serve` in this repository), so they need the local Postgres
(`ARUMA_TEST_DATABASE_URL`). Set `ARUMA_URL` and `ARUMA_TEST_DIR` to use a running one.

## License

Apache-2.0.
