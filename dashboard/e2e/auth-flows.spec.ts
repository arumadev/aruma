import { expect, open, test } from './fixtures';

// Journeys through the Authentication pages, in both languages, against the mocked API.

const AMAL = '0bQ3pX7aKd2LmN8rT4vW6yZ1cE5g';

test.describe('users', () => {
  test('searches, pages, adds a user and opens it', async ({ page, api, l }) => {
    const store = api.auth.store('chat-app');
    for (let i = 0; i < 24; i++) {
      store.users.push({ ...store.users[2]!, uid: `zz${String(i).padStart(26, '0')}`, email: `team${i}@example.com` });
    }
    await open(page, '/project/auth/?id=chat-app', l('المصادقة', 'Authentication'));
    const rows = page.getByRole('table').getByRole('row');
    await expect(rows).toHaveCount(26);
    await expect(page.getByText(l('1–25 من 30', '1–25 of 30'))).toBeVisible();
    await page.getByRole('button', { name: l('التالي', 'Next') }).click();
    await expect(page.getByText(l('26–30 من 30', '26–30 of 30'))).toBeVisible();
    await page.getByRole('button', { name: l('السابق', 'Previous') }).click();
    await expect(page.getByText(l('1–25 من 30', '1–25 of 30'))).toBeVisible();

    await page.getByLabel(l('البحث عن مستخدمين', 'Search users')).fill('TEAM1');
    await expect(page.getByText(l('1–11 من 11', '1–11 of 11'))).toBeVisible();
    await page.getByLabel(l('البحث عن مستخدمين', 'Search users')).fill(AMAL);
    await expect(rows).toHaveCount(2);
    await page.getByLabel(l('البحث عن مستخدمين', 'Search users')).fill('');

    await page.getByRole('button', { name: l('إضافة مستخدم', 'Add user') }).click();
    const dialog = page.getByRole('dialog');
    await dialog.getByLabel(l('البريد الإلكتروني', 'Email')).fill('layla@example.com');
    await dialog.getByLabel(l('كلمة المرور', 'Password')).fill('short');
    await dialog.getByRole('button', { name: l('إضافة المستخدم', 'Add user') }).click();
    await expect(dialog.getByText(l('كلمة المرور أقصر مما تسمح به سياسة هذا المشروع.', "The password is shorter than this project's policy allows."))).toBeVisible();
    await dialog.getByLabel(l('كلمة المرور', 'Password')).fill('long enough password');
    await dialog.getByRole('button', { name: l('إضافة المستخدم', 'Add user') }).click();
    await expect(dialog).toBeHidden();
    await page.getByLabel(l('البحث عن مستخدمين', 'Search users')).fill('layla');
    await page.getByRole('link', { name: 'layla@example.com' }).click();
    await expect(page.getByRole('heading', { name: 'layla@example.com' })).toBeVisible();
    expect(api.calls.find((c) => c.method === 'POST' && c.path.endsWith('/auth/users'))?.csrf).toBe('csrf-token');
  });

  test('edits claims, disables, signs out everywhere and deletes', async ({ page, api, l }) => {
    const store = api.auth.store('chat-app');
    await open(page, `/project/auth/user/?id=chat-app&uid=${AMAL}`, l('المصادقة', 'Authentication'));

    const claims = page.getByLabel(l('المطالبات (JSON)', 'Claims (JSON)'));
    await claims.fill('{"role": "editor", "sub": "x"}');
    await expect(page.getByText(l('الاسم "sub" محجوز لرمز الهوية. اختر اسمًا آخر.', '"sub" is reserved for the ID token. Use another name.'))).toBeVisible();
    await claims.fill('{"role": "editor"}');
    await expect(page.getByText(l('17 / 1000 بايت', '17 / 1000 bytes'))).toBeVisible();
    await page.getByRole('button', { name: l('حفظ', 'Save') }).click();
    await expect(page.getByRole('status').getByText(l('حُفظت المطالبات المخصّصة', 'Custom claims saved'), { exact: false })).toBeVisible();
    expect(store.users.find((u) => u.uid === AMAL)?.customClaims).toBe('{"role":"editor"}');

    await page.getByRole('button', { name: l('تعطيل', 'Disable') }).click();
    await page.getByRole('alertdialog').getByRole('button', { name: l('تعطيل', 'Disable') }).click();
    await expect(page.getByText(l('معطّل', 'Disabled'), { exact: true })).toBeVisible();
    await page.getByRole('button', { name: l('تفعيل', 'Enable') }).click();
    await expect(page.getByText(l('مفعّل', 'Active'), { exact: true })).toBeVisible();

    await page.getByRole('button', { name: l('تسجيل الخروج من كل الأجهزة', 'Sign out everywhere') }).click();
    await page.getByRole('alertdialog').getByRole('button', { name: l('تسجيل الخروج من كل الأجهزة', 'Sign out everywhere') }).click();
    await expect(page.getByRole('alertdialog')).toBeHidden();
    expect(store.calls.some((c) => c.path.endsWith(':revokeSessions'))).toBe(true);

    await page.getByRole('button', { name: l('حذف المستخدم', 'Delete user') }).click();
    await page.getByRole('alertdialog').getByRole('button', { name: l('حذف المستخدم', 'Delete user') }).click();
    await expect(page).toHaveURL('/project/auth/?id=chat-app');
    await expect(page.getByRole('link', { name: 'amal@example.com' })).toHaveCount(0);
  });

  test('sends a password reset, or says the server cannot yet', async ({ page, api, l }) => {
    await open(page, `/project/auth/user/?id=chat-app&uid=${AMAL}`, l('المصادقة', 'Authentication'));
    await page.getByRole('button', { name: l('إرسال إعادة تعيين كلمة المرور', 'Send password reset') }).click();
    await expect(page.getByRole('status').getByText('amal@example.com', { exact: false })).toBeVisible();
    api.auth.store('chat-app').resetAvailable = false;
    await page.getByRole('button', { name: l('إرسال إعادة تعيين كلمة المرور', 'Send password reset') }).click();
    await expect(page.getByRole('main').getByRole('alert')).toHaveText(l('لا يستطيع هذا الخادم إرسال رسائل إعادة تعيين كلمة المرور بعد.', "This server can't send password reset emails yet."));
  });
});

