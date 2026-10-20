# Phase 2: email actions, decisions

Status: decisions, 2026-10-18. Scope: verification, password reset, email change and recovery
(plan tasks T11–T13; AC-A6, AC-A7, AC-A19, part of AC-A4). The wire contract is in
[phase2-identity-toolkit.md](phase2-identity-toolkit.md) §3.5, §3.7, §3.8, §6 and §7; this note
records what Aruma chose where the sources are silent or disagree, and why. Citation keys
([D-enum], [E-ops], [J-…]) are defined in that document's References.

Additional sources:
- [D-limits] Firebase Auth limits, "Email sending limits": https://firebase.google.com/docs/auth/limits
- [D-enum2] Identity Platform, email enumeration protection (current URL): https://docs.cloud.google.com/identity-platform/docs/admin/email-enumeration-protection
- [J-domain] firebase-js-sdk `core/util/validate_origin.ts` (`matchDomain`): a listed domain also matches its subdomains; an IP address matches only itself.

## Codes

- **Form.** 256 random bits, base64url (43 characters). Only `HMAC("auth-oob:" + code)` is
  stored in `auth.oob_codes`; the code exists only in the link (tamper scenario "dump of DB").
- **Single use.** Using a code marks it used in the same transaction as its effect
  (`UPDATE … WHERE used_at IS NULL`), so a concurrent second use finds nothing. Using a code
  also retires the user's other open codes of the same type. Any change of address (a link, a
  recovery, `accounts:update`, the Admin API) retires every open code of the user, so none of
  them wakes up if the old address comes back; a change of password retires the open reset
  links but not recovery links, which are the previous owner's way back when whoever changed
  the address changed the password too (security review v0.1, S-3).
- **Fit.** A code acts on the user it was issued for, and refuses (INVALID_OOB_CODE) if that
  user was deleted or recreated, or if the address changed since. A recovery code fits while the
  account still has the address that replaced the restored one (its `newEmail`): it undoes one
  change, and only while that change is the latest. After A→B→C the link sent to A is dead; the
  one sent to B works. A disabled user answers USER_DISABLED and leaves the code unused.
- **Lifetimes.** No official figure exists ([phase2-identity-toolkit.md] §6, §9.7):
  `PASSWORD_RESET` 1 hour (it grants a credential), `VERIFY_EMAIL` 3 days (the commonly
  reported figure), `VERIFY_AND_CHANGE_EMAIL` and `RECOVER_EMAIL` 3 days as well: recovery must
  outlive the time a victim takes to notice, but not leave the old mailbox's holder a long
  window. Expired codes answer EXPIRED_OOB_CODE; housekeeping deletes codes a day after use or
  expiry.

## sendOobCode

- **Order** (follows the emulator [E-ops sendOob], since no doc states one): MISSING_REQ_TYPE;
  `EMAIL_SIGNIN` → OPERATION_NOT_ALLOWED (email-link sign-in is deferred, AC-A17); continueUrl
  (INVALID_CONTINUE_URI, then UNAUTHORIZED_DOMAIN); then per type.
