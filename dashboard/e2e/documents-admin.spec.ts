import { expect, open, test as base } from './fixtures';
import { LOCKED, MockDocumentsAdmin, SIGNED_IN } from './mock-documents-admin';

// Journeys through the Document Database's Rules, Indexes and Usage tabs, in both languages,
// against the mocked console API (mock-documents-admin.ts).

const test = base.extend<{ docs: MockDocumentsAdmin }>({
  docs: [async ({ page }, use) => {
    const docs = new MockDocumentsAdmin();
    await docs.install(page);
    await use(docs);
  },
  // The mock answers the section's own info call, so every test needs it, used or not.
  { auto: true },
]});

const TITLE = ['قاعدة المستندات', 'Document Database'] as const;
const url = (tab: string) => `/project/documents/${tab}/?id=chat-app`;

test.describe('rules', () => {
  test('publishes a change, shows it as the published version, and discards another', async ({ page, docs, l }) => {
    await open(page, url('rules'), l(...TITLE));
    const editor = page.getByRole('textbox', { name: l('القواعد', 'Rules'), exact: true });
    await expect(editor).toHaveValue(LOCKED);
    const bar = page.getByRole('region', { name: l('تغييرات غير منشورة', 'Unpublished changes') });
    await expect(bar).toHaveCount(0);

    await editor.fill(SIGNED_IN);
    await expect(bar).toBeVisible();
    await page.getByRole('button', { name: l('نشر', 'Publish'), exact: true }).click();
    await expect(page.getByText(l('نُشرت القواعد.', 'Rules published.'))).toBeVisible();
    await expect(bar).toHaveCount(0);
    const versions = page.getByRole('navigation', { name: l('النسخ', 'Versions') }).getByRole('listitem');
    await expect(versions).toHaveCount(2);
    await expect(versions.first()).toContainText(l('منشورة', 'Published'));
    await expect(versions.nth(1)).not.toContainText(l('منشورة', 'Published'));
    expect(docs.published).toBe(true);
    expect(docs.rulesets[0]!.source).toBe(SIGNED_IN);

    await editor.fill(LOCKED);
    await expect(bar).toBeVisible();
    await page.getByRole('button', { name: l('تجاهل', 'Discard'), exact: true }).click();
    await expect(page.getByText(l('تم تجاهل التغييرات', 'Changes discarded'))).toBeVisible();
    await expect(editor).toHaveValue(SIGNED_IN);
    await expect(bar).toHaveCount(0);
  });

  test('loads an older version into the editor as an unpublished change', async ({ page, l }) => {
    await open(page, url('rules'), l(...TITLE));
    const editor = page.getByRole('textbox', { name: l('القواعد', 'Rules'), exact: true });
    await editor.fill(SIGNED_IN);
    await page.getByRole('button', { name: l('نشر', 'Publish'), exact: true }).click();
    await expect(page.getByRole('navigation', { name: l('النسخ', 'Versions') }).getByRole('listitem')).toHaveCount(2);
    await page.getByRole('navigation', { name: l('النسخ', 'Versions') }).getByRole('listitem').nth(1).getByRole('button').click();
    await expect(editor).toHaveValue(LOCKED);
    await expect(page.getByRole('region', { name: l('تغييرات غير منشورة', 'Unpublished changes') })).toBeVisible();
  });

  test('marks an error on its line as you type and will not publish it', async ({ page, l }) => {
    await open(page, url('rules'), l(...TITLE));
    const editor = page.getByRole('textbox', { name: l('القواعد', 'Rules'), exact: true });
    await editor.fill(LOCKED.replace('if false', 'if '));
    const gutter = page.locator('[data-line="6"][data-issue="error"]');
    await expect(gutter).toBeVisible();
    await expect(gutter).toHaveAttribute('title', /mismatched input ';'/);
    await expect(page.getByText(/mismatched input ';'/).first()).toBeVisible();
    await expect(page.getByRole('button', { name: l('نشر', 'Publish'), exact: true })).toBeDisabled();
    await editor.fill(LOCKED.replace('if false', 'if true'));
    await expect(page.locator('[data-issue="error"]')).toHaveCount(0);
    await expect(page.getByRole('button', { name: l('نشر', 'Publish'), exact: true })).toBeEnabled();
  });

  test('simulates a request, and calls the result outdated once the rules change', async ({ page, docs, l }) => {
    await open(page, url('rules'), l(...TITLE));
    await page.getByRole('button', { name: new RegExp(l('ساحة القواعد', 'Rules Playground')) }).click();
    await page.getByRole('textbox', { name: l('الموضع', 'Location') }).fill('users/alice');
    await page.getByRole('button', { name: l('تشغيل', 'Run'), exact: true }).click();
    const result = page.getByTestId('simulation');
    await expect(result).toContainText(l('محاكاة القراءة: مرفوض', 'Simulated read denied'));
    await expect(page.getByText(l('معلومات تفصيلية', 'Detailed information'))).toBeVisible();

    // The rules change after the run: the banner says it is out of date, and a new run is judged by them.
    await page.getByRole('textbox', { name: l('القواعد', 'Rules'), exact: true }).fill(SIGNED_IN);
    await expect(result).toContainText(l('(تعرض محاكاة قديمة)', '(viewing outdated simulation)'));
    await page.getByText(l('مسجّل الدخول', 'Authenticated'), { exact: true }).click();
    await expect(page.getByText(l('كل الحقول اختيارية', 'All fields are optional'))).toBeVisible();
    await page.getByRole('textbox', { name: l('معرّف Firebase (UID)', 'Firebase UID') }).fill('alice');
    await expect(page.getByRole('textbox', { name: l('حمولة المصادقة', 'Authentication payload') })).toHaveValue(/"aud": "chat-app"[\s\S]*"sign_in_provider": "google.com"/);
    await page.getByRole('button', { name: l('تشغيل', 'Run'), exact: true }).click();
    await expect(result).toContainText(l('محاكاة القراءة: مسموح', 'Simulated read allowed'));
    await expect(result).not.toContainText(l('(تعرض محاكاة قديمة)', '(viewing outdated simulation)'));
    expect(docs.simulated.at(-1)).toMatchObject({ method: 'get', path: 'users/alice', auth: { uid: 'alice', provider: 'google.com' }, source: SIGNED_IN });

    // A create takes its data; a path that is not a document is refused before it is sent.
    await page.getByRole('button',{ name: new RegExp(l('نوع المحاكاة', 'Simulation type')) }).click();
    await page.getByRole('option', { name: 'create' }).click();
    await expect(page.getByRole('textbox', { name: l('بيانات المستند (JSON)', 'Firestore data (JSON)') })).toBeVisible();
    await page.getByRole('textbox', { name: l('الموضع', 'Location') }).fill('users');
    await page.getByRole('button', { name: l('تشغيل', 'Run'), exact: true }).click();
    await expect(page.getByText(l('مسار المستند عدد معرّفاته زوجي، مثل users/alice', 'A document path has an even number of ids, such as users/alice'))).toBeVisible();
  });

  test('shows a compile error of the simulated rules', async ({ page, l }) => {
    await open(page, url('rules'), l(...TITLE));
    await page.getByRole('textbox', { name: l('القواعد', 'Rules'), exact: true }).fill(LOCKED.replace('if false', 'if '));
    await page.getByRole('button', { name: new RegExp(l('ساحة القواعد', 'Rules Playground')) }).click();
    await page.getByRole('textbox', { name: l('الموضع', 'Location') }).fill('users/alice');
    await page.getByRole('button', { name: l('تشغيل', 'Run'), exact: true }).click();
    await expect(page.getByText(l('تعذّر تشغيل المحاكاة', 'The simulation could not run'))).toBeVisible();
  });
});

