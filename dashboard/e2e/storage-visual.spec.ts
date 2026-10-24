import { expect, open, test } from './fixtures';

// Full-page screenshots of the Cloud Storage pages in both languages (see visual.spec.ts).

const TITLE = ['التخزين السحابي', 'Cloud Storage'] as const;

test('files', async ({ page, snapshot, l }) => {
  await open(page, '/project/storage/?id=chat-app&prefix=photos%2F', l(...TITLE));
  await expect(page.getByRole('link', { name: 'cat.png' })).toBeVisible();
  await snapshot('storage-files');
});

test('a file', async ({ page, snapshot, l }) => {
  await open(page, '/project/storage/object/?id=chat-app&name=photos%2Fcat.png', l(...TITLE));
  const preview = page.getByRole('img', { name: l('معاينة photos/cat.png', 'Preview of photos/cat.png') });
  await expect.poll(() => preview.evaluate((img: HTMLImageElement) => img.naturalWidth)).toBe(8);
  await snapshot('storage-object');
});

test('rules with an error', async ({ page, snapshot, l }) => {
  await open(page, '/project/storage/rules/?id=chat-app', l(...TITLE));
  const editor = page.getByRole('textbox', { name: l('القواعد', 'Rules'), exact: true });
  await editor.fill("rules_version = '2';\nservice firebase.storage {\n  match /b/{bucket}/o {\n    match /{allPaths=**} {\n      allow read: if request.auth.uid ==;\n    }\n  }\n}\n");
  await page.getByRole('button', { name: l('نشر', 'Publish') }).click();
  await expect(page.getByText(l('في القواعد خطأ', 'The rules have an error'))).toBeVisible();
  await page.evaluate(() => window.scrollTo(0, 0));
  await snapshot('storage-rules');
});

test('usage', async ({ page, snapshot, l }) => {
  await open(page, '/project/storage/usage/?id=chat-app', l(...TITLE));
  await expect(page.getByRole('meter')).toBeVisible();
  await snapshot('storage-usage');
});
