import { expect, open, test } from './fixtures';

// Journeys through the Cloud Storage pages, in both languages, against the mocked console API
// and /v0 session URLs. The fixtures fail every test on a Content-Security-Policy violation,
// under the policy the Go server sends (scripts and styles from /_next/static/ alone).

const FILES = '/project/storage/?id=chat-app';
const TITLE = ['التخزين السحابي', 'Cloud Storage'] as const;

test.describe('files', () => {
  test('browses folders by prefix', async ({ page, l }) => {
    await open(page, FILES, l(...TITLE));
    await expect(page.getByText('gs://chat-app.console.example.com')).toBeVisible();
    await expect(page.getByRole('link', { name: 'readme.txt' })).toBeVisible();
    await page.getByRole('link', { name: l('فتح المجلد photos', 'Open folder photos') }).click();
    await expect(page).toHaveURL(/prefix=photos%2F/);
    await expect(page.getByRole('link', { name: 'cat.png' })).toBeVisible();
    await expect(page.getByRole('link', { name: 'readme.txt' })).toHaveCount(0);
    await page.getByRole('link', { name: l('فتح المجلد 2026', 'Open folder 2026') }).click();
    await expect(page.getByRole('link', { name: 'trip.jpg' })).toBeVisible();
    const crumbs = page.getByRole('navigation', { name: l('المجلد', 'Folder') });
    await expect(crumbs.locator('[aria-current="page"]')).toHaveText('2026');
    await crumbs.getByRole('link', { name: 'photos' }).click();
    await expect(page.getByRole('link', { name: 'cat.png' })).toBeVisible();
    await page.goBack();
    await expect(page.getByRole('link', { name: 'trip.jpg' })).toBeVisible();
  });

  test('uploads a large file in resumable chunks to /v0, without the cookie', async ({ page, api, l, baseURL }) => {
    const store = api.storage.store('chat-app');
    await page.context().addCookies([{ name: 'aruma_session', value: 'session-cookie', url: baseURL! }]);
    const cookies: { url: string; cookie?: string }[] = [];
    page.on('request', (req) => {
      void req.allHeaders().then((h) => cookies.push({ url: req.url(), cookie: h.cookie }));
    });
    // The third chunk meets an unavailable server once: the upload asks the session where it
    // stands, and goes on from there.
    store.failChunks = [3];
    await open(page, `${FILES}&prefix=photos%2F`, l(...TITLE));
    const size = 3 * 1024 * 1024 + 4321;
    const data = Buffer.alloc(size);
    for (let i = 0; i < size; i++) data[i] = (i * 7) % 256;
    await page.locator('input[type=file]').setInputFiles({ name: 'big.bin', mimeType: 'application/octet-stream', buffer: data });

    const uploads = page.getByRole('region', { name: l('الرفع', 'Uploads') });
    await expect(uploads.getByText(l('رُفع', 'Uploaded'), { exact: true })).toBeVisible({ timeout: 20_000 });
    await expect(page.getByRole('status').getByText(l('رُفع photos/big.bin.', 'Uploaded photos/big.bin.'))).toBeVisible();
    await expect(page.getByRole('link', { name: 'big.bin' })).toBeVisible();

    // The session was issued by the console, with the CSRF token and the declared size.
    expect(store.issued).toEqual([{ name: 'photos/big.bin', size, contentType: 'application/octet-stream' }]);
    expect(api.calls.find((c) => c.method === 'POST' && c.path.endsWith('/storage/uploads'))?.csrf).toBe('csrf-token');
    // The bytes went to /v0 in growing chunks, the last one finalizing, and none of them carried
    // the session cookie or the CSRF token.
    const chunks = store.v0.filter((r) => r.command !== 'query');
    expect(chunks.length).toBeGreaterThanOrEqual(5);
    expect(chunks.at(-1)?.command).toBe('upload, finalize');
    expect(chunks.slice(0, 2).map((r) => r.size)).toEqual([256 * 1024, 512 * 1024]);
    expect(store.v0.some((r) => r.command === 'query')).toBe(true);
    expect(store.v0.every((r) => r.cookie === undefined && r.csrf === undefined)).toBe(true);
    expect(store.objects.get('photos/big.bin')?.bytes.equals(data)).toBe(true);
    // The console's own requests carried it: the check above would have seen one.
    expect(cookies.some((c) => c.url.includes('/console/v1/') && c.cookie?.includes('session-cookie'))).toBe(true);
    expect(cookies.filter((c) => c.url.includes('/v0/')).every((c) => !c.cookie)).toBe(true);
  });

  test('asks before replacing a file, and refuses what the server would', async ({ page, api, l }) => {
    const store = api.storage.store('chat-app');
    await open(page, FILES, l(...TITLE));
    await page.locator('input[type=file]').setInputFiles({ name: 'readme.txt', mimeType: 'text/plain', buffer: Buffer.from('new') });
    const dialog = page.getByRole('alertdialog');
    await expect(dialog.getByText('readme.txt')).toBeVisible();
    await dialog.getByRole('button', { name: l('استبدال', 'Replace') }).click();
    await expect(page.getByRole('status').getByText(l('رُفع readme.txt.', 'Uploaded readme.txt.'))).toBeVisible();
    expect(store.objects.get('readme.txt')?.bytes.toString()).toBe('new');

    await page.getByRole('button', { name: l('مجلد جديد', 'New folder') }).click();
    const folder = page.getByRole('dialog');
    await folder.getByLabel(l('اسم المجلد', 'Folder name')).fill('a/b');
    await folder.getByRole('button', { name: l('فتح المجلد', 'Open folder') }).click();
    await expect(folder.getByText(l('لا يحتوي اسم المجلد على /.', "A folder name can't contain /."))).toBeVisible();
    await folder.getByLabel(l('اسم المجلد', 'Folder name')).fill('drafts');
    await folder.getByRole('button', { name: l('فتح المجلد', 'Open folder') }).click();
    await expect(page.getByText(l('هذا المجلد فارغ', 'This folder is empty'))).toBeVisible();
    await expect(page).toHaveURL(/prefix=drafts%2F/);
  });

  test('deletes a file after a confirmation', async ({ page, api, l }) => {
    const store = api.storage.store('chat-app');
    await open(page, FILES, l(...TITLE));
    await page.getByRole('button', { name: l('حذف readme.txt', 'Delete readme.txt') }).click();
    const dialog = page.getByRole('alertdialog');
    await expect(dialog.getByText('readme.txt')).toBeVisible();
    await dialog.getByRole('button', { name: l('حذف', 'Delete') }).click();
    await expect(dialog).toBeHidden();
    await expect(page.getByRole('link', { name: 'readme.txt' })).toHaveCount(0);
    expect(store.objects.has('readme.txt')).toBe(false);
  });
});

