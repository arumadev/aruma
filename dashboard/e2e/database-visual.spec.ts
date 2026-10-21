import { expect, open, test } from './fixtures';

// Full-page screenshots of the Realtime Database pages in both languages (see visual.spec.ts).

test('data', async ({ page, snapshot, l }) => {
  await open(page, '/project/database/?id=chat-app', l('قاعدة البيانات اللحظية', 'Realtime Database'));
  await page.getByRole('button', { name: l('توسيع rooms', 'Expand rooms') }).click();
  await page.getByRole('button', { name: l('توسيع general', 'Expand general') }).click();
  await expect(page.getByText('"عام"')).toBeVisible();
  await page.getByRole('button', { name: l('تعديل title', 'Edit title') }).first().click();
  await page.mouse.move(0, 0);
  await snapshot('database-data');
});

test('delete confirmation', async ({ page, snapshot, l }) => {
  await open(page, '/project/database/?id=chat-app', l('قاعدة البيانات اللحظية', 'Realtime Database'));
  await page.getByRole('button', { name: l('حذف rooms', 'Delete rooms') }).click();
  await expect(page.getByRole('alertdialog')).toBeVisible();
  await snapshot('database-delete', { modal: true });
});

test('rules with an error', async ({ page, snapshot, l }) => {
  await open(page, '/project/database/rules/?id=chat-app', l('قاعدة البيانات اللحظية', 'Realtime Database'));
  await page.getByLabel(l('القواعد (JSON)', 'Rules (JSON)')).fill('{\n  "rules": {\n    ".read": true,\n    ".write": "auth.uid =="\n  }\n}\n');
  await page.getByRole('button', { name: l('نشر', 'Publish') }).click();
  await expect(page.getByText(l('في القواعد خطأ', 'The rules have an error'))).toBeVisible();
  await page.evaluate(() => window.scrollTo(0, 0));
  await snapshot('database-rules');
});
