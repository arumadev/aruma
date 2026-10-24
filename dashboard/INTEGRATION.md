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

  `web.New` reads it and appends its entries to `style-src`.
  `src/lib/csp.test.ts` recomputes the hashes from the installed `react-aria`, so a dependency
  upgrade that changes the injected text fails `npm test` until `csp.json` is updated.
  The Playwright test server (`e2e/serve.mjs`) sends this exact policy (inline script hashes
  plus `csp.json`) and every e2e test fails on any CSP violation, so a regression shows up there.
  The exported HTML has no inline `style` attributes or `<style>` elements; inline scripts are
  only Next's own, as before.
- **No `'self'` for code (ADR-022 §10.1).** Storage serves user files on the dashboard's
  origin, so `script-src`, `style-src` and `font-src` name `<host>/_next/static/` (the request's
  Host, without a scheme) and the hashes, never `'self'`, and `default-src` is `'none'`
  (`internal/admin/web`, pinned by `TestCSPCodeOnlyFromStaticDir`). Everything the export loads
  is under `/_next/static/`; keep it so. `e2e/serve.mjs` sends the same policy, and
  `e2e/live-storage.spec.ts` (with `ARUMA_DASHBOARD_URL`) runs on the Go server itself and
  checks that an uploaded script or stylesheet is blocked.
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
the routes in `docs/api/console.md`: users, settings and signing keys; "Auth email" (templates,
SMTP, the dev mailbox, password reset); and "Auth: Google and Apple sign-in". The shapes are in
`src/lib/api/types.ts`. Notes on how the screens use them:

- Secrets are write-only. The Google client secret, the Apple private key and the SMTP password
  are sent only when typed; the screens show "saved" from `…Set` and never a value. Removing the
  SMTP password sends `password: ""` after a confirmation.
- `PATCH /smtp` sends `devMailbox` only when the switch changed, since sending it pins the
  project's choice. The switch appears only where the mailbox route answers.
- The mailbox answers 404 where the instance never keeps email: the Mailbox tab is then hidden.
  With `enabled: false` the tab stays while earlier messages remain, under a "now over SMTP" note.
- The Google and Apple forms check the server's rules first (credentials required to enable,
  10-character Team and Key IDs, a PEM private key); anything else comes back as
  `INVALID_CONFIG` and is shown as a general message.
- The reset button asks for the email language and shows the returned link with a copy button.
- The template preview never inserts template markup: the HTML body is reduced to text in an
  inert `DOMParser` document, so the strict CSP needs no change. Mailbox links open only when
  they are `http(s)` URLs.

When a change alters screenshots, remove the affected `-linux` baselines with the new `-darwin`
ones: CI regenerates them (`--update-snapshots=missing`) and uploads them as the
`playwright-snapshots-linux` artifact, to be committed from there.
