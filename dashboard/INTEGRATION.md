# Dashboard integration notes

What the Go side needs to know to build, embed, serve and test the dashboard. Everything here
lives in `dashboard/`; nothing outside it was changed.

## Build

- Node 24 and npm. `npm ci && npm run build` writes the static export to `dashboard/out/`
  (about 2.3 MB, of which 470 KB are the self-hosted IBM Plex fonts). No environment variables
  are read at build time; the export is identical for every deployment.
- The existing `make dashboard` target already does the right thing (`npm ci`, `npm run build`,
  copy `out/.` into `internal/admin/web/dist/`).
- The export calls the API on the same origin only (`/console/v1/…`), with
  `credentials: 'same-origin'`. No external requests, analytics or CDNs.

## Serving (internal/admin/web)

The handler in `phase1` serves the export correctly. Verified end to end against
`aruma serve` with this export embedded: setup, sign-in, projects, apps, config and key
downloads, keys, service accounts, tokens, settings, the 404 page, and client-side navigation.
Points worth keeping in mind:

- **Required CSP change: allow two `<style>` elements by hash.** React Aria injects two fixed
  stylesheets at runtime: `touch-action` for pressable elements on first render, and an
  `overscroll-behavior` rule while a modal is open on iOS. `style-src 'self'` blocks both; the
  browser logs a violation on every page, and touch press handling and iOS scroll locking lose
  those rules. The hashes ship in the export as **`csp.json`**:

  ```json
  { "styleSrcElem": ["'sha256-38RhXrc7EdReTKsOm23ZPOCUgniTUUcjky8QOOrQx6o='", "'sha256-gYiS/BvZvRcK27JIXTuwhZ3hs2+VJ1X+2gUlE+farlg='"] }
  ```

  Please read it in `web.New` and append its entries to `style-src` (`style-src 'self' <hashes>`).
  `src/lib/csp.test.ts` recomputes the hashes from the installed `react-aria`, so a dependency
  upgrade that changes the injected text fails `npm test` until `csp.json` is updated.
  The Playwright test server (`e2e/serve.mjs`) sends this exact policy (inline script hashes
  plus `csp.json`) and every e2e test fails on any CSP violation, so a regression shows up there.
  The exported HTML has no inline `style` attributes or `<style>` elements; inline scripts are
  only Next's own, as before.
- Next 16 writes per-segment prefetch files with `!` in their names, such as
  `login/__next.!KGF1dGgp.login.__PAGE__.txt`. They must be served as-is (the current handler
  does). `csp.json` is also served publicly, which is harmless.
- Pages are prerendered in Arabic (`<html lang="ar" dir="rtl">`); the client switches language
  without a reload. All console pages render on the client after `GET /console/v1/me`.

## Contract assumptions

The client follows `docs/api/console.md` as of `phase1`, including the CLI token change:
`POST /me/tokens` sends `currentPassword` and shows a wrong password on the field (400 with a
`currentPassword` field violation) or the retry time (429 `Retry-After`). Notes:

- A 401 from any route other than `/status`, `/setup`, `/auth:login` and `/auth:signup`
  sends the user to `/login/?next=<current page>`. Sign-in shows one generic message for every
  401/400/403, and the `Retry-After` countdown for 429.
- Server error messages are English, so the UI never shows them. It maps `status` (and
  `fieldViolations[].field`) to localized text (`src/lib/errors.ts`). A new validation field
  falls back to a generic "check the highlighted fields" message until it gets a translation.
- The audit log labels the actions listed in `AUDIT_ACTIONS` (`src/features/audit/AuditScreen.tsx`);
  an unknown action is shown as its raw code. Please add new actions there and to both
  catalogs (a unit test checks the catalogs cover the list).
- Downloads read the file name from `Content-Disposition` (`filename=`, quoted, or `filename*=`).

## CI

Suggested job (ubuntu-latest), with `working-directory: dashboard`:

```yaml
- uses: actions/setup-node@v7
  with: { node-version: 24, cache: npm, cache-dependency-path: dashboard/package-lock.json }
- run: npm ci
- run: npm run lint          # eslint + stylelint + tsc --noEmit
- run: npm test              # vitest
- run: npm run build         # static export to out/
- run: npx playwright install --with-deps chromium
- run: npm run test:e2e      # playwright: ar-rtl and en-ltr projects
- uses: actions/upload-artifact@v7
  if: failure()
  with: { name: playwright-report, path: dashboard/playwright-report }
```

