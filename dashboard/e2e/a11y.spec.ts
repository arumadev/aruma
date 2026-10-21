import { open, test } from './fixtures';

// axe-core over the main screens in both languages: no serious or critical WCAG 2.2 A/AA
// violations.

const PAGES: [string, string, [string, string]][] = [
  ['projects', '/projects/', ['المشاريع', 'Projects']],
  ['overview', '/project/?id=chat-app', ['Chat App', 'Chat App']],
  ['add app', '/project/apps/new/?id=chat-app', ['إضافة تطبيق', 'Add an app']],
  ['app', '/project/app/?id=chat-app&app=1:123456789012:android:6b7fd59bb891c192', ['Chat Android', 'Chat Android']],
  ['keys', '/project/keys/?id=chat-app', ['مفاتيح API', 'API keys']],
  ['service accounts', '/project/service-accounts/?id=chat-app', ['حسابات الخدمة', 'Service accounts']],
  ['settings', '/project/settings/?id=chat-app', ['إعدادات المشروع', 'Project settings']],
  ['audit', '/project/audit/?id=chat-app', ['سجل التدقيق', 'Audit log']],
  ['auth users', '/project/auth/?id=chat-app', ['المصادقة', 'Authentication']],
  ['auth user', '/project/auth/user/?id=chat-app&uid=0bQ3pX7aKd2LmN8rT4vW6yZ1cE5g', ['المصادقة', 'Authentication']],
  ['auth sign-in methods', '/project/auth/providers/?id=chat-app', ['المصادقة', 'Authentication']],
  ['auth templates', '/project/auth/templates/?id=chat-app', ['المصادقة', 'Authentication']],
  ['auth mailbox', '/project/auth/mailbox/?id=chat-app', ['المصادقة', 'Authentication']],
  ['auth settings', '/project/auth/settings/?id=chat-app', ['المصادقة', 'Authentication']],
  ['database data', '/project/database/?id=chat-app', ['قاعدة البيانات اللحظية', 'Realtime Database']],
  ['database rules', '/project/database/rules/?id=chat-app', ['قاعدة البيانات اللحظية', 'Realtime Database']],
  ['account', '/account/', ['الحساب', 'Account']],
  ['limits', '/limits/', ['حدود الإصدار 1', 'v1 limits']],
];

for (const [name, path, [ar, en]] of PAGES) {
  test(`${name} has no serious accessibility violations`, async ({ page, checkA11y, l }) => {
    await open(page, path, l(ar, en));
    await page.evaluate(() => document.fonts.ready);
    await checkA11y();
  });
}

test('login has no serious accessibility violations', async ({ page, api, checkA11y, l }) => {
  api.store.signedIn = false;
  await open(page, '/login/', l('تسجيل الدخول', 'Sign in'));
  await checkA11y();
});

test('new project dialog has no serious accessibility violations', async ({ page, checkA11y, l }) => {
  await open(page, '/projects/', l('المشاريع', 'Projects'));
  await page.getByRole('button', { name: l('مشروع جديد', 'New project') }).click();
  await page.getByRole('dialog').waitFor();
  await checkA11y();
});
