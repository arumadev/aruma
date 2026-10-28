import { expect, open, test } from './fixtures';

// Journeys through the Document Database's data browser, in both languages, against the mocked
// console API. The behaviour to match is the Firebase console's Firestore Data tab, recorded in
// chatapp-8ae21 (f7-production-console.md §1).

const DATA = '/project/documents/?id=chat-app';
const TITLE = ['قاعدة المستندات', 'Document Database'] as const;

test.describe('data browser', () => {
  test('browses collections, documents and typed fields, the path in the address', async ({ page, l }) => {
    await open(page, DATA, l(...TITLE));
    await expect(page.locator('html')).toHaveAttribute('dir', l('rtl', 'ltr'));
    await page.getByRole('link', { name: 'users' }).click();
    await expect(page).toHaveURL(/path=users$/);
    // A document with subcollections and no fields is listed in italics, named as missing.
    const ghost = page.getByRole('link', { name: l('ghost (مستند غير موجود)', 'ghost (missing document)') });
    await expect(ghost).toHaveCSS('font-style', 'italic');
    await page.getByRole('link', { name: 'alice' }).click();
    await expect(page).toHaveURL(/path=users%2Falice$/);

    const fields = page.getByRole('list', { name: l('حقول alice', 'Fields of alice') });
    await expect(fields).toContainText('name: "Alice" (string)');
    await expect(fields).toContainText('age: 9223372036854775807 (int64)');
    await expect(fields).toContainText('score: NaN (double)');
    await expect(fields).toContainText('ratio: 1.5 (double)');
    await expect(fields).toContainText('active: true (boolean)');
    await expect(fields).toContainText('nothing: null (null)');
    await expect(fields).toContainText(l('joined: 17 أكتوبر 2026 في 2:56:25', 'joined: October 17, 2026 at 2:56:25 PM UTC (timestamp)'));
    await expect(fields).toContainText('home: [24.5° N, 0° E] (geopoint)');
    await expect(fields).toContainText('friend: /users/bob (reference)');
    await expect(fields).toContainText('avatar: AAECAw== (bytes)');
    await expect(fields).toContainText('city: "الرياض" (string)');
    await page.getByRole('button', { name: l('إخفاء حقول address', 'Hide the fields of address') }).click();
    await expect(fields).not.toContainText('الرياض');

    // The breadcrumb leads back up, and "Edit path" goes anywhere.
    const crumbs = page.getByRole('navigation', { name: l('مسار المستند', 'Document path') });
    await expect(crumbs.locator('[aria-current="page"]')).toHaveText('alice');
    await page.getByRole('button', { name: l('تعديل المسار', 'Edit path') }).click();
    await page.getByLabel(l('المسار', 'Path')).fill('/users/ghost/posts');
    await page.getByRole('button', { name: l('انتقال', 'Go') }).click();
    await expect(page).toHaveURL(/path=users%2Fghost%2Fposts$/);
    await expect(page.getByText(l('هذا المستند غير موجود.', 'This document does not exist.'), { exact: false })).toBeVisible();
    await page.getByRole('link', { name: 'p1' }).click();
    await expect(page.getByRole('list', { name: l('حقول p1', 'Fields of p1') })).toContainText('title: "hi" (string)');
    await crumbs.getByRole('link', { name: 'users' }).click();
    await expect(page.getByRole('link', { name: 'bob' })).toBeVisible();
    await page.goBack();
    await expect(page.getByRole('link', { name: 'p2' })).toBeVisible();
  });

  test('pages a collection with "Load more"', async ({ page, api, l }) => {
    api.documents.store('chat-app').pageSize = 2;
    await open(page, `${DATA}&path=users`, l(...TITLE));
    const list = page.getByRole('list', { name: l('مستندات users', 'Documents of users') });
    await expect(list.getByRole('link')).toHaveCount(2);
    await page.getByRole('button', { name: l('تحميل المزيد', 'Load more') }).click();
    await expect(list.getByRole('link')).toHaveCount(3);
    await expect(page.getByRole('button', { name: l('تحميل المزيد', 'Load more') })).toHaveCount(0);
  });

  test('adds a document with checked ids, Auto-ID and typed fields, and refuses a taken id', async ({ page, api, l }) => {
    await open(page, `${DATA}&path=users`, l(...TITLE));
    await page.getByRole('button', { name: l('إضافة مستند', 'Add document') }).click();
    const dialog = page.getByRole('dialog', { name: l('إضافة مستند', 'Add a document') });
    const id = dialog.getByLabel(l('معرّف المستند', 'Document ID'));
    const save = dialog.getByRole('button', { name: l('حفظ', 'Save') });
    for (const [typed, message] of [
      ['a/b', l('لا يحتوي على شرطة مائلة (/)', "Can't contain a forward slash (/)")],
      ['__a__', l('لا يبدأ ولا ينتهي بشرطتين سفليتين (__)', "Can't start and end with double underscores (__)")],
      ['..', l('لا يبدأ بنقطة (.)', "Can't start with a dot (.)")],
    ] as const) {
      await id.fill(typed);
      await id.blur();
      await expect(dialog.getByText(message)).toBeVisible();
      await expect(save).toBeDisabled();
    }
    await dialog.getByRole('button', { name: l('معرّف تلقائي', 'Auto-ID') }).click();
    await expect(id).toHaveValue(/^[A-Za-z0-9]{20}$/);
    await id.fill('carol');

    // An empty field name is required.
    const name = dialog.getByRole('textbox', { name: l('الحقل', 'Field'), exact: true });
    await name.focus();
    await name.blur();
    await expect(dialog.getByText(l('مطلوب', 'Required'), { exact: true })).toBeVisible();
    await name.fill('age');
    await dialog.getByRole('button', { name: new RegExp(l('النوع', 'Type')) }).click();
    await page.getByRole('option', { name: 'int64' }).click();
    await dialog.getByLabel(l('القيمة', 'Value')).fill('-9223372036854775808');
    await save.click();
    await expect(dialog).toHaveCount(0);
    await expect(page).toHaveURL(/path=users%2Fcarol$/);
    await expect(page.getByRole('list', { name: l('حقول carol', 'Fields of carol') })).toContainText('age: -9223372036854775808 (int64)');
    const create = api.calls.find((c) => c.method === 'POST' && c.path.endsWith('/documents/documents'));
    expect(create?.body).toEqual({ documentId: 'carol', fields: { age: { integerValue: '-9223372036854775808' } } });
    expect(create?.csrf).toBe('csrf-token');

    // A second add of the same id fails: a create, not an overwrite.
    await page.getByRole('button', { name: l('إضافة مستند', 'Add document') }).click();
    await dialog.getByLabel(l('معرّف المستند', 'Document ID')).fill('bob');
    await dialog.getByRole('button', { name: l('إزالة الحقل', 'Remove field') }).click();
    await dialog.getByRole('button', { name: l('حفظ', 'Save') }).click();
    await expect(dialog.getByText(l('يوجد مستند بهذا المعرّف.', 'A document with this ID already exists.'))).toBeVisible();
    expect(api.documents.store('chat-app').docs.get('users/bob')?.fields).toEqual({ name: { stringValue: 'Bob' } });
  });

  test('starts a collection in two steps, its first document required', async ({ page, l }) => {
    await open(page, DATA, l(...TITLE));
    await page.getByRole('button', { name: l('بدء مجموعة', 'Start collection') }).click();
    const dialog = page.getByRole('dialog', { name: l('بدء مجموعة', 'Start a collection') });
    const next = dialog.getByRole('button', { name: l('التالي', 'Next') });
    await expect(next).toBeDisabled();
    await dialog.getByLabel(l('معرّف المجموعة', 'Collection ID')).fill('cities');
    await next.click();
    await expect(dialog.getByText('/cities', { exact: true })).toBeVisible();
    await dialog.getByLabel(l('معرّف المستند', 'Document ID')).fill('riyadh');
    await dialog.getByRole('textbox', { name: l('الحقل', 'Field'), exact: true }).fill('location');
    await dialog.getByRole('button', { name: new RegExp(l('النوع', 'Type')) }).click();
    await page.getByRole('option', { name: 'geopoint' }).click();
    const lat = dialog.getByLabel(l('خط العرض', 'Latitude'));
    await lat.fill('91');
    await lat.blur();
    await expect(dialog.getByText(l('بين ‎-90 و90', 'Must be between -90 and 90'))).toBeVisible();
    await expect(dialog.getByRole('button', { name: l('حفظ', 'Save') })).toBeDisabled();
    await lat.fill('24.7136');
    await dialog.getByLabel(l('خط الطول', 'Longitude')).fill('46.6753');
    await dialog.getByRole('button', { name: l('حفظ', 'Save') }).click();
    await expect(page).toHaveURL(/path=cities%2Friyadh$/);
    await expect(page.getByRole('list', { name: l('حقول riyadh', 'Fields of riyadh') })).toContainText('location: [24.7136° N, 46.6753° E] (geopoint)');
    const roots = page.getByRole('list', { name: l('مجموعات /', 'Collections of /') });
    await expect(roots.getByRole('link', { name: 'cities' })).toHaveAttribute('aria-current', 'page');
  });

  test('edits a field over the version read, and says so when another edit came first', async ({ page, api, l }) => {
    await open(page, `${DATA}&path=users%2Falice`, l(...TITLE));
    await page.getByRole('button', { name: l('تعديل الحقل ratio', 'Edit field ratio') }).click();
    const dialog = page.getByRole('dialog', { name: l('تعديل حقل', 'Edit field') });
    await expect(dialog.getByRole('textbox', { name: l('الحقل', 'Field'), exact: true })).toHaveValue('ratio');
    await dialog.getByLabel(l('القيمة', 'Value')).fill('-Infinity');
    await dialog.getByRole('button', { name: l('تحديث', 'Update') }).click();
    await expect(dialog).toHaveCount(0);
    await expect(page.getByRole('list', { name: l('حقول alice', 'Fields of alice') })).toContainText('ratio: -Infinity (double)');
    const edit = api.calls.find((c) => c.method === 'PATCH');
    expect(edit?.body).toMatchObject({ fields: { ratio: { doubleValue: '-Infinity' } }, updateMask: ['`ratio`'] });
    expect((edit?.body as { updateTime?: string }).updateTime).toMatch(/Z$/);
    // The rest of the document is as it was, its int64 to the last bit.
    expect(api.documents.store('chat-app').docs.get('users/alice')?.fields.age).toEqual({ integerValue: '9223372036854775807' });

    // A nested field is edited by its path; another writer got there first.
    await page.getByRole('button', { name: l('تعديل الحقل address.city', 'Edit field address.city') }).click();
    api.documents.store('chat-app').touch('users/alice');
    await dialog.getByLabel(l('القيمة', 'Value')).fill('جدة');
    await dialog.getByRole('button', { name: l('تحديث', 'Update') }).click();
    await expect(dialog.getByText(l('تغيّر هذا المستند منذ فتحته.', 'This document changed since you opened it.'), { exact: false })).toBeVisible();
    await dialog.getByRole('button', { name: l('إعادة تحميل المستند', 'Reload document') }).click();
    await page.getByRole('button', { name: l('تعديل الحقل address.city', 'Edit field address.city') }).click();
    await dialog.getByLabel(l('القيمة', 'Value')).fill('جدة');
    await dialog.getByRole('button', { name: l('تحديث', 'Update') }).click();
    await expect(page.getByRole('list', { name: l('حقول alice', 'Fields of alice') })).toContainText('city: "جدة" (string)');
    expect(api.calls.filter((c) => c.method === 'PATCH').at(-1)?.body).toMatchObject({ updateMask: ['`address`.`city`'] });
  });

  test('deletes a field after a confirmation, or at once with a shift-click', async ({ page, api, l }) => {
    await open(page, `${DATA}&path=users%2Falice`, l(...TITLE));
    const fields = page.getByRole('list', { name: l('حقول alice', 'Fields of alice') });
    await page.getByRole('button', { name: l('حذف الحقل nothing', 'Delete field nothing') }).click();
    const dialog = page.getByRole('alertdialog', { name: l('حذف البيانات', 'Delete Data') });
    await expect(dialog).toContainText(l("سيحذف هذا البيانات في 'nothing' من المستند", "This will delete the data at 'nothing' from the document"));
    await expect(dialog).toContainText('/users/alice');
    await dialog.getByRole('button', { name: l('حذف', 'Delete') }).click();
    await expect(fields).not.toContainText('nothing:');

    await page.getByRole('button', { name: l('حذف الحقل active', 'Delete field active') }).click({ modifiers: ['Shift'] });
    await expect(page.getByRole('alertdialog')).toHaveCount(0);
    await expect(fields).not.toContainText('active:');
    const stored = api.documents.store('chat-app').docs.get('users/alice')!.fields;
    expect(Object.keys(stored)).not.toContain('active');
    expect(stored.joined).toEqual({ timestampValue: '2026-10-17T14:56:25.123456789Z' });
  });

  test('deletes a document and everything under it ("Start delete")', async ({ page, api, l }) => {
    await open(page, `${DATA}&path=users%2Fghost`, l(...TITLE));
    await page.getByRole('button', { name: l('إجراءات أخرى للمستند ghost', 'More actions for document ghost') }).click();
    await page.getByRole('menuitem', { name: l('حذف المستند', 'Delete document') }).click();
    const dialog = page.getByRole('alertdialog', { name: l('حذف البيانات', 'Delete Data') });
    await expect(dialog).toContainText(l('ومعها كل البيانات المتداخلة', 'including all nested data'));
    await dialog.getByRole('button', { name: l('بدء الحذف', 'Start delete') }).click();
    await expect(page).toHaveURL(/path=users$/);
    await expect(page.getByRole('link', { name: /ghost/ })).toHaveCount(0);
    expect([...api.documents.store('chat-app').docs.keys()].some((p) => p.startsWith('users/ghost'))).toBe(false);
  });

  test('deletes a collection once its id is typed, a step at a time until done', async ({ page, api, l }) => {
    const store = api.documents.store('chat-app');
    store.deleteStep = 2;
    await open(page, `${DATA}&path=users`, l(...TITLE));
    await page.getByRole('button', { name: l('إجراءات أخرى للمجموعة users', 'More actions for collection users') }).click();
    await page.getByRole('menuitem', { name: l('حذف المجموعة', 'Delete collection') }).click();
    const dialog = page.getByRole('alertdialog', { name: l('حذف هذه المجموعة؟', 'Delete this collection?') });
    await expect(dialog).toContainText(l('أكّد حذف هذه المجموعة بكتابة معرّفها: users', 'Confirm you want to delete this collection by typing its ID: users'));
    const confirm = dialog.getByRole('button', { name: l('حذف', 'Delete') });
    await expect(confirm).toBeDisabled();
    await dialog.getByLabel(l('معرّف المجموعة', 'Collection ID')).fill('user');
    await expect(confirm).toBeDisabled();
    await dialog.getByLabel(l('معرّف المجموعة', 'Collection ID')).fill('users');
    await confirm.click();
    await expect(dialog).toHaveCount(0);
    await expect(page).toHaveURL(/\?id=chat-app$/);
    await expect(page.getByRole('link', { name: 'users' })).toHaveCount(0);
    await expect(page.getByRole('link', { name: 'rooms' })).toBeVisible();
    // Four documents, two a step: the dashboard asked until the server said done.
    expect(api.calls.filter((c) => c.path.endsWith('documents:recursiveDelete')).length).toBe(2);
    expect([...store.docs.keys()]).toEqual(['rooms/r1']);
  });

  test('shows Arabic, accents, CJK and emoji intact, accessibly', async ({ page, l, checkA11y }) => {
    await open(page, `${DATA}&path=rooms%2Fr1`, l(...TITLE));
    await expect(page.getByRole('list', { name: l('حقول r1', 'Fields of r1') })).toContainText('title: "مرحبا 👋 café 你好" (string)');
    await checkA11y();
    await page.getByRole('button', { name: l('إضافة مستند', 'Add document') }).click();
    await expect(page.getByRole('dialog')).toBeVisible();
    await page.evaluate(() => Promise.all(document.getAnimations().map((a) => a.finished)));
    await checkA11y();
  });
});
