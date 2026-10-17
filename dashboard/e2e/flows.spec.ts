import { expect, open, test } from './fixtures';

// User journeys through the dashboard, in both languages, against the mocked console API.

test.describe('sign-in', () => {
  test('sends a signed-out visitor to sign in and back to the page they asked for', async ({ page, api, l }) => {
    api.store.signedIn = false;
    await page.goto('/project/keys/?id=chat-app');
    await expect(page).toHaveURL(/\/login\/\?next=%2Fproject%2Fkeys%2F%3Fid%3Dchat-app$/);
    await page.getByLabel(l('البريد الإلكتروني', 'Email')).fill('layla@example.com');
    await page.getByLabel(l('كلمة المرور', 'Password')).fill('correct horse battery');
    await page.getByRole('button', { name: l('تسجيل الدخول', 'Sign in') }).click();
    await expect(page).toHaveURL('/project/keys/?id=chat-app');
    await expect(page.getByRole('heading', { level: 1, name: l('مفاتيح API', 'API keys') })).toBeVisible();
  });

  test('gives one message for every failure and the retry time when throttled', async ({ page, api, l }) => {
    api.store.signedIn = false;
    await page.goto('/login/');
    await page.getByLabel(l('البريد الإلكتروني', 'Email')).fill('nobody@example.com');
    await page.getByLabel(l('كلمة المرور', 'Password')).fill('wrong');
    const submit = page.getByRole('button', { name: l('تسجيل الدخول', 'Sign in') });
    await submit.click();
    await expect(page.getByRole('alert').filter({ hasText: l('البريد الإلكتروني أو كلمة المرور غير صحيحة.', 'The email or password is incorrect.') })).toBeVisible();
    await submit.click();
    await submit.click();
    await submit.click();
    await expect(page.getByRole('alert').filter({ hasText: l('أعد المحاولة بعد 42 ثانية', 'Try again in 42 seconds') })).toBeVisible();
    await expect(submit).toBeDisabled();
  });

  test('routes first boot to setup and creates the first account', async ({ page, api, l }) => {
    api.store.status.setupRequired = true;
    api.store.signedIn = false;
    await page.goto('/');
    await expect(page).toHaveURL('/setup/');
    await page.getByLabel(l('رمز الإعداد', 'Setup code')).fill('SETUP-CODE');
    await page.getByLabel(l('البريد الإلكتروني', 'Email')).fill('owner@example.com');
    await page.getByLabel(l('كلمة المرور', 'Password')).fill('correct horse battery');
    await page.getByRole('button', { name: l('إنشاء الحساب', 'Create account') }).click();
    await expect(page).toHaveURL('/projects/');
    const setup = api.calls.find((c) => c.path === '/setup');
    expect(setup?.body).toMatchObject({ setupCode: 'SETUP-CODE', email: 'owner@example.com', locale: l('ar', 'en') });
  });

  test('signs out', async ({ page, api, l }) => {
    await open(page, '/projects/', l('المشاريع', 'Projects'));
    await page.getByRole('button', { name: l('قائمة الحساب layla@example.com', 'Account menu for layla@example.com') }).click();
    await page.getByRole('menuitem', { name: l('تسجيل الخروج', 'Sign out') }).click();
    await expect(page).toHaveURL('/login/');
    expect(api.store.signedIn).toBe(false);
  });
});