- **PASSWORD_RESET:** MISSING_EMAIL, INVALID_EMAIL, OPERATION_NOT_ALLOWED when password sign-in
  is off, flood limits, then the lookup. Under enumeration protection an unknown or disabled
  address succeeds with nothing sent ([D-enum2]: "a verification email is sent only if the
  email address exists… there are no specific error messages indicating when emails aren't
  sent"); without it, EMAIL_NOT_FOUND / USER_DISABLED.
- **VERIFY_EMAIL:** the idToken's user and address; the body's `email` is ignored, as in the
  emulator. A missing token is INVALID_ID_TOKEN, a user without an email MISSING_EMAIL.
- **VERIFY_AND_CHANGE_EMAIL** (verifyBeforeUpdateEmail): MISSING_NEW_EMAIL, INVALID_ID_TOKEN,
  INVALID_EMAIL, the token, then a recent sign-in (CREDENTIAL_TOO_OLD_LOGIN_AGAIN, as a direct
  email change needs). A taken new address succeeds silently under protection ([D-enum2]: "sent
  only if the email address doesn't already exist"), EMAIL_EXISTS without. The emulator returns
  EMAIL_EXISTS even under protection; Aruma follows the documentation.
- **returnOobLink** from a client is refused with INSUFFICIENT_PERMISSION (403), as the emulator
  does; only the Admin API receives links ([phase2-admin-auth.md] generate*Link).
- **Language.** `X-Firebase-Locale` picks `en` for any English tag; everything else gets the
  project default, Arabic. The link carries `lang=` so the action page matches the email.

## Using codes

- **resetPassword** without `newPassword` inspects (verifyPasswordResetCode / checkActionCode);
  with it, it consumes a PASSWORD_RESET code only. The password policy runs before the code is
  used, so a weak password leaves it usable. Effects: new password, `emailVerified = true`
  (opening the link proves the mailbox, as the emulator does; production unverified, §9.9),
  every session revoked ([D-sessions]). The emulator also unlinks every other provider; Aruma
  does not, since nothing documents it and it would destroy Google or Apple links.
  OPERATION_NOT_ALLOWED when password sign-in is off (REST doc; the emulator says
  PASSWORD_LOGIN_DISABLED), checked on consumption only so checkActionCode keeps working.
- **accounts:update {oobCode}:** VERIFY_EMAIL sets `emailVerified`; VERIFY_AND_CHANGE_EMAIL
  sets the new address, verified, revokes sessions (an email change is a "major account change"
  [D-sessions]) and queues a RECOVER_EMAIL link to the previous address; RECOVER_EMAIL restores
  the previous address, verified, and revokes sessions. A taken address answers EMAIL_EXISTS.
  The response is the user, as for a profile update, without new tokens: the emulator issues
  none for these either.
- The response's `email` / `newEmail` follow the JS SDK's `checkActionCode` [J-pwstrat]: for a
  change, `email` is the old address and `newEmail` the new; for recovery, `email` is the
  address being restored and `newEmail` the one it replaces.

## continueUrl

Absolute http(s), no user info, ≤ 2048 bytes, else INVALID_CONTINUE_URI. Its host must be on
the project's authorised domains, matched as the web SDK matches origins [J-domain]: a listed
domain covers its subdomains; an IP address only itself. The emulator checks only that the URL
is absolute; Aruma enforces the domain on the server because the SDK skips its own check in
emulator mode (ADR-014). The URL is stored with the code; the action page offers "Continue"
only to that stored URL, re-checked against the domains at that moment, never to the link's
query parameter.

## Links

`<base>/__/auth/action?apiKey=…&lang=…&mode=…&oobCode=…[&continueUrl=…]`. The base is the
public URL (scheme and port kept) while the project's authDomain is the default (the host of
`public_url`), and `https://<authDomain>` once the owner sets a custom one. `apiKey` is the
caller's key, or for admin links the project's web key ([D-handler]: the link carries the web
API key).

## The action page

- Server-rendered, no script; Arabic and RTL unless `lang=en`; Aruma's name and the line "not
  affiliated with or endorsed by Google or Firebase".
- **No change on GET.** Mail scanners and link previewers fetch links; Firebase's page applies
  verification on load, which would let a prefetch verify an address an attacker registered.
  Aruma shows a confirm button (or the new-password form) and acts on POST.
- **POST protection:** `http.CrossOriginProtection` (Sec-Fetch-Site / Origin) and a hidden
  token `HMAC("auth-action-form:" + code)`, so a form from elsewhere needs the code itself.
- **Project binding:** the page resolves the project from `apiKey` and looks the code up in
  that project only; a key of another project makes the code unknown. The `mode` must match
  the code's type.
- **Headers:** `Content-Security-Policy: default-src 'none'; style-src 'sha256-…';
  form-action 'self'; base-uri 'none'; frame-ancestors 'none'`, `Referrer-Policy: no-referrer`,
  `X-Frame-Options: DENY`, `Cache-Control: no-store`, `X-Robots-Tag: noindex`. The access log
  never records query strings, so codes in the URL stay out of it.

## Templates

Built-in texts for the four types in Arabic and English are Aruma's own wording following the
structure in [phase2-identity-toolkit.md] §7. Bodies are HTML; `%LINK%`, `%APP_NAME%` (the Auth
app name, else the project's display name), `%EMAIL%`, `%NEW_EMAIL%`, `%DISPLAY_NAME%` are
HTML-escaped into the body and inserted plain into the subject with line breaks removed. The
plain-text part is derived from the template before substitution, so values cannot inject
markup into either part. A saved template must contain `%LINK%`.

`%DISPLAY_NAME%` is empty in mail to an address the account has not verified: every
`VERIFY_EMAIL` and `VERIFY_AND_CHANGE_EMAIL` message, and a reset or recovery message to an
unverified address. The name is whatever the account holder typed, so otherwise anyone could
send their own text from the project's sender to any inbox, by signing up with the address or
asking to change to it (the display-name spam abuse known on Firebase projects). The built-in
texts greet without a name at all ("Hello," / "مرحبًا،"), which also avoids "Hello ," for
users without one. Firebase's own defaults greet with `%DISPLAY_NAME%`; a custom template may
still do so and gets the name for verified addresses (security review v0.1, S-4).

## Outbox, delivery and the dev mailbox

- The message is rendered and written to `auth.email_outbox` in the transaction that creates
  its code. Its content is sealed (AES-GCM envelope, AAD `auth.email_outbox|message|<id>`),
  because it holds a live link.
- **Dev mailbox.** Instance setting `mail.dev_mailbox` (`ARUMA_MAIL_DEV_MAILBOX`), default on
  only when `public_url` is loopback. When allowed, a project uses it unless it has an SMTP host
  or pinned another choice (`auth.project_config.mail_dev_mailbox`). Messages for the dev
  mailbox are stored as delivered and never sent; the console reads them. Production
  instances therefore never swallow mail silently.
- **Worker.** Every 5 s, claims up to 20 due rows with `FOR UPDATE SKIP LOCKED` and a 5-minute
  lease (a crashed worker's rows are retried, not lost), sends, and records success or the
  error. Backoff 30 s doubling to 1 h; abandoned after 8 attempts (`failed_at`). Delivered and
  abandoned rows are purged after 30 days.
- **SMTP.** `net/smtp`. `starttls` refuses a server that does not offer STARTTLS (no
  downgrade); `tls` is implicit TLS; certificates are verified; TLS ≥ 1.2. Credentials go only
  over TLS or to a loopback server (net/smtp's PLAIN rule). The password is sealed with AAD
  `auth.project_config|smtp_password_enc|<project number>`. Headers are encoded (RFC 2047) and
  come from validated values, so none can add a header. Messages are multipart/alternative,
  UTF-8, quoted-printable, `Auto-Submitted: auto-generated`.
- **Where SMTP may go.** The host is the console user's choice, so the server would otherwise
  open TCP connections anywhere it can reach, and `smtp:test` would read back whatever a host
  and port answer (security review v0.1, S-5). Unless the instance setting
  `mail.smtp_allow_private` (`ARUMA_MAIL_SMTP_ALLOW_PRIVATE`) is on, a host on a loopback,
  private (RFC 1918, `fc00::/7`), link-local (`169.254.0.0/16`, `fe80::/10`: the metadata
  address of most clouds), unspecified, shared (`100.64.0.0/10`, Alibaba's metadata), reserved
  or multicast address is refused (`SMTP_HOST_PRIVATE`): as an address or a `localhost` name
  when saved, and for any name on the address actually dialled, after resolution,
  so DNS rebinding gains nothing. IPv4-mapped and NAT64 addresses are judged by the IPv4
  address they carry. The default is on only when `public_url` is loopback (a developer's own
  machine, like the dev mailbox). A self-hosted single-owner install with a local relay
  (Postfix on 127.0.0.1 or the LAN) turns it on with one line; it is off elsewhere because a
  console account, even on an instance with closed sign-up, is not meant to reach the
  operator's network. `smtp:test` answers a generic `SMTP_ERROR` and the server log keeps the
  detail; only the private-address refusal is named, so the operator knows which setting to
  change. Connecting and the server's greeting (with the TLS handshake for `tls`) each have
  10 s, the whole conversation 30 s, so a silent port fails fast and the console's request
  ends within the HTTP write timeout.

## Flood limits

Firebase publishes daily per-project quotas only (Spark: 1000 verification, 1000 change, 150
reset emails a day [D-limits]). Aruma adds finer limits:

| Bucket | Limit | Answer |
|---|---|---|
| per client address, all sendOobCode calls | 20 at once, 60 an hour | TOO_MANY_ATTEMPTS_TRY_LATER |
| per project and recipient address | 5 at once, 5 an hour | RESET_PASSWORD_EXCEED_LIMIT for resets, else TOO_MANY_ATTEMPTS_TRY_LATER |
| per project | 200 at once, 1000 an hour | TOO_MANY_ATTEMPTS_TRY_LATER |

The recipient bucket is taken before the account is looked up, so an unknown address is limited
exactly like a known one and the answer reveals nothing under enumeration protection. Limits
are in memory, per server process, like the other Auth rate limits.

## For the Admin API (agent B)

`core.Service` offers, for `accounts:sendOobCode` with `returnOobLink` on the admin path:
`GeneratePasswordResetLink(ctx, p, email, settings)` (EMAIL_NOT_FOUND for an unknown address),
`GenerateEmailVerificationLink(ctx, p, email, settings)` (USER_NOT_FOUND) and
`GenerateVerifyAndChangeEmailLink(ctx, p, email, newEmail, settings)` (USER_NOT_FOUND,
EMAIL_EXISTS). They return the link, send nothing, and skip enumeration protection and flood
limits. `settings` is `ActionCodeSettings{ContinueURL, Language}`. The emulator answers
`{kind, email, oobCode, oobLink}`; the code is the link's `oobCode` parameter.

## Open, needs production comparison

1. Code lifetimes per type.
2. Whether production sets `emailVerified` on reset, and whether it unlinks other providers.
3. Whether production sends RECOVER_EMAIL after verifyBeforeUpdateEmail completes (it does
   after a direct `updateEmail`), and the exact recovery link lifetime.
4. The production status code of `returnOobLink` from an unprivileged caller.