test.describe('indexes', () => {
  test('creates a composite index that builds then is enabled, and deletes it', async ({ page, docs, l }) => {
    await open(page, url('indexes'), l(...TITLE));
    await expect(page.getByText(l('ابنِ الفهارس التي تحتاجها استعلاماتك المركّبة', 'Build the indexes you need to power your compound queries'))).toBeVisible();
    await page.getByRole('button', { name: l('إنشاء فهرس', 'Create index') }).click();
    const dialog = page.getByRole('dialog');
    await dialog.getByRole('button', { name: l('إنشاء', 'Create'), exact: true }).click();
    await expect(dialog.getByText(l('مطلوب', 'Required')).first()).toBeVisible();

    await dialog.getByRole('textbox', { name: l('معرّف المجموعة', 'Collection ID') }).fill('posts');
    await dialog.getByRole('textbox', { name: l('مسار الحقل 1', 'Field path 1') }).fill('author');
    await dialog.getByRole('textbox', { name: l('مسار الحقل 2', 'Field path 2') }).fill('likes');
    await dialog.getByRole('button',{ name: new RegExp(l('ترتيب الحقل 2', 'Order of field 2')) }).click();
    await page.getByRole('option', { name: l('تنازلي', 'Descending') }).click();
    await dialog.getByRole('button', { name: l('إنشاء', 'Create'), exact: true }).click();

    const table = page.getByRole('region', { name: l('الفهارس المركّبة', 'Composite indexes') });
    await expect(table).toContainText('posts');
    await expect(table).toContainText('__name__');
    await expect(table).toContainText(l('قيد البناء…', 'Building…'));
    await expect(table).toContainText(l('مفعّل', 'Enabled'), { timeout: 10_000 });
    expect(docs.indexes[0]!.queryScope).toBe('COLLECTION');
    expect(docs.indexes[0]!.fields.slice(0, 2)).toEqual([{ fieldPath: 'author', order: 'ASCENDING' }, { fieldPath: 'likes', order: 'DESCENDING' }]);

    await page.getByRole('button', { name: l('مزيد من الإجراءات للفهرس ix1', 'More actions for index ix1') }).click();
    await page.getByRole('menuitem', { name: l('حذف', 'Delete') }).click();
    const confirm = page.getByRole('alertdialog');
    await expect(confirm).toContainText(l('بعد حذف هذا الفهرس لن تعمل الاستعلامات التي تعتمد عليه.', 'Once you delete this index, queries that rely on it will no longer work.'));
    await confirm.getByRole('button', { name: l('حذف', 'Delete'), exact: true }).click();
    await expect(page.getByText(l('حُذف الفهرس.', 'Index deleted.'))).toBeVisible();
    await expect(table).toHaveCount(0);
    expect(docs.indexes).toHaveLength(0);
  });

  test('exempts a field from the automatic indexes in two steps, and removes the exemption', async ({ page, docs, l }) => {
    await open(page, url('indexes'), l(...TITLE));
    await page.getByRole('tab', { name: l('تلقائي', 'Automatic') }).click();
    await expect(page.getByText(l('لا استثناءات', 'No exemptions'))).toBeVisible();
    await page.getByRole('button', { name: l('إضافة استثناء', 'Add exemption') }).click();
    const dialog = page.getByRole('dialog');
    await dialog.getByRole('button', { name: l('التالي', 'Next') }).click();
    await expect(dialog.getByText(l('مطلوب', 'Required')).first()).toBeVisible();
    await dialog.getByRole('textbox', { name: l('معرّف المجموعة', 'Collection ID') }).fill('users');
    await dialog.getByRole('textbox', { name: l('مسار الحقل', 'Field path') }).fill('bio');
    await dialog.getByRole('button', { name: l('التالي', 'Next') }).click();
    await expect(dialog.getByText(l('غيّر إعدادات الفهرسة بحذر', 'Change indexes settings carefully'), { exact: false })).toBeVisible();
    await dialog.getByText(l('المصفوفات', 'Arrays'), { exact: true }).click();
    await expect(dialog.getByRole('switch', { name: l('المصفوفات', 'Arrays') })).not.toBeChecked();
    await dialog.getByRole('button', { name: l('حفظ', 'Save') }).click();

    const row = page.getByRole('row', { name: /bio/ });
    await expect(row).toContainText('users');
    await expect(row).toContainText(l('المصفوفات (معطّل)', 'Arrays (disabled)'));
    await expect(row).toContainText(l('لا تغييرات عن إعدادات الفهرسة التلقائية', 'No changes from automatic index settings'));
    // Only the enabled indexes are sent: ascending and descending.
    expect(docs.fields[0]!.indexConfig!.indexes).toEqual([
      { queryScope: 'COLLECTION', fields: [{ order: 'ASCENDING' }] },
      { queryScope: 'COLLECTION', fields: [{ order: 'DESCENDING' }] },
    ]);

    await page.getByRole('button', { name: l('مزيد من الإجراءات للحقل bio', 'More actions for field bio') }).click();
    await page.getByRole('menuitem', { name: l('حذف', 'Delete') }).click();
    const confirm = page.getByRole('alertdialog');
    await expect(confirm).toContainText(l('حذف استثناء', 'Delete exemption'));
    await confirm.getByRole('button', { name: l('حذف', 'Delete'), exact: true }).click();
    await expect(page.getByText(l('لا استثناءات', 'No exemptions'))).toBeVisible();
    expect(docs.fields).toHaveLength(0);
  });

  test('shows the automatic index settings of a new database', async ({ page, l }) => {
    await open(page, url('indexes'), l(...TITLE));
    await page.getByRole('tab', { name: l('تلقائي', 'Automatic') }).click();
    await expect(page.getByText(l('نطاق المجموعة: 3 فهارس مفعّلة', 'Collection scope: 3 indexes enabled'), { exact: false })).toBeVisible();
  });

  test('sets one TTL policy per collection group', async ({ page, docs, l }) => {
    await open(page, url('indexes'), l(...TITLE));
    await page.getByRole('tab', { name: l('مدة البقاء', 'Time to live') }).click();
    await expect(page.getByText(l('لا سياسات لعرضها', 'No policies to display'))).toBeVisible();
    for (const attempt of [0, 1]) {
      await page.getByRole('button', { name: l('إنشاء سياسة', 'Create policy') }).click();
      const dialog = page.getByRole('dialog');
      await expect(dialog.getByText(l('يمكنك ضبط سياسة TTL واحدة لكل مجموعة مجموعات.', 'You can set one TTL policy per collection group.'))).toBeVisible();
      await dialog.getByRole('textbox', { name: l('مجموعة المجموعات', 'Collection group') }).fill('sessions');
      await dialog.getByRole('textbox', { name: l('حقل الطابع الزمني', 'Timestamp field') }).fill(attempt === 0 ? 'expiresAt' : 'other');
      await dialog.getByRole('button', { name: l('إنشاء', 'Create'), exact: true }).click();
      if (attempt === 0) {
        const row = page.getByRole('row', { name: /expiresAt/ });
        await expect(row).toContainText('sessions');
        await expect(row).toContainText(l('تعمل', 'Serving'));
      } else {
        await expect(dialog.getByRole('alert')).toBeVisible();
        await dialog.getByRole('button', { name: l('إلغاء', 'Cancel') }).click();
      }
    }
    expect(docs.ttl).toHaveLength(1);
    await page.getByRole('button', { name: l('مزيد من الإجراءات لسياسة sessions', 'More actions for the policy of sessions') }).click();
    await page.getByRole('menuitem', { name: l('حذف', 'Delete') }).click();
    await page.getByRole('alertdialog').getByRole('button', { name: l('حذف', 'Delete'), exact: true }).click();
    await expect(page.getByText(l('لا سياسات لعرضها', 'No policies to display'))).toBeVisible();
  });
});