test.describe('projects', () => {
  test('filters projects and shows deleted ones on request', async ({ page, l }) => {
    await open(page, '/projects/', l('المشاريع', 'Projects'));
    const cards = page.getByRole('list', { name: l('المشاريع', 'Projects') }).getByRole('listitem');
    await expect(cards).toHaveCount(2);
    await page.getByLabel(l('تصفية المشاريع', 'Filter projects')).fill('maps');
    await expect(cards).toHaveCount(1);
    await page.getByLabel(l('تصفية المشاريع', 'Filter projects')).fill('');
    await page.getByText(l('إظهار المشاريع المحذوفة', 'Show deleted projects')).click();
    await expect(cards).toHaveCount(3);
    await expect(cards.filter({ hasText: 'Old Store' })).toContainText(l('محذوف', 'Deleted'));
  });

  test('creates a project from a suggested, checked id', async ({ page, api, l }) => {
    await open(page, '/projects/', l('المشاريع', 'Projects'));
    await page.getByRole('button', { name: l('مشروع جديد', 'New project') }).click();
    const dialog = page.getByRole('dialog');
    await dialog.getByLabel(l('اسم المشروع', 'Project name')).fill('Weather Station');
    const id = dialog.getByLabel(l('معرّف المشروع', 'Project ID'));
    await expect(id).toHaveValue('weather-station');
    await expect(id).toHaveAttribute('dir', 'ltr');
    await expect(dialog.getByText(l('متاح', 'Available'))).toBeVisible();
    await id.fill('chat-app');
    await expect(dialog.getByText(l('هذا المعرّف مستخدم', 'This ID is taken'))).toBeVisible();
    await id.fill('weather-01');
    await expect(dialog.getByText(l('متاح', 'Available'))).toBeVisible();
    await dialog.getByRole('button', { name: l('إنشاء المشروع', 'Create project') }).click();
    await expect(page).toHaveURL('/project/?id=weather-01');
    await expect(page.getByRole('heading', { level: 1, name: 'Weather Station' })).toBeVisible();
    expect(api.calls.find((c) => c.method === 'POST' && c.path === '/projects')).toMatchObject({
      body: { projectId: 'weather-01', displayName: 'Weather Station' },
      csrf: 'csrf-token',
    });
  });

  test('keeps identifiers left to right in any language', async ({ page, l }) => {
    await open(page, '/project/?id=chat-app', 'Chat App');
    const appId = page.getByRole('link', { name: /Chat Android/ }).getByText('1:123456789012:android:6b7fd59bb891c192');
    await expect(appId).toHaveAttribute('dir', 'ltr');
    await expect(page.locator('html')).toHaveAttribute('dir', l('rtl', 'ltr'));
    await expect(page.getByText(l('قريبًا', 'Coming soon'))).toHaveCount(5);
  });
});

test.describe('apps', () => {
  test('registers an Android app and downloads google-services.json', async ({ page, l }) => {
    await open(page, '/project/apps/new/?id=chat-app', l('إضافة تطبيق', 'Add an app'));
    await page.getByText('Android', { exact: true }).click();
    await page.getByRole('button', { name: l('التالي', 'Next') }).click();
    await expect(page.getByText(l('لا يمكن تغيير هذا أبدًا', 'This can never change'))).toBeVisible();
    const identifier = page.getByLabel(l('اسم الحزمة', 'Package name'));
    await identifier.fill('not a package');
    await page.getByRole('button', { name: l('التالي', 'Next') }).click();
    await expect(page.getByText(l('اسم الحزمة يكون بصيغة com.example.app.', 'A package name looks like com.example.app.'))).toBeVisible();
    await identifier.fill('com.example.chat.beta');
    await page.getByRole('button', { name: l('التالي', 'Next') }).click();
    await page.getByLabel(l('الاسم المستعار للتطبيق', 'App nickname')).fill('Chat Beta');
    await page.getByRole('button', { name: l('تسجيل التطبيق', 'Register app') }).click();
    const download = page.waitForEvent('download');
    await page.getByRole('button', { name: l('تنزيل google-services.json', 'Download google-services.json') }).click();
    expect((await download).suggestedFilename()).toBe('google-services.json');
    await page.getByRole('link', { name: l('الذهاب إلى التطبيق', 'Go to the app') }).click();
    await expect(page.getByRole('heading', { level: 1, name: 'Chat Beta' })).toBeVisible();
  });

  test('shows the web snippet after registering a web app', async ({ page, l }) => {
    await open(page, '/project/apps/new/?id=chat-app', l('إضافة تطبيق', 'Add an app'));
    await page.getByText(l('الويب', 'Web'), { exact: true }).click();
    await page.getByRole('button', { name: l('التالي', 'Next') }).click();
    await page.getByRole('button', { name: l('تسجيل التطبيق', 'Register app') }).click();
    await expect(page.locator('pre').filter({ hasText: 'const firebaseConfig' })).toHaveAttribute('dir', 'ltr');
  });

  test('adds and removes SHA fingerprints, renames, deletes and restores', async ({ page, l }) => {
    await open(page, '/project/app/?id=chat-app&app=1:123456789012:android:6b7fd59bb891c192', 'Chat Android');
    const table = page.getByRole('table', { name: l('بصمات شهادات SHA', 'SHA certificate fingerprints') });
    await expect(table.getByRole('row')).toHaveCount(3);
    await page.getByLabel(l('إضافة بصمة', 'Add a fingerprint')).fill('12:34:56:78:90:ab:cd:ef:12:34:56:78:90:ab:cd:ef:12:34:56:78');
    await page.getByRole('button', { name: l('إضافة', 'Add'), exact: true }).click();
    await expect(table.getByRole('row')).toHaveCount(4);
    await table.getByRole('button', { name: l('حذف البصمة', 'Delete fingerprint') }).first().click();
    await page.getByRole('alertdialog').getByRole('button', { name: l('حذف البصمة', 'Delete fingerprint') }).click();
    await expect(table.getByRole('row')).toHaveCount(3);

    await page.getByRole('button', { name: l('إعادة التسمية', 'Rename') }).click();
    await page.getByRole('dialog').getByLabel(l('الاسم المستعار للتطبيق', 'App nickname')).fill('Chat for Android');
    await page.getByRole('dialog').getByRole('button', { name: l('حفظ', 'Save') }).click();
    await expect(page.getByRole('heading', { level: 1, name: 'Chat for Android' })).toBeVisible();

    await page.getByRole('button', { name: l('حذف التطبيق', 'Delete app') }).click();
    await expect(page.getByRole('alertdialog')).toContainText(l('خلال 30 يومًا', 'within 30 days'));
    await page.getByRole('alertdialog').getByRole('button', { name: l('حذف التطبيق', 'Delete app') }).click();
    await expect(page.getByText(l('هذا التطبيق محذوف', 'This app is deleted'))).toBeVisible();
    await page.getByRole('button', { name: l('استرجاع التطبيق', 'Restore app') }).click();
    await expect(page.getByText(l('هذا التطبيق محذوف', 'This app is deleted'))).toBeHidden();
  });

  test('downloads firebase_options.dart for the chosen apps', async ({ page, api, l }) => {
    await open(page, '/project/?id=chat-app', 'Chat App');
    const download = page.waitForEvent('download');
    await page.getByRole('button', { name: l('تنزيل firebase_options.dart', 'Download firebase_options.dart') }).click();
    expect((await download).suggestedFilename()).toBe('firebase_options.dart');
    expect(api.calls.at(-1)?.path).toBe('/projects/chat-app/flutterOptions');
  });
});