- **Linux screenshot baselines are not committed yet.** Visual baselines are named by platform
  (`e2e/__screenshots__/<project>/<spec>/<name>-<platform>.png`); only `-darwin` ones exist.
  Generate the Linux set once on the CI image and commit it:
  `npx playwright test e2e/visual.spec.ts --update-snapshots`. Until then the visual tests
  fail on Linux with "snapshot doesn't exist".
- `npm audit` reports a high-severity advisory in `braces` reached only through dev tooling
  (stylelint and eslint-config-next). `npm audit --omit=dev` is clean; nothing from it ships.

## Live smoke test (optional)

`e2e/live.spec.ts` runs the main journey against a real server through the same static server:

```sh
ARUMA_API_URL=http://127.0.0.1:8080 ARUMA_SETUP_CODE=<code from the log> npm run test:e2e -- --project=live
```

On a server that is already set up it signs in with `ARUMA_E2E_EMAIL` / `ARUMA_E2E_PASSWORD`
(default `owner@example.com` / `correct horse battery`). It creates and then deletes a project.

## Local development

```sh
npm run dev          # http://localhost:3000, /console/v1 proxied to ARUMA_DEV_API
```

`ARUMA_DEV_API` defaults to `http://127.0.0.1:18081`. The proxy and the dev-only
`skipTrailingSlashRedirect` exist only under `next dev`; the export has neither.
`npm run tokens` regenerates `src/styles/colors.css` after a change to the brand seed in
`scripts/generate-tokens.ts`.

## Authentication screens (Phase 2)

The Auth section (`/project/auth/…`: Users, Sign-in methods, Templates, Mailbox, Settings) uses
the routes in `docs/api/console.md` and, from the email and provider work, the routes below.
Their shapes were not fixed when the screens were built, so this is what the dashboard expects
(`src/lib/api/types.ts`). Each of them may answer 404 until it exists: the screens then show a
"not available on this server yet" note instead of an error, and hide the Mailbox tab.

Every path starts with `/console/v1/projects/{project}/auth`.

| Method | Path | Body → Response |
|---|---|---|
| POST | `/users/{uid}:sendPasswordReset` | → 2xx (body ignored) |
| GET | `/providers` | → `{providers: [Provider]}` |
| PATCH | `/providers/{providerId}` | Google `{enabled?, clientId?, clientSecret?}`, Apple `{enabled?, servicesId?, teamId?, keyId?, privateKey?}` → `Provider`. A secret is sent only when the user typed a new one |
| GET | `/templates` | → `{templates: [{type, language: "ar"\|"en", subject, body, customized}]}`, every type in both languages |
| PUT | `/templates/{type}/{language}` | `{subject, body}` → the template |
| DELETE | `/templates/{type}/{language}` | → 2xx; the list is fetched again |
| GET | `/smtp` | → `{host, port, username, passwordSet, security: "starttls"\|"tls"\|"none", senderName, senderAddress, replyTo}` |
| PATCH | `/smtp` | the fields above (not `passwordSet`), plus `password` only when replaced → the settings |
| POST | `/smtp:test` | `{to}` → 2xx |
| GET | `/mailbox` | → `{enabled, messages: [{id, to, type?, subject, link?, createTime}]}`; `enabled: false` or 404 hides the tab |
| DELETE | `/mailbox` | → 2xx |

`Provider` = `{providerId: "google.com"|"apple.com", enabled, redirectUri, clientId?, clientSecretSet?, servicesId?, teamId?, keyId?, privateKeySet?}`.
Secrets are write-only: the dashboard shows "saved" when `…Set` is true and never displays a value.

`GET /config` may carry `limits.emailCodeLifetimeSeconds: {VERIFY_EMAIL: …, PASSWORD_RESET: …, …}`;
the Settings page lists those lifetimes when present.

The template preview never inserts template markup: an HTML body is reduced to text in an inert
`DOMParser` document, so the strict CSP needs no change. Mailbox links open only when they are
`http(s)` URLs.

Adding the Authentication destination changed the navigation rail and the overview, so the
`-linux` baselines of those screens were removed; CI regenerates them (`--update-snapshots=missing`)
and uploads them as the `playwright-snapshots-linux` artifact, to be committed from there.