test.describe('a file', () => {
  const CAT = '/project/storage/object/?id=chat-app&name=photos%2Fcat.png';

  test('previews an image, edits its metadata and manages its download links', async ({ page, api, l }) => {
    const store = api.storage.store('chat-app');
    await open(page, CAT, l(...TITLE));
    await expect(page.getByRole('heading', { name: 'cat.png' })).toBeVisible();
    const preview = page.getByRole('img', { name: l('معاينة photos/cat.png', 'Preview of photos/cat.png') });
    await expect(preview).toBeVisible();
    await expect.poll(() => preview.evaluate((img: HTMLImageElement) => img.naturalWidth)).toBe(8);

    // Metadata: a content field set, a key changed, one added.
    await page.getByLabel('Cache-Control').fill('public, max-age=300');
    await page.getByRole('textbox', { name: l('القيمة 1', 'Value 1') }).fill('omar');
    await page.getByRole('button', { name: l('إضافة مفتاح', 'Add key') }).click();
    await page.getByRole('textbox', { name: l('المفتاح 2', 'Key 2') }).fill('owner');
    await page.getByRole('button', { name: l('حفظ', 'Save') }).click();
    await expect(page.getByText(l('هذا المفتاح مستعمل.', 'This key is already used.'))).toBeVisible();
    await page.getByRole('textbox', { name: l('المفتاح 2', 'Key 2') }).fill('album');
    await page.getByRole('textbox', { name: l('القيمة 2', 'Value 2') }).fill('pets');
    await page.getByRole('button', { name: l('حفظ', 'Save') }).click();
    await expect(page.getByRole('status').getByText(l('حُفظت البيانات الوصفية.', 'Metadata saved.'))).toBeVisible();
    const patch = api.calls.find((c) => c.method === 'PATCH' && c.path.endsWith('/storage/object'));
    expect(patch?.body).toEqual({ cacheControl: 'public, max-age=300', metadata: { owner: 'omar', album: 'pets' } });
    expect(patch?.csrf).toBe('csrf-token');
    expect(store.objects.get('photos/cat.png')?.meta.metadata).toEqual({ owner: 'omar', album: 'pets' });

    // Download links: one more, then revoke the first.
    const links = page.getByRole('list', { name: l('روابط التنزيل', 'Download links') });
    await expect(links.getByRole('listitem')).toHaveCount(1);
    await page.getByRole('button', { name: l('إنشاء رابط', 'Create link') }).click();
    await expect(links.getByRole('listitem')).toHaveCount(2);
    const first = store.objects.get('photos/cat.png')!.meta.downloadTokens[0]!;
    await page.getByRole('button', { name: l('إلغاء رابط التنزيل 1', 'Revoke download link 1') }).click();
    await page.getByRole('alertdialog').getByRole('button', { name: l('إلغاء الرابط', 'Revoke') }).click();
    await expect(links.getByRole('listitem')).toHaveCount(1);
    expect(store.objects.get('photos/cat.png')?.meta.downloadTokens).not.toContain(first);
    // The token went in the body, never in a URL.
    const revoke = api.calls.find((c) => c.path.endsWith('/object:deleteToken'));
    expect(revoke?.body).toEqual({ token: first });

    // The edit survived the token changes.
    await expect(page.getByLabel('Cache-Control')).toHaveValue('public, max-age=300');
  });

  test('downloads as an attachment and deletes', async ({ page, api, l }) => {
    const store = api.storage.store('chat-app');
    await open(page, '/project/storage/object/?id=chat-app&name=readme.txt', l(...TITLE));
    const download = page.waitForEvent('download');
    await page.getByRole('button', { name: l('تنزيل', 'Download'), exact: true }).click();
    expect((await download).suggestedFilename()).toBe('readme.txt');
    // A text file gets no preview: only raster images do.
    await expect(page.getByRole('img', { name: /readme/ })).toHaveCount(0);
    await page.getByRole('button', { name: l('حذف', 'Delete'), exact: true }).click();
    await page.getByRole('alertdialog').getByRole('button', { name: l('حذف', 'Delete') }).click();
    await expect(page).toHaveURL('/project/storage/?id=chat-app');
    expect(store.objects.has('readme.txt')).toBe(false);
  });
});

