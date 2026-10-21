import { expect, open, test } from './fixtures';

// Journeys through the Realtime Database pages, in both languages, against the mocked API.

const DATA = '/project/database/?id=chat-app';
const RULES = '/project/database/rules/?id=chat-app';

test.describe('data', () => {
  test('browses, edits a leaf, adds a child and deletes with a confirmation', async ({ page, api, l }) => {
    const store = api.database.store('chat-app');
    await open(page, DATA, l('قاعدة البيانات اللحظية', 'Realtime Database'));
    await expect(page.getByText('https://chat-app-default-rtdb.rtdb.console.example.com')).toBeVisible();
    await expect(page.getByRole('tab', { name: l('البيانات', 'Data') })).toHaveAttribute('aria-selected', 'true');

    // Nodes open on demand.
    await expect(page.getByText('general', { exact: true })).toHaveCount(0);
    await page.getByRole('button', { name: l('توسيع rooms', 'Expand rooms') }).click();
    await page.getByRole('button', { name: l('توسيع general', 'Expand general') }).click();
    await expect(page.getByText('"عام"')).toBeVisible();

    // A leaf is edited in place; the value keeps its type.
    await page.getByRole('button', { name: l('تعديل members', 'Edit members') }).first().click();
    const value = page.getByLabel(l('القيمة', 'Value'));
    await expect(value).toHaveValue('3');
    await value.fill('4');
    await expect(page.getByText(l('يُحفظ رقمًا.', 'Saved as a number.'))).toBeVisible();
    await page.getByRole('button', { name: l('حفظ', 'Save') }).click();
    await expect(page.getByRole('status').getByText(l('حُفظ /rooms/general/members.', 'Saved /rooms/general/members.'))).toBeVisible();
    expect((store.data.rooms as Record<string, Record<string, unknown>>).general?.members).toBe(4);
    expect(api.calls.find((c) => c.method === 'PUT' && c.path.endsWith('/database/data'))?.csrf).toBe('csrf-token');

    // A child: the key is checked before anything is sent.
    await page.getByRole('button', { name: l('إضافة ابن إلى rooms', 'Add a child to rooms') }).click();
    await page.getByLabel(l('المفتاح', 'Key'), { exact: true }).fill('team.chat');
    await page.getByLabel(l('القيمة', 'Value')).fill('{"title": "الفريق"}');
    await page.getByRole('button', { name: l('إضافة', 'Add'), exact: true }).click();
    await expect(page.getByText(l('لا يحتوي المفتاح على . # $ [ ] / ولا على محارف التحكم.', "A key can't contain . # $ [ ] / or control characters."))).toBeVisible();
    expect(store.calls.filter((c) => c.method === 'PUT')).toHaveLength(1);
    await page.getByLabel(l('المفتاح', 'Key'), { exact: true }).fill('team');
    await page.getByRole('button', { name: l('إضافة', 'Add'), exact: true }).click();
    await expect(page.getByRole('button', { name: l('توسيع team', 'Expand team') })).toBeVisible();
    expect((store.data.rooms as Record<string, unknown>).team).toEqual({ title: 'الفريق' });

    // Deleting asks first, in the page.
    await page.getByRole('button', { name: l('حذف random', 'Delete random') }).click();
    const dialog = page.getByRole('alertdialog');
    await expect(dialog.getByText('/rooms/random')).toBeVisible();
    await dialog.getByRole('button', { name: l('حذف', 'Delete') }).click();
    await expect(dialog).toBeHidden();
    await expect(page.getByText('random', { exact: true })).toHaveCount(0);
    expect((store.data.rooms as Record<string, unknown>).random).toBeUndefined();
  });

  test('pages through a big list', async ({ page, l }) => {
    await open(page, DATA, l('قاعدة البيانات اللحظية', 'Realtime Database'));
    await page.getByRole('button', { name: l('توسيع messages', 'Expand messages') }).click();
    const list = page.getByRole('list', { name: l('أبناء /messages', 'Children of /messages') });
    await expect(list.getByRole('listitem')).toHaveCount(50);
    await page.getByRole('button', { name: l('عرض المزيد', 'Load more') }).click();
    await expect(list.getByRole('listitem')).toHaveCount(60);
    await expect(page.getByRole('button', { name: l('عرض المزيد', 'Load more') })).toHaveCount(0);
  });
});

test.describe('rules', () => {
  test('saves with an error shown at its line, then the fix', async ({ page, api, l }) => {
    const store = api.database.store('chat-app');
    await open(page, RULES, l('قاعدة البيانات اللحظية', 'Realtime Database'));
    const editor = page.getByLabel(l('القواعد (JSON)', 'Rules (JSON)'));
    await expect(editor).toHaveValue('{"rules": {".read": false, ".write": false}}');

    await editor.fill('{\n  "rules": {\n    ".read": "auth != null",\n    ".write": "auth.uid =="\n  }\n}\n');
    await page.getByRole('button', { name: l('نشر', 'Publish') }).click();
    await expect(page.getByText(l('في القواعد خطأ', 'The rules have an error'))).toBeVisible();
    const at = page.getByRole('button', { name: l('السطر 4، العمود 27:', 'Line 4, column 27:') });
    await expect(at).toBeVisible();
    await at.click();
    await expect(editor).toBeFocused();
    expect(store.rules).toBe('{"rules": {".read": false, ".write": false}}');

    await editor.fill('{\n  "rules": {\n    ".read": "auth != null",\n    ".write": "auth.uid == \'admin\'"\n  }\n}\n');
    await expect(page.getByText(l('في القواعد خطأ', 'The rules have an error'))).toHaveCount(0);
    await page.getByRole('button', { name: l('نشر', 'Publish') }).click();
    await expect(page.getByRole('status').getByText(l('نُشرت القواعد.', 'Rules published.'))).toBeVisible();
    expect(store.rules).toContain(`"auth.uid == 'admin'"`);
  });

  test('warns while test mode opens the database', async ({ page, l }) => {
    await open(page, RULES, l('قاعدة البيانات اللحظية', 'Realtime Database'));
    await page.getByRole('button', { name: l('وضع الاختبار', 'Test mode') }).click();
    await expect(page.getByText(l('بياناتك متاحة للجميع', 'Anyone can reach your data'))).toBeVisible();
    await page.getByRole('button', { name: l('الوضع المقفل', 'Locked mode') }).click();
    await expect(page.getByText(l('بياناتك متاحة للجميع', 'Anyone can reach your data'))).toHaveCount(0);
  });
});