test.describe('sign-in methods', () => {
  test('switches providers, edits domains and replaces a secret without showing it', async ({ page, api, l }) => {
    const store = api.auth.store('chat-app');
    await open(page, '/project/auth/providers/?id=chat-app', l('المصادقة', 'Authentication'));
    // Switches are pressed through their label, as people do; the input itself is visually hidden.
    await page.getByText(l('مجهول الهوية', 'Anonymous'), { exact: true }).click();
    await expect(page.getByRole('switch', { name: l('مجهول الهوية', 'Anonymous') })).not.toBeChecked();
    expect(store.config.anonymousEnabled).toBe(false);

    await page.getByLabel(l('إضافة نطاق', 'Add a domain')).fill('https://bad.example.com');
    await page.getByRole('button', { name: l('إضافة', 'Add'), exact: true }).click();
    await expect(page.getByText(l('دون https://', 'without https://'), { exact: false })).toBeVisible();
    await page.getByLabel(l('إضافة نطاق', 'Add a domain')).fill('App.Example.com');
    await page.getByRole('button', { name: l('إضافة', 'Add'), exact: true }).click();
    const domains = page.getByRole('list', { name: l('النطاقات المصرّح بها', 'Authorized domains') });
    await expect(domains.getByText('app.example.com')).toBeVisible();
    await page.getByRole('button', { name: l('إزالة localhost', 'Remove localhost') }).click();
    await expect(domains.getByText('localhost')).toHaveCount(0);
    expect(store.config.authorizedDomains).toEqual(['chat.example.com', 'app.example.com']);

    const google = page.locator('section').filter({ has: page.getByRole('heading', { name: 'Google' }) });
    const secret = google.getByLabel(l('سرّ العميل', 'Client secret'));
    await expect(secret).toHaveValue('');
    await secret.fill('GOCSPX-new-secret');
    await google.getByRole('button', { name: l('حفظ', 'Save') }).click();
    await expect(secret).toHaveValue('');
    const patch = store.calls.findLast((c) => c.path === 'providers/google.com');
    expect(patch?.body).toMatchObject({ clientSecret: 'GOCSPX-new-secret' });
    await expect(page.getByText('GOCSPX-new-secret')).toHaveCount(0);
  });

  test('copies the redirect URI and explains missing provider routes', async ({ page, api, l }) => {
    await open(page, '/project/auth/providers/?id=chat-app', l('المصادقة', 'Authentication'));
    await expect(page.getByText('https://console.example.com/__/auth/handler').first()).toBeVisible();
    api.auth.store('chat-app').providers = null;
    await page.reload();
    await expect(page.getByText(l('تسجيل الدخول عبر Google وApple غير متاح على هذا الخادم بعد.', "Google and Apple sign-in aren't available on this server yet."))).toBeVisible();
  });
});

