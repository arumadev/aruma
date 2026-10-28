# Security

Aruma holds other people's data and credentials, so this page says what it protects, what it assumes about you, and what independent reviews have found so far.

To report a vulnerability, see [SECURITY.md](../SECURITY.md). Do not open a public issue.

## What Aruma protects

| Asset | Why it matters |
|---|---|
| Project owner accounts and their sessions | Whoever holds one controls all of that owner's projects |
| CLI tokens and service-account tokens | Administrative access without a browser |
| Service-account private keys | They let you mint access tokens. Aruma never stores them |
| Isolation between projects | Project A must not read or change project B |
| The master key | Every HMAC and encryption key is derived from it |
| App users' accounts and sessions | Whoever holds a user account holds that user's data in your app |
| Per-project signing keys for sign-in tokens | They can mint a sign-in token for any user. Stored encrypted |
| Refresh tokens and email-action tokens | They grant a session or a password change. Stored only as HMACs |
| SMTP, Google and Apple secrets | They act in your name at an outside provider. Stored encrypted and never shown again after saving |
| Download links and upload session IDs (Storage) | Each is a bearer credential for an object or an upload |

## Trust boundaries

1. **Browser to console.** Session cookie, CSRF protection and a Content Security Policy.
2. **CLI or your server to the Management API.** A bearer token: a personal CLI token or a service-account token.
3. **Your app to the Firebase-compatible APIs.** An API key only identifies the project. It grants no permission. The caller's identity is the sign-in token or refresh token in the request. CORS is open on these routes only, and cookies are not used.
4. **Your server to the Admin APIs.** A service-account access token from the same project, or the owner's CLI token. API keys and app-user tokens are refused.
5. **Browser to the `authDomain` pages.** The email-action page and the Google and Apple sign-in handler. They use a strict CSP and may be framed only by authorized domains.
6. **Aruma to Google, Apple and your SMTP server.** Outside providers. Their tokens are checked against their public keys, `aud` and `nonce`. SMTP requires TLS.
7. **Aruma to PostgreSQL.** A trusted or local network. Secrets in the database are hashed or encrypted.
8. **Reverse proxy to Aruma.** Aruma believes `X-Forwarded-For` only from `trusted_proxies`.

## Threats and how Aruma handles them

**Console and owner accounts**

- Session theft: protected cookie, idle and absolute timeouts, revocation on password change, CSRF protection.
- Password guessing and credential stuffing: rate limits per address and per email, growing delays, argon2id.
- Account enumeration: one login error message, and a dummy password check so timing does not differ.
- Someone claiming a new, exposed server first: registration is closed, and setup needs a one-time token printed in the log.
- Browser attacks on the console (XSS, clickjacking): no inline scripts in the CSP, `frame-ancestors 'none'`, escaped output.
- Memory exhaustion by many password hashes: a limit on concurrent argon2id work, rate limits and body size limits.

**Project isolation and API keys**

- Using an API key as an admin identity: keys never reach an admin authenticator.
- Using a restricted key from another site, address, app or API: key restrictions are enforced on every request. Website, Android and iOS restrictions rely on headers that a non-browser client can forge, so they limit accidental use, not a determined attacker. This is how Google's keys work too. An IP restriction reads `X-Forwarded-For` only from a trusted proxy.
- Reaching another project by changing a project number or ID: the project is resolved and its ownership checked at the entrance, and every query carries the project number.
- Listing projects: anything the caller does not own returns the same `NOT_FOUND`.
- Moving an app or key between projects by editing a request: identifiers are immutable, and the project comes from the resolved path, not the body.
- Mixing up credential types: each credential type has its own verifier, and each API has an allowlist of the types it accepts.

**Tokens and sessions for app users**

