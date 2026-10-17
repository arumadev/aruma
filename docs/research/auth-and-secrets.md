# Authentication and secret handling

Research notes behind ADR-010 and the authn/crypto packages. Gathered 2026-10-17.

## Service account token exchange

- **Go Admin SDK** (`firebase.google.com/go/v4`): credentials load through `google.CredentialsFromJSONWithParams`, which **honours `token_uri`**. It falls back to `https://oauth2.googleapis.com/token` only when the field is empty. [google.go](https://github.com/golang/oauth2/blob/master/google/google.go)
  - The new `cloud.google.com/go/auth` path honours it too.
  - The assertion header is `{alg: RS256, typ: JWT, kid: <private_key_id>}`.
  - Claims: `iss`, `scope`, `aud` = token URL, `iat` = now−10s, `exp` = iat+1h.
  - Source: [jwt.go](https://github.com/golang/oauth2/blob/master/jwt/jwt.go)
- **Node Admin SDK** v13+: hands token fetching to google-auth-library and gtoken. gtoken **hardcodes** `https://oauth2.googleapis.com/token` and ignores `token_uri`. The header is `{alg: RS256}` with no `kid`. [credential-internal.ts](https://github.com/firebase/firebase-admin-node/blob/main/src/app/credential-internal.ts)
  - Initialising with `cert(file)` makes no network call. Token fetches need a custom `credential` (`getAccessToken()`) to reach Aruma.
- **Request**: form `grant_type=urn:ietf:params:oauth:grant-type:jwt-bearer&assertion=<JWT>`.
- **Response**: `{"access_token", "expires_in": 3600, "token_type": "Bearer"}`.
- **Errors**: HTTP 400 `{"error":"invalid_grant","error_description":...}`.
- **Validation** ([RFC 7523](https://www.rfc-editor.org/rfc/rfc7523.html)):
  - `alg` must be exactly RS256.
  - Look up the service account by `iss`, and verify with the `kid` key when present. Without `kid`, try the account's active keys.
  - `aud` must be our token URL.
  - Allow ±60 s skew on `iat` and `exp`, and require `exp − iat` ≤ 3600 s.
  - Do not require `sub`, because Google clients omit it.

## Passwords

- argon2id at the OWASP point `m=19456 KiB, t=2, p=1`, with a 16-byte salt and a 32-byte key. [OWASP](https://cheatsheetseries.owasp.org/cheatsheets/Password_Storage_Cheat_Sheet.html)
- Store it as a PHC string, `$argon2id$v=19$m=19456,t=2,p=1$<salt>$<hash>`, using raw standard base64.
- `argon2.IDKey` allocates `m` KiB per call, so concurrent hashing is capped by a semaphore.
- Hash a dummy password for unknown accounts so timing does not reveal them. Rehash on login when the parameters change.
- Accept passwords of 8 to 1024 bytes and never truncate them.

## Sessions and CSRF

- Tokens are 32 random bytes from `crypto/rand`, encoded as base64url. Only `HMAC-SHA256(key, token)` is stored.
- The cookie is `__Host-` prefixed, with `Secure; HttpOnly; Path=/; SameSite=Lax`. [OWASP sessions](https://cheatsheetseries.owasp.org/cheatsheets/Session_Management_Cheat_Sheet.html)
- Timeouts are 30 min idle and 8 h absolute. Sessions are rotated on login and revoked when the password changes.
- CSRF uses two layers:
  - `http.CrossOriginProtection` (Go 1.25+), which checks Fetch Metadata and falls back to Origin. [csrf.go](https://github.com/golang/go/blob/master/src/net/http/csrf.go)
  - A session-bound synchronizer token in the `X-Aruma-CSRF` header. [OWASP CSRF](https://cheatsheetseries.owasp.org/cheatsheets/Cross-Site_Request_Forgery_Prevention_Cheat_Sheet.html)
- Bearer-token requests need no CSRF token.

## Envelope encryption

- AES-256-GCM with random 96-bit nonces (`cipher.NewGCMWithRandomNonce`, Go 1.24+). [crypto/cipher](https://pkg.go.dev/crypto/cipher)
- Each record gets a fresh 32-byte DEK. The DEK is wrapped by a KEK derived from the master key, and both seals bind the same associated data (`aruma:v1|table|column|row`). [Google KMS](https://docs.cloud.google.com/kms/docs/envelope-encryption)
- Rotation means re-wrapping the DEKs under a new KEK version.

## Login throttling and enumeration

- **Per account** is the primary control: increasing delay after repeated failures. **Per IP** is secondary: a token bucket on the auth endpoints. [OWASP auth](https://cheatsheetseries.owasp.org/cheatsheets/Authentication_Cheat_Sheet.html)
- Unknown account, wrong password and disabled account return the same message, status and timing.

## Logs

- `slog` `ReplaceAttr` redacts by key name, and `LogValuer` types (`log.Secret`) redact by type. [log/slog](https://pkg.go.dev/log/slog), [OWASP logging](https://cheatsheetseries.owasp.org/cheatsheets/Logging_Cheat_Sheet.html)
