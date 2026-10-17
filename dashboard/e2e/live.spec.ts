import { expect, test } from '@playwright/test';

// Smoke test against a real `aruma serve`, enabled by ARUMA_API_URL (see playwright.config.ts).
// On a fresh server it completes first-boot setup with ARUMA_SETUP_CODE; otherwise it signs in
// with ARUMA_E2E_EMAIL and ARUMA_E2E_PASSWORD. It creates a project, registers apps, downloads
// their config files and a service account key, then deletes the project.

const email = process.env.ARUMA_E2E_EMAIL ?? 'owner@example.com';
const password = process.env.ARUMA_E2E_PASSWORD ?? 'correct horse battery';

test('a project from creation to deletion', async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem('aruma.locale', 'ar'));
  await page.goto('/');
  await page.waitForURL(/\/(setup|login|projects)\/$/);

  if (page.url().endsWith('/setup/')) {
    const code = process.env.ARUMA_SETUP_CODE;
    test.skip(!code, 'The server needs first-boot setup: set ARUMA_SETUP_CODE.');
    await page.getByLabel('رمز الإعداد').fill(code!);
    await page.getByLabel('البريد الإلكتروني').fill(email);
    await page.getByLabel('كلمة المرور').fill(password);
    await page.getByRole('button', { name: 'إنشاء الحساب' }).click();
  } else if (page.url().endsWith('/login/')) {
    await page.getByLabel('البريد الإلكتروني').fill(email);
    await page.getByLabel('كلمة المرور').fill(password);
    await page.getByRole('button', { name: 'تسجيل الدخول' }).click();
  }
  await expect(page).toHaveURL('/projects/');

  const projectId = `smoke-${Date.now().toString(36)}`;
  await page.getByRole('button', { name: 'مشروع جديد' }).first().click();
  const dialog = page.getByRole('dialog');
  await dialog.getByLabel('اسم المشروع').fill('Smoke Test');
  await dialog.getByLabel('معرّف المشروع').fill(projectId);
  await expect(dialog.getByText('متاح')).toBeVisible();
  await dialog.getByRole('button', { name: 'إنشاء المشروع' }).click();
  await expect(page).toHaveURL(`/project/?id=${projectId}`);

  // A web app: its snippet comes from the server.
  await page.getByRole('link', { name: 'إضافة تطبيق' }).click();
  await page.getByText('الويب', { exact: true }).click();
  await page.getByRole('button', { name: 'التالي' }).click();
  await page.getByRole('button', { name: 'تسجيل التطبيق' }).click();
  await expect(page.locator('pre').filter({ hasText: `projectId: "${projectId}"` })).toBeVisible();

  // An Android app: google-services.json downloads under its own name.
  await page.goto(`/project/apps/new/?id=${projectId}`);
  await page.getByText('Android', { exact: true }).click();
  await page.getByRole('button', { name: 'التالي' }).click();
  await page.getByLabel('اسم الحزمة').fill('com.example.smoke');
  await page.getByRole('button', { name: 'التالي' }).click();
  await page.getByRole('button', { name: 'تسجيل التطبيق' }).click();
  const config = page.waitForEvent('download');
  await page.getByRole('button', { name: 'تنزيل google-services.json' }).click();
  expect((await config).suggestedFilename()).toBe('google-services.json');

  // A service account key is a one-time download.
  await page.goto(`/project/service-accounts/?id=${projectId}`);
  await page.getByRole('button', { name: 'إنشاء حساب خدمة' }).first().click();
  await page.getByRole('dialog').getByLabel('معرّف الحساب').fill('smoke-server');
  await page.getByRole('dialog').getByRole('button', { name: 'إنشاء حساب خدمة' }).click();
  await page.getByRole('button', { name: 'إنشاء مفتاح' }).click();
  const key = page.waitForEvent('download');
  await page.getByRole('alertdialog').getByRole('button', { name: 'إنشاء وتنزيل' }).click();
  expect((await key).suggestedFilename()).toMatch(new RegExp(`^${projectId}-[0-9a-f]{12}\\.json$`));

  // The audit log saw it all; then the project is deleted with a typed confirmation.
  await page.goto(`/project/audit/?id=${projectId}`);
  await expect(page.getByRole('table')).toContainText('serviceaccount.key.create');
  await page.goto(`/project/settings/?id=${projectId}`);
  await page.getByRole('button', { name: 'حذف المشروع' }).click();
  await page.getByRole('alertdialog').getByLabel('معرّف المشروع').fill(projectId);
  await page.getByRole('alertdialog').getByRole('button', { name: 'حذف المشروع' }).click();
  await expect(page.getByText('هذا المشروع محذوف')).toBeVisible();
});
