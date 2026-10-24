import { expect, test, type Page } from '@playwright/test';

// The Storage screens against a real `aruma serve` with this dashboard embedded and Storage
// enabled, opened on the server itself (ARUMA_DASHBOARD_URL): the pages carry the Go server's
// own Content-Security-Policy, and the uploads go to its /v0. See playwright.config.ts.
//
// It fails on any CSP violation but the ones it provokes: an uploaded file loaded as a script
// or a stylesheet from the dashboard's origin, which the policy must block.

const email = process.env.ARUMA_E2E_EMAIL ?? 'owner@example.com';
const password = process.env.ARUMA_E2E_PASSWORD ?? 'correct horse battery';

async function signIn(page: Page) {
  await page.goto('/');
  await page.waitForURL(/\/(setup|login|projects)\/$/);
  if (page.url().endsWith('/setup/')) {
    const code = process.env.ARUMA_SETUP_CODE;
    test.skip(!code, 'The server needs first-boot setup: set ARUMA_SETUP_CODE.');
    await page.getByLabel('Setup code').fill(code!);
    await page.getByLabel('Email').fill(email);
    await page.getByLabel('Password').fill(password);
    await page.getByRole('button', { name: 'Create account' }).click();
  } else if (page.url().endsWith('/login/')) {
    await page.getByLabel('Email').fill(email);
    await page.getByLabel('Password').fill(password);
    await page.getByRole('button', { name: 'Sign in' }).click();
  }
  await expect(page).toHaveURL('/projects/');
}