test.describe('API keys', () => {
  test('explains keys are not secret, rotates, disables and edits restrictions', async ({ page, l }) => {
    await open(page, '/project/keys/?id=chat-app', l('مفاتيح API', 'API keys'));
    await expect(page.getByText(l('مفتاح API ليس سرًا', 'An API key is not a secret'))).toBeVisible();
    const rows = page.getByRole('table').getByRole('row');
    await expect(rows).toHaveCount(5);

    await rows.filter({ hasText: 'Android key' }).getByRole('button').last().click();
    await page.getByRole('menuitem', { name: l('تدوير', 'Rotate') }).click();
    await expect(page.getByRole('alertdialog')).toContainText(l('يبقى المفتاح القديم عاملًا حتى تعطّله', 'The old key keeps working until you disable it'));
    await page.getByRole('alertdialog').getByRole('button', { name: l('تدوير', 'Rotate') }).click();
    await expect(rows).toHaveCount(6);

    await rows.filter({ hasText: 'Browser key' }).getByRole('button').last().click();
    await page.getByRole('menuitem', { name: l('تعطيل', 'Disable') }).click();
    await page.getByRole('alertdialog').getByRole('button', { name: l('تعطيل', 'Disable') }).click();
    await expect(rows.filter({ hasText: 'Browser key' })).toContainText(l('معطّل', 'Disabled'));

    await rows.filter({ hasText: 'iOS key' }).getByRole('button').last().click();
    await page.getByRole('menuitem', { name: l('تعديل', 'Edit') }).click();
    const dialog = page.getByRole('dialog');
    await dialog.getByRole('button', { name: new RegExp(l('بلا قيود', 'None')) }).click();
    await page.getByRole('option', { name: l('تطبيقات iOS', 'iOS apps') }).click();
    await dialog.getByLabel(l('معرّفات الحزم المسموح بها', 'Allowed bundle IDs')).fill('com.example.chat');
    await dialog.getByRole('button', { name: l('حفظ', 'Save') }).click();
    await expect(rows.filter({ hasText: 'iOS key' })).toContainText(l('تطبيق iOS واحد', '1 iOS app'));
  });
});

