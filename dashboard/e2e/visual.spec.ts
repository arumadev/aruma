import { expect, open, test } from './fixtures';

// Full-page screenshots of every screen in both languages. Data comes from the mock, the
// clock is frozen and fonts are loaded first, so the pictures are deterministic. Baselines
// carry the platform in their name: macOS and Linux render text differently.

test.describe('signed out', () => {
  test.beforeEach(({ api }) => {
    api.store.signedIn = false;
  });

  test('login', async ({ page, snapshot, l }) => {
    await open(page, '/login/', l('تسجيل الدخول', 'Sign in'));
    await snapshot('login');
  });

  test('setup', async ({ page, api, snapshot, l }) => {
    api.store.status.setupRequired = true;
    await open(page, '/setup/', l('إعداد أروما', 'Set up Aruma'));
    await snapshot('setup');
  });
});

test('projects', async ({ page, snapshot, l }) => {
  await open(page, '/projects/', l('المشاريع', 'Projects'));
  await snapshot('projects');
});

test('new project dialog', async ({ page, snapshot, l }) => {
  await open(page, '/projects/', l('المشاريع', 'Projects'));
  await page.getByRole('button', { name: l('مشروع جديد', 'New project') }).click();
  await page.getByRole('dialog').getByLabel(l('اسم المشروع', 'Project name')).fill('Weather Station');
  await expect(page.getByRole('dialog').getByText(l('متاح', 'Available'))).toBeVisible();
  await snapshot('new-project', { modal: true });
});

test('project overview', async ({ page, snapshot }) => {
  await open(page, '/project/?id=chat-app', 'Chat App');
  await expect(page.getByRole('link', { name: /Chat Web/ })).toBeVisible();
  await snapshot('overview');
});

test('add app: identifier step', async ({ page, snapshot, l }) => {
  await open(page, '/project/apps/new/?id=chat-app', l('إضافة تطبيق', 'Add an app'));
  await page.getByText('iOS', { exact: true }).click();
  await page.getByRole('button', { name: l('التالي', 'Next') }).click();
  await page.getByLabel(l('معرّف الحزمة', 'Bundle ID')).fill('com.example.chat.watch');
  await snapshot('add-app-identifier');
});

test('add app: web config', async ({ page, snapshot, l }) => {
  await open(page, '/project/apps/new/?id=chat-app', l('إضافة تطبيق', 'Add an app'));
  await page.getByText(l('الويب', 'Web'), { exact: true }).click();
  await page.getByRole('button', { name: l('التالي', 'Next') }).click();
  await page.getByLabel(l('الاسم المستعار للتطبيق', 'App nickname')).fill('Chat Admin');
  await page.getByRole('button', { name: l('تسجيل التطبيق', 'Register app') }).click();
  await expect(page.getByText('const firebaseConfig')).toBeVisible();
  await page.getByRole('button', { name: l('إغلاق', 'Dismiss') }).click();
  await snapshot('add-app-web-done');
});

test('app details', async ({ page, snapshot }) => {
  await open(page, '/project/app/?id=chat-app&app=1:123456789012:android:6b7fd59bb891c192', 'Chat Android');
  await expect(page.getByRole('table')).toBeVisible();
  await snapshot('app-android');
});

test('API keys', async ({ page, snapshot, l }) => {
  await open(page, '/project/keys/?id=chat-app', l('مفاتيح API', 'API keys'));
  await expect(page.getByRole('table')).toBeVisible();
  await snapshot('keys');
});

test('service accounts', async ({ page, snapshot, l }) => {
  await open(page, '/project/service-accounts/?id=chat-app', l('حسابات الخدمة', 'Service accounts'));
  await expect(page.getByRole('table')).toBeVisible();
  await snapshot('service-accounts');
});

test('project settings and delete confirmation', async ({ page, snapshot, l }) => {
  await open(page, '/project/settings/?id=chat-app', l('إعدادات المشروع', 'Project settings'));
  await snapshot('settings');
  await page.getByRole('button', { name: l('حذف المشروع', 'Delete project') }).click();
  await snapshot('settings-delete', { modal: true });
});

test('deleted project', async ({ page, snapshot }) => {
  await open(page, '/project/?id=old-store', 'Old Store');
  await snapshot('deleted-project');
});

test('audit log', async ({ page, snapshot, l }) => {
  await open(page, '/project/audit/?id=chat-app', l('سجل التدقيق', 'Audit log'));
  await expect(page.getByRole('table')).toBeVisible();
  await snapshot('audit');
});

test('account security', async ({ page, snapshot, l }) => {
  await open(page, '/account/', l('الحساب', 'Account'));
  await page.getByRole('tab', { name: l('الأمان', 'Security') }).click();
  await expect(page.getByRole('table')).toBeVisible();
  await snapshot('account-security');
});

test('v1 limits', async ({ page, snapshot, l }) => {
  await open(page, '/limits/', l('حدود الإصدار 1', 'v1 limits'));
  await snapshot('limits');
});

test.describe('narrow screen', () => {
  test.use({ viewport: { width: 360, height: 760 } });

  test('overview and drawer', async ({ page, snapshot, l }) => {
    await open(page, '/project/?id=chat-app', 'Chat App');
    await expect(page.getByRole('link', { name: /Chat Web/ })).toBeVisible();
    await snapshot('mobile-overview');
    await page.getByRole('button', { name: l('فتح قائمة التنقّل', 'Open navigation') }).click();
    await expect(page.getByRole('dialog')).toBeVisible();
    await snapshot('mobile-drawer', { modal: true });
  });
});