test('Storage from upload to rules, under the server’s policy', async ({ page }) => {
  test.setTimeout(120_000);
  const violations: string[] = [];
  await page.exposeFunction('__reportCspViolation', (v: string) => violations.push(v));
  await page.addInitScript(() => {
    localStorage.setItem('aruma.locale', 'en');
    document.addEventListener('securitypolicyviolation', (e) => {
      void (window as unknown as { __reportCspViolation: (v: string) => void }).__reportCspViolation(`${e.violatedDirective} ${e.blockedURI}`);
    });
  });
  const v0: { method: string; cookie?: string; csrf?: string }[] = [];
  page.on('request', (req) => {
    if (new URL(req.url()).pathname.startsWith('/v0/')) void req.allHeaders().then((h) => v0.push({ method: req.method(), cookie: h.cookie, csrf: h['x-aruma-csrf'] }));
  });

  await signIn(page);
  const projectId = `store-${Date.now().toString(36)}`;
  await page.getByRole('button', { name: 'New project' }).first().click();
  const dialog = page.getByRole('dialog');
  await dialog.getByLabel('Project name').fill('Storage Live');
  await dialog.getByLabel('Project ID').fill(projectId);
  await expect(dialog.getByText('Available')).toBeVisible();
  await dialog.getByRole('button', { name: 'Create project' }).click();
  await expect(page).toHaveURL(`/project/?id=${projectId}`);

  // A large file, uploaded in resumable chunks to /v0 through a session the console issued.
  await page.getByRole('link', { name: 'Cloud Storage' }).click();
  await expect(page.getByText(`gs://${projectId}.`)).toBeVisible();
  const size = 3 * 1024 * 1024 + 777;
  const big = Buffer.alloc(size);
  for (let i = 0; i < size; i++) big[i] = (i * 13) % 256;
  await page.locator('input[type=file]').first().setInputFiles({ name: 'big.bin', mimeType: 'application/octet-stream', buffer: big });
  await expect(page.getByRole('status').getByText('Uploaded big.bin.')).toBeVisible({ timeout: 60_000 });
  await expect(page.getByRole('link', { name: 'big.bin' })).toBeVisible();
  // Chunks of 256 KiB, 512 KiB and 1 MiB, then the rest, which finalizes.
  await expect.poll(() => v0.length).toBe(4);
  expect(v0.every((r) => r.method === 'POST' && !r.cookie && !r.csrf)).toBe(true);

  // An image, a script and a stylesheet, into a folder.
  const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAgAAAAICAIAAABLbSncAAAAGUlEQVR4nGN4kKVln3UCk2TAKgokGQalDgA3BFyhYLlH1gAAAABJRU5ErkJggg==', 'base64');
  await page.goto(`/project/storage/?id=${projectId}&prefix=media%2F`);
  await page.locator('input[type=file]').first().setInputFiles([
    { name: 'cat.png', mimeType: 'image/png', buffer: png },
    { name: 'evil.js', mimeType: 'text/javascript', buffer: Buffer.from('window.__pwned = true;') },
    { name: 'evil.css', mimeType: 'text/css', buffer: Buffer.from('body { display: none }') },
  ]);
  await expect(page.getByRole('link', { name: 'evil.css' })).toBeVisible({ timeout: 30_000 });
  await expect(page.getByRole('link', { name: 'cat.png' })).toBeVisible();

  // The image previews; its metadata and download links change.
  await page.getByRole('link', { name: 'cat.png' }).click();
  const preview = page.getByRole('img', { name: 'Preview of media/cat.png' });
  await expect.poll(() => preview.evaluate((img: HTMLImageElement) => img.naturalWidth)).toBe(8);
  await page.getByLabel('Cache-Control').fill('public, max-age=300');
  await page.getByRole('button', { name: 'Add key' }).click();
  await page.getByRole('textbox', { name: 'Key 1' }).fill('album');
  await page.getByRole('textbox', { name: 'Value 1' }).fill('pets');
  await page.getByRole('button', { name: 'Save' }).click();
  await expect(page.getByRole('status').getByText('Metadata saved.')).toBeVisible();
  await page.reload();
  await expect(page.getByLabel('Cache-Control')).toHaveValue('public, max-age=300');
  await expect(page.getByRole('textbox', { name: 'Value 1' })).toHaveValue('pets');
  const links = page.getByRole('list', { name: 'Download links' });
  await expect(links.getByRole('listitem')).toHaveCount(1);
  await page.getByRole('button', { name: 'Create link' }).click();
  await expect(links.getByRole('listitem')).toHaveCount(2);
  const url = (await links.getByRole('listitem').first().locator('bdi').textContent())!;
  expect((await page.request.get(url)).status()).toBe(200);
  await page.getByRole('button', { name: 'Revoke download link 1' }).click();
  await page.getByRole('alertdialog').getByRole('button', { name: 'Revoke' }).click();
  await expect(links.getByRole('listitem')).toHaveCount(1);
  expect((await page.request.get(url)).status()).toBe(403);

  // An uploaded file never loads as the dashboard's script or stylesheet: the policy names
  // /_next/static/ alone. The browser reports both as violations, and nothing runs.
  const scriptUrl = (await (await page.request.get(`/console/v1/projects/${projectId}/storage/object?name=media%2Fevil.js`)).json()) as { downloadUrls: string[] };
  const cssUrl = (await (await page.request.get(`/console/v1/projects/${projectId}/storage/object?name=media%2Fevil.css`)).json()) as { downloadUrls: string[] };
  const loaded = await page.evaluate(
    async ([js, css]) => {
      const load = (el: HTMLScriptElement | HTMLLinkElement) =>
        new Promise<string>((resolve) => {
          el.onload = () => resolve('loaded');
          el.onerror = () => resolve('blocked');
          document.head.append(el);
        });
      const script = document.createElement('script');
      script.src = js!;
      const link = document.createElement('link');
      link.rel = 'stylesheet';
      link.href = css!;
      return [await load(script), await load(link), String((window as unknown as { __pwned?: boolean }).__pwned)];
    },
    [scriptUrl.downloadUrls[0], cssUrl.downloadUrls[0]],
  );
  expect(loaded).toEqual(['blocked', 'blocked', 'undefined']);
  await expect.poll(() => violations.length).toBe(2);
  expect(violations.map((v) => v.split(' ')[0]).sort()).toEqual(['script-src-elem', 'style-src-elem']);
  violations.length = 0;

  // The rules editor shows the compiler's error on its line, then publishes the fix.
  await page.goto(`/project/storage/rules/?id=${projectId}`);
  const editor = page.getByRole('textbox', { name: 'Rules', exact: true });
  await expect(editor).toHaveValue(/allow read, write: if false;/);
  await editor.fill("rules_version = '2';\nservice firebase.storage {\n  match /b/{bucket}/o {\n    match /{allPaths=**} {\n      allow read: if request.auth.uid == ;\n    }\n  }\n}\n");
  await page.getByRole('button', { name: 'Publish' }).click();
  await expect(page.getByText(/The rules have (an error|\d+ errors)/)).toBeVisible();
  await expect(page.locator('[data-issue="error"]').first()).toHaveAttribute('data-line', '5');
  await editor.fill("rules_version = '2';\nservice firebase.storage {\n  match /b/{bucket}/o {\n    match /{allPaths=**} {\n      allow read: if request.auth != null;\n    }\n  }\n}\n");
  await page.getByRole('button', { name: 'Check' }).click();
  await expect(page.getByText('No errors or warnings.')).toBeVisible();
  await page.getByRole('button', { name: 'Publish' }).click();
  await expect(page.getByRole('status').getByText('Rules published.')).toBeVisible();
  await page.reload();
  await expect(editor).toHaveValue(/request\.auth != null/);

  // CORS and usage.
  await page.goto(`/project/storage/cors/?id=${projectId}`);
  await page.getByLabel('Rules (JSON)').fill('[{"origin": ["https://app.example.com"], "method": ["GET"], "maxAgeSeconds": 600}]');
  await page.getByRole('button', { name: 'Save' }).click();
  await expect(page.getByRole('status').getByText('Saved 1 rule.')).toBeVisible();
  await page.reload();
  await expect(page.getByLabel('Rules (JSON)')).toHaveValue(/https:\/\/app\.example\.com/);
  await page.goto(`/project/storage/usage/?id=${projectId}`);
  await expect(page.getByText('Stored files')).toBeVisible();
  await expect(page.getByText(/^4$/)).toBeVisible();

  // Delete a file, then the project.
  await page.goto(`/project/storage/?id=${projectId}`);
  await page.getByRole('button', { name: 'Delete big.bin' }).click();
  await page.getByRole('alertdialog').getByRole('button', { name: 'Delete' }).click();
  await expect(page.getByRole('link', { name: 'big.bin' })).toHaveCount(0);
  await page.goto(`/project/settings/?id=${projectId}`);
  await page.getByRole('button', { name: 'Delete project' }).click();
  await page.getByRole('alertdialog').getByLabel('Project ID').fill(projectId);
  await page.getByRole('alertdialog').getByRole('button', { name: 'Delete project' }).click();
  await expect(page.getByText('This project is deleted')).toBeVisible();

  expect(violations, 'Content-Security-Policy violations').toEqual([]);
});
