// The README's screenshots, from the web build: a phone (412x915 at device scale 2), the five
// screens in English with the light theme and in Arabic with the dark one. run.sh --screenshots
// runs it after the scenario; the PNGs go to ARUMA_E2E_SHOTS (docs/).
import { join } from 'node:path';
import { test } from './fixtures.mjs';
import { chooseFile, fill, openApp, see, tap } from './app.mjs';

const dir = process.env.ARUMA_E2E_SHOTS;
test.skip(!dir, 'ARUMA_E2E_SHOTS is not set');

async function shot(page, name) {
  // Let animations, ripples and snack bars finish, and no tooltip stay open under the pointer.
  await page.mouse.move(1, 1);
  await page.waitForTimeout(4_500);
  await page.screenshot({ path: join(dir, `${name}.png`) });
}

test('screenshots', async ({ browser, network }) => {
  const run = Date.now().toString(36);

  // English, light.
  const a = await openApp(browser, network, { colorScheme: 'light' });
  await shot(a.page, 'en-light-1-sign-in');
  await tap(a.page, 'No account? Create one');
  await fill(a.page, 'Name', 'Ada');
  await fill(a.page, 'Email', `ada-shots-${run}@example.com`);
  await fill(a.page, 'Password', 'ada-secret-1');
  await tap(a.page, 'Create account');
  await see(a.page, 'No teams yet');
  await tap(a.page, 'Create team');
  await fill(a.page, 'Team name', 'Product launch');
  await tap(a.page, 'Create');
  await see(a.page, /Team created\. Its join code is/);
  const code = /join code is ([A-Z0-9]{6})/.exec(await a.page.locator('flt-semantics-host').getByText(/Team created/).textContent())[1];
  for (const t of ['Order the banners', 'Book the venue', 'Draft the announcement']) {
    await fill(a.page, 'New task', t);
    await tap(a.page, 'Add task');
    await see(a.page, new RegExp(t));
  }
  await a.page.locator('flt-semantics-host').getByRole('checkbox', { name: 'Done: Order the banners' }).click();
  await tap(a.page, 'More for Draft the announcement');
  await chooseFile(a.page, 'Attach a file', { name: 'announcement.pdf', mimeType: 'application/pdf', buffer: Buffer.from('%PDF-1.4\n') });
  await see(a.page, 'File attached');

  // Arabic, dark: the second member picks Arabic on the sign-in screen.
  const b = await openApp(browser, network, { colorScheme: 'dark' });
  await tap(b.page, 'Language');
  await tap(b.page, 'Arabic');
  await see(b.page, 'المتابعة كضيف');
  await shot(b.page, 'ar-dark-1-sign-in');
  await tap(b.page, 'ليس لديك حساب؟ أنشئ حسابًا');
  await fill(b.page, 'الاسم', 'سارة');
  await fill(b.page, 'البريد الإلكتروني', `sara-shots-${run}@example.com`);
  await fill(b.page, 'كلمة المرور', 'sara-secret-1');
  await tap(b.page, 'إنشاء الحساب');
  await see(b.page, 'لا توجد فرق بعد');
  await tap(b.page, 'الانضمام إلى فريق');
  await fill(b.page, 'رمز الانضمام', code);
  await tap(b.page, 'انضمام');
  await see(b.page, /Draft the announcement/);

  await tap(a.page, 'More for Book the venue');
  await tap(a.page, 'Assign');
  await tap(a.page, 'سارة');
  await see(a.page, /Assigned to/);

  await tap(a.page, 'Chat');
  await tap(b.page, 'الدردشة');
  for (const [p, label, text] of [
    [a.page, 'Message', 'The draft is attached to the task.'],
    [b.page, 'رسالة', 'شكرًا، سأراجعها اليوم.'],
    [a.page, 'Message', 'Great, the venue is yours to book.'],
  ]) {
    await fill(p, label, text);
    await tap(p, label === 'Message' ? 'Send' : 'إرسال');
    await see(a.page, text);
    await see(b.page, text);
  }

  await shot(a.page, 'en-light-4-chat');
  await tap(a.page, 'Tasks');
  await shot(a.page, 'en-light-3-tasks');
  await tap(a.page, 'Back');
  await tap(a.page, 'Create team');
  await fill(a.page, 'Team name', 'Design review');
  await tap(a.page, 'Create');
  await see(a.page, /Team created/);
  await tap(a.page, 'Back');
  await see(a.page, 'Design review');
  await shot(a.page, 'en-light-2-teams');
  await tap(a.page, 'Profile');
  await shot(a.page, 'en-light-5-profile');

  await shot(b.page, 'ar-dark-4-chat');
  await tap(b.page, 'المهام');
  await shot(b.page, 'ar-dark-3-tasks');
  await tap(b.page, 'رجوع');
  await see(b.page, 'فرقي');
  await shot(b.page, 'ar-dark-2-teams');
  await tap(b.page, 'الملف الشخصي');
  await shot(b.page, 'ar-dark-5-profile');

  await a.context.close();
  await b.context.close();
});