test.describe('rules', () => {
  test('shows a refused publish’s error on its line, then publishes the fix', async ({ page, api, l }) => {
    const store = api.storage.store('chat-app');
    await open(page, '/project/storage/rules/?id=chat-app', l(...TITLE));
    await expect(page.getByText(l('لم تُنشر قواعد بعد', 'No rules published yet'))).toBeVisible();
    const editor = page.getByRole('textbox', { name: l('القواعد', 'Rules'), exact: true });
    await expect(editor).toHaveValue(/allow read, write: if false;/);

    const bad = "rules_version = '2';\nservice firebase.storage {\n  match /b/{bucket}/o {\n    match /{allPaths=**} {\n      allow read: if request.auth.uid ==;\n    }\n  }\n}\n";
    await editor.fill(bad);
    await page.getByRole('button', { name: l('نشر', 'Publish') }).click();
    await expect(page.getByText(l('في القواعد خطأ', 'The rules have an error'))).toBeVisible();
    await expect(page.locator('[data-line="5"]')).toHaveAttribute('data-issue', 'error');
    await expect(page.locator('[data-issue]')).toHaveCount(1);
    const at = page.getByRole('button', { name: l('السطر 5، العمود 41:', 'Line 5, column 41:') });
    await at.click();
    await expect(editor).toBeFocused();
    expect(await editor.evaluate((el: HTMLTextAreaElement) => el.value.slice(el.selectionStart, el.selectionEnd))).toBe(';');
    expect(store.rules.published).toBe(false);

    // Check reports a warning without publishing; Publish then saves.
    await editor.fill(bad.replace('request.auth.uid ==;', 'true;'));
    await expect(page.locator('[data-issue]')).toHaveCount(0);
    await expect(page.getByText(l('ملفاتك متاحة للجميع', 'Anyone can reach your files'))).toBeVisible();
    await page.getByRole('button', { name: l('فحص', 'Check') }).click();
    await expect(page.locator('[data-line="5"]')).toHaveAttribute('data-issue', 'warning');
    expect(store.rules.published).toBe(false);
    await page.getByRole('button', { name: l('مقفلة', 'Locked') }).click();
    await page.getByRole('button', { name: l('نشر', 'Publish') }).click();
    await expect(page.getByRole('status').getByText(l('نُشرت القواعد.', 'Rules published.'))).toBeVisible();
    expect(store.rules.source).toContain('allow read, write: if false;');
    await expect(page.getByText(l('لم تُنشر قواعد بعد', 'No rules published yet'))).toHaveCount(0);
  });
});

