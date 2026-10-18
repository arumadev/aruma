import { expect, open, test } from './fixtures';

// Full-page screenshots of the Authentication pages in both languages (see visual.spec.ts).

const AMAL = '0bQ3pX7aKd2LmN8rT4vW6yZ1cE5g';

test('users', async ({ page, snapshot, l }) => {
  await open(page, '/project/auth/?id=chat-app', l('المصادقة', 'Authentication'));
  await expect(page.getByRole('table')).toBeVisible();
  await snapshot('auth-users');
});

test('add user dialog', async ({ page, snapshot, l }) => {
  await open(page, '/project/auth/?id=chat-app', l('المصادقة', 'Authentication'));
  await page.getByRole('button', { name: l('إضافة مستخدم', 'Add user') }).click();
  await page.getByRole('dialog').getByLabel(l('البريد الإلكتروني', 'Email')).fill('layla@example.com');
  await snapshot('auth-add-user', { modal: true });
});

test('user details', async ({ page, snapshot, l }) => {
  await open(page, `/project/auth/user/?id=chat-app&uid=${AMAL}`, l('المصادقة', 'Authentication'));
  await expect(page.getByRole('heading', { name: 'amal@example.com' })).toBeVisible();
  await snapshot('auth-user');
});

test('sign-in methods', async ({ page, snapshot, l }) => {
  await open(page, '/project/auth/providers/?id=chat-app', l('المصادقة', 'Authentication'));
  await expect(page.getByRole('heading', { name: 'Apple' })).toBeVisible();
  await snapshot('auth-methods');
});

test('templates', async ({ page, snapshot, l }) => {
  await open(page, '/project/auth/templates/?id=chat-app', l('المصادقة', 'Authentication'));
  await expect(page.getByRole('region', { name: l('معاينة', 'Preview') })).toBeVisible();
  await expect(page.getByLabel(l('المضيف', 'Host'))).toHaveValue('smtp.example.com');
  await snapshot('auth-templates');
});

test('mailbox', async ({ page, snapshot, l }) => {
  await open(page, '/project/auth/mailbox/?id=chat-app', l('المصادقة', 'Authentication'));
  await expect(page.getByRole('table')).toBeVisible();
  await snapshot('auth-mailbox');
});

test('settings and emergency rotation', async ({ page, snapshot, l }) => {
  await open(page, '/project/auth/settings/?id=chat-app', l('المصادقة', 'Authentication'));
  await expect(page.getByRole('table').first()).toBeVisible();
  await expect(page.getByText(l('طلبات العملاء', 'Client requests'))).toBeVisible();
  await snapshot('auth-settings');
  await page.getByRole('button', { name: l('تدوير طارئ', 'Emergency rotation') }).first().click();
  await expect(page.getByRole('alertdialog')).toBeVisible();
  await snapshot('auth-emergency', { modal: true });
});