test.describe('templates and mailbox', () => {
  test('edits a template with a live preview, then resets it', async ({ page, api, l }) => {
    await open(page, '/project/auth/templates/?id=chat-app', l('المصادقة', 'Authentication'));
    await page.getByRole('button', { name: l('القالب', 'Template') }).click();
    await page.getByRole('option', { name: l('إعادة تعيين كلمة المرور', 'Password reset') }).click();
    await page.getByRole('button', { name: l('اللغة', 'Language') }).click();
    await page.getByRole('option', { name: 'English' }).click();
    const subject = page.getByLabel(l('الموضوع', 'Subject'));
    await expect(subject).toHaveAttribute('dir', 'ltr');
    await subject.fill('Reset your %APP_NAME% password');
    const preview = page.getByRole('region', { name: l('معاينة', 'Preview') });
    await expect(preview.getByText('Reset your دردشة password')).toBeVisible();
    await page.getByRole('button', { name: l('حفظ', 'Save') }).first().click();
    await expect(page.getByText(l('مخصّص', 'Customized'))).toBeVisible();
    await page.getByRole('button', { name: l('استعادة الافتراضي', 'Reset to default') }).click();
    await page.getByRole('alertdialog').getByRole('button', { name: l('استعادة الافتراضي', 'Reset to default') }).click();
    await expect(subject).toHaveValue('Reset your password for %APP_NAME%');
    expect(api.auth.store('chat-app').templates?.find((t) => t.type === 'PASSWORD_RESET' && t.language === 'en')?.customized).toBe(false);
  });

  test('saves SMTP settings, keeps the password write-only and sends a test', async ({ page, api, l }) => {
    await open(page, '/project/auth/templates/?id=chat-app', l('المصادقة', 'Authentication'));
    const password = page.getByRole('textbox', { name: l('كلمة المرور', 'Password'), exact: true });
    await expect(password).toHaveValue('');
    await page.getByLabel(l('المنفذ', 'Port')).fill('465');
    await page.getByRole('button', { name: l('حفظ', 'Save') }).last().click();
    await expect(page.getByRole('status').getByText(l('حُفظت إعدادات SMTP.', 'SMTP settings saved.'))).toBeVisible();
    const patch = api.auth.store('chat-app').calls.findLast((c) => c.path === 'smtp');
    expect(patch?.body).toMatchObject({ port: 465 });
    expect(patch?.body).not.toHaveProperty('password');

    await page.getByRole('button', { name: l('إرسال رسالة تجريبية', 'Send test email') }).click();
    await page.getByRole('dialog').getByLabel(l('المستلم', 'Recipient')).fill('layla@example.com');
    await page.getByRole('dialog').getByRole('button', { name: l('إرسال', 'Send') }).click();
    await expect(page.getByRole('status').getByText('layla@example.com', { exact: false })).toBeVisible();
  });

  test('shows the mailbox in dev mode and hides it otherwise', async ({ page, api, l }) => {
    await open(page, '/project/auth/?id=chat-app', l('المصادقة', 'Authentication'));
    await page.getByRole('tab', { name: l('صندوق البريد', 'Mailbox') }).click();
    await expect(page).toHaveURL('/project/auth/mailbox/?id=chat-app');
    await expect(page.getByRole('link', { name: l('فتح', 'Open') }).first()).toHaveAttribute('href', /oobCode=Xk3PqL9vRt2/);
    await page.getByRole('button', { name: l('إفراغ الصندوق', 'Clear mailbox') }).click();
    await page.getByRole('alertdialog').getByRole('button', { name: l('إفراغ الصندوق', 'Clear mailbox') }).click();
    await expect(page.getByText(l('لا رسائل بعد', 'No email yet'))).toBeVisible();

    api.auth.store('chat-app').mailbox = null;
    await open(page, '/project/auth/?id=chat-app', l('المصادقة', 'Authentication'));
    await expect(page.getByRole('tab', { name: l('الإعدادات', 'Settings') })).toBeVisible();
    await expect(page.getByRole('tab', { name: l('صندوق البريد', 'Mailbox') })).toHaveCount(0);
  });
});

test('rotates signing keys, with a typed confirmation for an emergency', async ({ page, api, l }) => {
  const store = api.auth.store('chat-app');
  await open(page, '/project/auth/settings/?id=chat-app', l('المصادقة', 'Authentication'));
  await page.getByRole('button', { name: l('تدوير', 'Rotate'), exact: true }).first().click();
  await page.getByRole('alertdialog').getByRole('button', { name: l('تدوير', 'Rotate') }).click();
  await expect(page.getByRole('alertdialog')).toBeHidden();
  expect(store.keys.filter((k) => k.purpose === 'id_token' && k.state === 'retired')).toHaveLength(2);

  await page.getByRole('button', { name: l('تدوير طارئ', 'Emergency rotation') }).first().click();
  const dialog = page.getByRole('alertdialog');
  const confirm = dialog.getByRole('button', { name: l('تدوير طارئ', 'Emergency rotation') });
  await expect(confirm).toBeDisabled();
  await dialog.getByLabel(l('معرّف المشروع', 'Project ID')).fill('chat-ap');
  await expect(confirm).toBeDisabled();
  await dialog.getByLabel(l('معرّف المشروع', 'Project ID')).fill('chat-app');
  await confirm.click();
  await expect(dialog).toBeHidden();
  const rotate = store.calls.findLast((c) => c.path === 'signingKeys:rotate');
  expect(rotate?.body).toEqual({ purpose: 'id_token', emergency: true });
});