test.describe('service accounts', () => {
  test('creates an account and downloads a key once', async ({ page, l }) => {
    await open(page, '/project/service-accounts/?id=chat-app', l('حسابات الخدمة', 'Service accounts'));
    await page.getByRole('button', { name: l('إنشاء حساب خدمة', 'Create service account') }).first().click();
    await page.getByRole('dialog').getByLabel(l('معرّف الحساب', 'Account ID')).fill('ci-deployer');
    await page.getByRole('dialog').getByRole('button', { name: l('إنشاء حساب خدمة', 'Create service account') }).click();
    const card = page.getByRole('listitem').filter({ hasText: 'ci-deployer@chat-app.sa.console.example.com' });
    await expect(card).toContainText(l('لا مفاتيح.', 'No keys.'));
    await card.getByRole('button', { name: l('إنشاء مفتاح', 'Generate key') }).click();
    await expect(page.getByRole('alertdialog')).toContainText(l('لا تحتفظ أروما بالمفتاح الخاص', "Aruma doesn't keep the private key"));
    const download = page.waitForEvent('download');
    await page.getByRole('alertdialog').getByRole('button', { name: l('إنشاء وتنزيل', 'Generate and download') }).click();
    expect((await download).suggestedFilename()).toMatch(/^chat-app-9d3c[0-9a-f]{8}\.json$/);
    await expect(card.getByRole('table')).toBeVisible();
  });
});

test.describe('project settings', () => {
  test('renames the project everywhere it is shown', async ({ page, api, l }) => {
    await open(page, '/project/settings/?id=chat-app', l('إعدادات المشروع', 'Project settings'));
    await page.getByLabel(l('اسم المشروع', 'Project name')).fill('Chat Platform');
    await page.getByRole('button', { name: l('حفظ', 'Save') }).click();
    await expect(page.getByRole('banner').getByRole('button', { name: /Chat Platform/ })).toBeVisible();
    expect(api.calls.find((c) => c.method === 'PATCH')).toMatchObject({ body: { displayName: 'Chat Platform', etag: '1_chat-app' } });
  });

  test('deletes only after the id is typed, then restores', async ({ page, l }) => {
    await open(page, '/project/settings/?id=chat-app', l('إعدادات المشروع', 'Project settings'));
    await page.getByRole('button', { name: l('حذف المشروع', 'Delete project') }).click();
    const dialog = page.getByRole('alertdialog');
    const confirm = dialog.getByRole('button', { name: l('حذف المشروع', 'Delete project') });
    await expect(dialog).toContainText(l('17 نوفمبر 2026', 'November 17, 2026'));
    await expect(confirm).toBeDisabled();
    await dialog.getByLabel(l('معرّف المشروع', 'Project ID')).fill('chat-ap');
    await expect(confirm).toBeDisabled();
    await dialog.getByLabel(l('معرّف المشروع', 'Project ID')).fill('chat-app');
    await confirm.click();
    await expect(page.getByText(l('هذا المشروع محذوف', 'This project is deleted'))).toBeVisible();
    await page.getByRole('button', { name: l('استرجاع المشروع', 'Restore project') }).click();
    await expect(page.getByText(l('هذا المشروع محذوف', 'This project is deleted'))).toBeHidden();
  });
});