- Forged sign-in tokens (`alg:none`, algorithm confusion, edited payload, another project's `kid`): RS256 only, `kid` required and taken from the project's own keys, `iss`, `aud` and `exp` checked on the server and in both Admin libraries.
- A token from one project used on another, or with a Google issuer: each project has its own issuer, and refresh tokens are bound to the project.
- A stolen session surviving a password change: sessions are revoked up to the next second, so none survives the revocation second.
- Forged service-account tokens and custom tokens: RS256 only, verified with the registered public key, `aud` and `exp` required, one-hour maximum. A custom token must be signed by an active key of a service account in the same project.
- Emulator environment variables switching verification off in your server: both Admin libraries refuse to start with `FIREBASE_AUTH_EMULATOR_HOST`, and refuse plain http except to loopback, Google domains and redirects.

**Account takeover and email**

- Enumerating user emails: enumeration protection is on by default. Sign-in returns one error, a password reset reports success silently, and the sign-in method list is empty.
- Guessing user passwords: limits per address, growing delays per project and email, argon2id with bounded concurrency. When a limit table fills, the oldest entry is evicted, so a flood cannot lock everyone out.
- Mail flooding: limits per recipient, per address and per project. Built-in email templates carry no user-chosen display name when the address is unverified.
- Reusing an email token or redirecting to a foreign domain: 256-bit one-time tokens that expire and are bound to the project. `continueUrl` must be an authorized domain. Opening a link (GET) performs no action.
- Taking over an account through an unverified email, then losing it when the real owner signs in with Google: when a provider confirms the email, the password and other providers are removed and sessions are revoked.
- Google email trust: `email_verified` from Google is trusted only for `gmail.com` and `googlemail.com`, or for a Workspace account whose `hd` equals the email domain. Otherwise the email counts as unverified and an existing account gets `needConfirmation`.
- Replayed or swapped `state`, and wrong `nonce`, in Google and Apple sign-in: one-time sessions bound to a `sessionId`, PKCE for Google, `nonce` required.
- Admin fields sent by a client (`customAttributes`, `disabled`, `localId`): ignored on client endpoints. `returnOobLink` is refused.

**Secrets**

- Raw secrets in the database or log: tokens are stored as HMACs, keys, secrets and the outbound mail queue use envelope encryption, and logs redact by type and name. Tests dump the database and log after a full run and look for raw secrets.

**Storage, rules and resource limits**

- A hostile project owner on a shared instance: rules are compiled and evaluated under memory and work budgets, and compilation shares a limited number of slots.
- Upload and download links: upload IDs have 128 bits and are stored only as an HMAC. Download tokens are generated by the server. Comparison is constant time, and a missing object returns the same 403 as a wrong token.
- Served objects carry a stored content type, `nosniff` and a `sandbox` policy, and workers and service workers are refused from every origin.
- Console SMTP settings as a port scanner: connections to loopback, private, link-local and similar addresses are refused, checked again at connect time so DNS rebinding does not help.

## Secure defaults

- Console sign-up is closed (`ARUMA_SIGNUP_OPEN=false`). The first owner is created with a one-time setup token from the log.
- Console sessions expire after 30 minutes idle and 8 hours in total.
- `/metrics` listens on `127.0.0.1:9464`, not on the public port.
- The dev mailbox, SMTP to private addresses, and the Document Database dev switches are off unless `public_url` is on loopback.
- Realtime Database, Storage and the Document Database are off until you turn them on.
- A new project's Storage rules deny all access until you deploy your own.
- Admin credentials on the Document Database are accepted only over TLS, or through a trusted proxy that sets `X-Forwarded-Proto: https`.

## What you must do

- **Serve over TLS.** Put Aruma behind a TLS proxy, and set `ARUMA_PUBLIC_URL` to the https address. Storage logs a warning on plain http. Firebase Admin credentials are refused without TLS. See [Configuration](reference/configuration.md).
- **Protect the master key.** Generate it with `openssl rand -base64 32`. Keep it out of the repository, and store it apart from your database backups. Anyone with the database and the master key can read everything.
- **Back up both halves.** `aruma backup` dumps the database only. If you use Storage, back up its data directory with it and restore both together. Test a restore.
- **Mount the Storage disk before the service starts.** Aruma refuses to boot on an empty data directory when the database expects files. Make the service wait for the mount.
- **Set `trusted_proxies` correctly.** List only your own proxies. Make the proxy set `X-Forwarded-For` and `X-Forwarded-Proto` itself instead of passing the client's.
- **Protect the database.** Use TLS to PostgreSQL if it is not on the same host. Where you can, run migrations with a separate role and give the server DML rights only, because the audit log is protected from deletion by a trigger that the table owner could disable.
- **Remove `localhost` from authorized domains in production.** It is allowed by default, as in Firebase.
- **Keep API keys restricted.** Restrictions reduce misuse. They are not secrets.
- **Run one server for now.** Rate limits and signer caches live in memory. They restart empty and are not shared between servers.
- **Keep the host trusted.** Aruma does not defend against the person who runs the server. Whoever controls the database and master key controls everything.

Also out of scope today: phone sign-in, multi-factor authentication, email-link sign-in and tenants are not implemented. A password-sign-in lockout of 15 minutes after repeated wrong guesses for one email is accepted, as in Firebase.

## Reviews

Every release has had a security review by a reviewer who did not write the code. The reviewer reproduced each finding against a real server, and every fix came with a test that failed before the fix and runs in CI. Severity labels are the reviewer's own.

### v0.1: accounts, console and Auth

v0.1 was reviewed in three passes: the platform and console, the Auth service as it was built, and a final review of Auth and its SDK packages before release.

**Platform and console (2 high, 3 medium, 7 low, 7 informational).** Everything rated low or above was fixed.

- High: memory growth from an unbounded metrics label chosen by the client, and unbounded memory for failed-login emails of any length.
- Medium: CLI tokens survived a password change; the per-email login delay could be bypassed with concurrent attempts; backups dropped the `sslmode` setting and weakened TLS.
- Low: service-account token endpoint revealed account state before checking the signature; service tokens outlived their deleted key; password change had no attempt limit; backup files were created world-readable; CLI config file permissions; IPv6 rate limits were per address instead of per /64; one deletion edge case.
- Informational, accepted: the audit log does not cover sign-in or denials; no cap on resources an owner can create (a self-hosted instance with known owners); `/status` shows the version.

**Auth service (3 high, 3 medium, 9 low, 9 informational).**

- High, all fixed: `signUp` with an `idToken` could replace an account's password or email using a stolen or old token; an email-change token stayed valid after takeover protection and after revocation; a session started in the revocation second survived it.
- Medium, all fixed: account-creation limit bypass through `signUp`; full rate-limit tables disabled password sign-in for everyone (now the oldest entry is evicted); emergency key rotation took up to five minutes to reach the Admin libraries (now 60 seconds).
- Low: the Go library followed redirects (fixed). A 15-minute lockout after repeated wrong passwords is accepted. Seven low findings were still open at the v0.1 review: a timing difference in password-reset mail between registered and unknown emails; email matching can replace another linked Google or Apple identity without a notice; Apple refresh tokens are not revoked at Apple except on account deletion; non-strict base64 for token signatures; a trailing dot in a hostname bypassing the Google-domain check in the Admin libraries; a rotation race in the signer cache; no alert to the old address on a direct email change with protection off.
- Informational, all accepted: `localhost` is authorized by default; `needConfirmation` reveals that an email has an account when a provider token is held; and similar low-impact items that match Firebase.
- A set of 22 tamper scenarios (forged tokens, replayed and swapped OAuth state, cross-project tokens, admin-field injection, secret dumps, rate limits) all pass against the server and both Admin libraries.
- Dependencies: no reachable vulnerabilities in Go or npm dependencies. One advisory in an unused package (`x/crypto/openpgp`) has no published fix and is not called.

**Final Auth review before release (2 medium, 3 low, 1 low/informational, 1 informational).** All fixed.

- Medium: API key restrictions were saved but not enforced, so a restricted key worked from anywhere. Google's `email_verified` was trusted for any domain, which let a stranger sign in to an account by email (fixed as described above).
- Low: an old email-recovery link stayed valid after later changes; the display name, which is user text, reached mail sent to unproven addresses; SMTP settings could probe the internal network.
- Low/informational: the Android redirect library accepted plain http to non-loopback hosts.
- Informational: removing a user's email left a password provider on the old address, blocking re-registration.
- Checked and sound: token signing and verification, refresh tokens, custom tokens, console CSRF and cookies, email tokens and `continueUrl` checks, Google sign-in, SQL parameterization, and malformed-input fuzzing (no crash).

**Accepted at v0.1:** limits are in memory and per server; the seven open low findings above.

### v0.2: Realtime Database

Scope: the Realtime Database wire protocol, core, API, rules and console, plus the limits in configuration. Behavior was compared with Firebase, and a browser test covered the long-polling page.

**Ten findings, all fixed.** The reviewer did not assign severities; the findings concern robustness and correctness.

- A client without read access could learn whether a guess of the data hash was right (conditional writes compared the hash before rules). It now checks read access first, then the hash, then write rules.
- Long-poll requests found a session by ID alone, from any database. A session now stays on its own database.
- An Admin session using an auth override was never revalidated after its credential expired or was revoked.
- REST push IDs advanced by one, so one client's ID predicted the next client's. The step is now random.
- `matches()` in rules had no cost limit and could hold the writer for a minute. It is now bounded.
- A single read had no size limit. A new `rtdb.limits.read_rows` limit (default one million rows) returns `too_big`.
- Long-poll message fragments were outside the in-flight byte budget.
- Path parsing split an entire path before checking limits.
- `/.settings/rules.json` ignored `auth_variable_override`. It now answers 401.
- Long-poll scripts carried stored text without escaping, and the `dframe` page allowed any inline script. Output is now escaped and the page's CSP allows one script by hash.
- Also fixed: long-poll connection IDs were a counter and revealed the connection count. They are now random.

**Checked and sound:** long-poll IDs and passwords from a cryptographic source and compared in constant time; escaped REST errors with `nosniff`; exact ETag comparison; forwarded host trusted only from trusted proxies; project ownership in the console; connection, outbox and export limits.

**Known at v0.2:**

- A listener on a query re-reads its node on every write below it. `read_rows` checks size only when the listener starts.
- Modern Chromium does not send the `disconn` request when a page closes, so a long-poll session ends by the 90-second abandon timeout instead of at once.
- Limits are in memory and per server.

### v0.3: Security rules and Cloud Storage

Scope: the Storage byte-serving and upload paths, the rules engine, the new management surfaces, the console CSP, and the SDK packages and build-time switches (web, Android, iOS, Node, Go). Dependencies were audited with `npm audit` and `govulncheck`.

**Ten findings: 2 blocker, 4 major, 4 minor.** All were fixed except the callable-functions part of SR3-10.

- Blocker: the total cost of literal regex patterns in one ruleset had no limit. Any project owner on the instance could exhaust server memory with a ruleset within the source size limit. Fixed with a per-ruleset budget.
- Blocker: rule evaluation error messages copied attacker-sized text without counting it against the memory meter. Fixed by bounding messages.
- Major: an empty data directory was accepted even when the database held objects. After an unmounted disk, reads failed and new uploads were written to the system disk, then lost when the disk came back. Aruma now refuses to start in that case.
- Major: errors from function call cycles grew quadratically. Fixed.
- Major: a web app using Aruma for Auth only sent its Aruma ID token to Google's Storage host. Fixed with a runtime guard and a build failure.
- Major: the same app also sent the token to Firestore and callable functions at Google. Firestore is fixed in v0.4 (Aruma serves it and the web package routes to it, with runtime and build-time guards, including Node's gRPC transport). **Callable functions are still open:** Aruma does not serve them, so do not use them in an app that signs in with Aruma Auth. The README says so.
- Minor: console rule compilation bypassed the compile limit (fixed; a publish-pile-up case is deferred and documented); Android strict mode did not refuse Storage hosts (fixed); the Node redirect scope was wider than documented (documentation fixed); the iOS mirror's contents were not pinned (now pinned by commit and tree and checked in the build).

**Checked and sound:** upload and download token handling, constant-time comparison, no tokens in logs, rules failing closed, `media` response headers, Range handling, gzip streaming with fixed memory, multipart limits, object names, slow-client timeouts, disk-full and `fsync` failure handling, reservation recovery, console CSRF, and management routes. `npm audit` found no vulnerabilities in the three npm packages. `govulncheck` found one reachable advisory in `crypto/x509`, fixed by pinning Go 1.27.3.

**Accepted at v0.3:**

- Node's HTTP client follows redirects to the same host or a subdomain, on any port and the same protocol, up to 20 hops, with its bearer token. Resumable uploads do not follow redirects.
- Bundlers without an end-of-build hook (Turbopack, Metro) cannot always fail the build. The runtime guard still covers requests, but not `<img>` links.
- With no Aruma URL set, the web, iOS and Android adapters do nothing and traffic goes to Google. This is your decision as operator.
- Fallback modes leave download links on Google's host.
- On plain http there is no `Sec-Fetch-*` protection, so the console CSP alone applies. Storage warns at startup. Use https.
- A download link is a bearer credential in the query string, like Firebase. It is never logged, `Referrer-Policy: no-referrer` is set, and replacing the token revokes it.
- Gzip decompression up to the object size limit per download, and no per-IP download limit. Put limits at your proxy.
- An upload session created by a service account stays valid for up to 7 days after that account or key is deleted. A deleted project stops it.
- A reservation left ambiguous by a database outage at commit stays reserved until the next start.
- Per-IP limits are in memory, for one server.

**Not verified in this review:** Android and iOS builds, and ext4 and XFS behavior when a disk is removed (measured on APFS only).

### v0.4

v0.4.0-rc.1 has no release-wide review yet. Its parts were reviewed as they were built, and a release-wide review is planned before v0.4.0. Findings will be fixed in `v0.4.0-rc.2`.