test.describe('settings', () => {
  test('edits the CORS rules', async ({ page, api, l }) => {
    const store = api.storage.store('chat-app');
    await open(page, '/project/storage/cors/?id=chat-app', l(...TITLE));
    const editor = page.getByLabel(l('القواعد (JSON)', 'Rules (JSON)'));
    await expect(editor).toHaveValue('[]\n');
    await page.getByRole('button', { name: l('ابدأ من مثال', 'Start from an example') }).click();
    await editor.fill('[{"origin": ["https://app.example.com"], "method": ["GET"]}, {"method": ["GET"]}]');
    await expect(page.getByText(l('القاعدة 2 تحتاج قائمة origin، مثل ["https://example.com"].', 'Rule 2 needs an origin list, such as ["https://example.com"].'))).toBeVisible();
    await editor.fill('[{"origin": ["*"], "method": ["GET"], "maxAgeSeconds": 600}]');
    await expect(page.getByText(l('كل المواقع تقرأ ملفاتك', 'Every website can read your files'))).toBeVisible();
    await editor.fill('[{"origin": ["https://app.example.com"], "method": ["GET", "HEAD"], "responseHeader": ["Content-Type"], "maxAgeSeconds": 600}]');
    await page.getByRole('button', { name: l('حفظ', 'Save') }).click();
    await expect(page.getByRole('status').getByText(l('حُفظت قاعدة واحدة.', 'Saved 1 rule.'))).toBeVisible();
    expect(store.cors).toEqual([{ origin: ['https://app.example.com'], method: ['GET', 'HEAD'], responseHeader: ['Content-Type'], maxAgeSeconds: 600 }]);
  });

  test('shows the usage against the quota', async ({ page, l }) => {
    await open(page, '/project/storage/usage/?id=chat-app', l(...TITLE));
    await expect(page.getByRole('meter', { name: l('المستعمل من الحصة', 'Quota used') })).toBeVisible();
  });
});