test.describe('account', () => {
  test('creates a CLI token after re-checking the password and shows it once', async ({ page, api, l }) => {
    await open(page, '/account/', l('الحساب', 'Account'));
    await page.getByRole('tab', { name: l('رموز CLI', 'CLI tokens') }).click();
    await page.getByRole('button', { name: l('إنشاء رمز', 'Create token') }).click();
    const dialog = page.getByRole('dialog');
    await dialog.getByLabel(l('الاسم', 'Name')).fill('laptop');
    await dialog.getByRole('button', { name: l('إنشاء رمز', 'Create token') }).click();
    await expect(dialog.getByText(l('أدخل كلمة المرور الحالية.', 'Enter your current password.'))).toBeVisible();
    await dialog.getByLabel(l('كلمة المرور الحالية', 'Current password')).fill('wrong password');
    await dialog.getByRole('button', { name: l('إنشاء رمز', 'Create token') }).click();
    await expect(dialog.getByText(l('كلمة المرور الحالية غير صحيحة.', 'The current password is incorrect.'))).toBeVisible();
    await dialog.getByLabel(l('كلمة المرور الحالية', 'Current password')).fill('correct horse battery');
    await dialog.getByRole('button', { name: l('إنشاء رمز', 'Create token') }).click();
    expect(api.calls.at(-1)).toMatchObject({ body: { name: 'laptop', expireDays: 90, currentPassword: 'correct horse battery' } });
    await expect(page.getByRole('alertdialog')).toContainText('aruma_pat_Xw3zR8kQmVtN2pL6yHc4BfJ0sDgE7aUo');
    await page.getByRole('button', { name: l('تم', 'Done') }).click();
    await expect(page.getByText('aruma_pat_Xw3zR8kQmVtN2pL6yHc4BfJ0sDgE7aUo')).toHaveCount(0);
    await expect(page.getByRole('table')).toContainText('laptop');
  });

  test('changing the password warns that CLI tokens are revoked', async ({ page, l }) => {
    await open(page, '/account/', l('الحساب', 'Account'));
    await page.getByRole('tab', { name: l('الأمان', 'Security') }).click();
    await expect(page.getByText(l('ويلغي كل رموز CLI الخاصة بك', 'and revokes all your CLI tokens'))).toBeVisible();
    await page.getByLabel(l('كلمة المرور الحالية', 'Current password')).fill('correct horse battery');
    await page.getByLabel(l('كلمة المرور الجديدة', 'New password'), { exact: true }).fill('a new long password');
    await page.getByLabel(l('أعد كتابة كلمة المرور الجديدة', 'Repeat the new password')).fill('a new long password');
    await page.getByRole('button', { name: l('تغيير كلمة المرور', 'Change password') }).click();
    await expect(page.getByRole('status').filter({ hasText: l('أُلغيت رموز CLI', 'CLI tokens revoked') })).toBeVisible();
    await page.getByRole('tab', { name: l('رموز CLI', 'CLI tokens') }).click();
    await expect(page.getByText(l('لا رموز بعد.', 'No tokens yet.'))).toBeVisible();
  });

  test('switches the language without reloading and saves it on the account', async ({ page, api, l }) => {
    await open(page, '/project/?id=chat-app', 'Chat App');
    await page.evaluate(() => Object.assign(window, { notReloaded: true }));
    const other = l('en', 'ar');
    await page.getByRole('button', { name: l('التبديل إلى English', 'Switch to العربية') }).click();
    await expect(page.locator('html')).toHaveAttribute('lang', other);
    await expect(page.locator('html')).toHaveAttribute('dir', other === 'ar' ? 'rtl' : 'ltr');
    await expect(page.getByRole('heading', { name: other === 'ar' ? 'المعرّفات' : 'Identifiers' })).toBeVisible();
    expect(await page.evaluate(() => (window as unknown as { notReloaded?: boolean }).notReloaded)).toBe(true);
    await expect.poll(() => api.store.account.locale).toBe(other);
  });
});

test.describe('layout', () => {
  test('uses a drawer on narrow screens without sideways scrolling', async ({ page, l }) => {
    await page.setViewportSize({ width: 360, height: 760 });
    await open(page, '/project/keys/?id=chat-app', l('مفاتيح API', 'API keys'));
    expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBe(0);
    await page.getByRole('button', { name: l('فتح قائمة التنقّل', 'Open navigation') }).click();
    await page.getByRole('dialog').getByRole('link', { name: l('حسابات الخدمة', 'Service accounts') }).click();
    await expect(page.getByRole('heading', { level: 1, name: l('حسابات الخدمة', 'Service accounts') })).toBeVisible();
  });

  test('is usable from the keyboard with a visible focus ring', async ({ page, l }) => {
    await open(page, '/projects/', l('المشاريع', 'Projects'));
    await page.keyboard.press('Tab');
    const skip = page.getByRole('link', { name: l('تخطَّ إلى المحتوى', 'Skip to content') });
    await expect(skip).toBeFocused();
    await expect(skip).toBeInViewport();
    await page.keyboard.press('Enter');
    await page.keyboard.press('Tab');
    const focused = page.locator(':focus');
    await expect(focused).toHaveText(l('مشروع جديد', 'New project'));
    expect(await focused.evaluate((el) => getComputedStyle(el).outlineStyle)).toBe('solid');
    await page.keyboard.press('Enter');
    await expect(page.getByRole('dialog')).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(page.getByRole('dialog')).toBeHidden();
    await expect(focused).toBeFocused();
  });

  test('shows the disclaimer on every page', async ({ page, api, l }) => {
    const text = l('غير تابع لشركة Google ولا معتمد منها.', 'Not affiliated with or endorsed by Google.');
    await open(page, '/projects/', l('المشاريع', 'Projects'));
    await expect(page.getByRole('contentinfo')).toHaveText(text);
    api.store.signedIn = false;
    await page.goto('/login/');
    await expect(page.getByRole('contentinfo')).toHaveText(text);
  });
});