test.describe('usage', () => {
  test('shows the counters of each period and charts them', async ({ page, docs, l }) => {
    await open(page, url('usage'), l(...TITLE));
    const reads = page.locator('[data-counter="reads"]');
    await expect(reads).toHaveAttribute('data-value', '50');
    await expect(page.locator('[data-counter="deletes"]')).toHaveAttribute('data-value', '3');
    await expect(page.locator('[data-counter="listenersPeak"]')).toHaveAttribute('data-value', '4');
    await expect(page.locator('[data-counter="rulesDenies"]')).toHaveAttribute('data-value', '25');
    for (const heading of [l('مقاييس الفوترة', 'Billable metrics'), l('مقاييس الاشتراكات', 'Subscription metrics'), l('مقاييس القواعد', 'Rules metrics')]) {
      await expect(page.getByRole('heading', { name: heading })).toBeVisible();
    }
    await expect(page.getByRole('img', { name: new RegExp(l('العمليات \\(في الساعة\\)', 'Operations \\(per hour\\)')) })).toBeVisible();

    const periods: [string, string, number][] = [
      ['7d', l('آخر 7 أيام', 'Last 7 days'), 338],
      ['30d', l('آخر 30 يومًا', 'Last 30 days'), 1442],
      ['60m', l('آخر 60 دقيقة', 'Last 60 minutes'), 4],
    ];
    for (const [period, label, total] of periods) {
      await page.getByRole('button',{ name: new RegExp(l('الفترة', 'Period')) }).click();
      await page.getByRole('option', { name: label }).click();
      await expect(reads).toHaveAttribute('data-value', String(total));
      expect(docs.periods).toContain(period);
    }
    await expect(page.getByRole('img', { name: new RegExp(l('العمليات \\(في الساعة\\)', 'Operations \\(per hour\\)')) })).toBeVisible();
  });

  test('has no serious accessibility violations on the three tabs', async ({ page, l, checkA11y }) => {
    for (const tab of ['rules', 'indexes', 'usage']) {
      await open(page, url(tab), l(...TITLE));
      await expect(page.getByRole('status', { name: /Loading|جارٍ التحميل/ })).toHaveCount(0);
      await checkA11y();
    }
  });
});
