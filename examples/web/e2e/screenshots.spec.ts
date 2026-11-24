import { deflateSync } from 'node:zlib';
import { readFileSync } from 'node:fs';
import { devices, type Browser, type Page } from '@playwright/test';
import { expect, guard, test, type NetworkLog } from './fixtures';

// The README's screenshots, taken from the real app on a real backend: sign-in, teams, tasks, chat
// and profile, in English (light, desktop 1280x800) and in Arabic (dark, a Pixel 7), at 2x.
// Run with `e2e/run.sh --screenshots`; they are written to docs/.

type Sample = {
  main: string;
  other: string;
  third: string;
  team: string;
  secondTeam: string;
  tasks: string[];
  done: number[];
  otherTask: string;
  attachTo: number;
  attachment: string;
  chat: [string, string][];
};
const data = JSON.parse(readFileSync(new URL('./screenshot-data.json', import.meta.url), 'utf8')) as Record<'en' | 'ar', Sample>;
const password = 'screenshots-password';
const run = Date.now().toString(36);

const variants = [
  { lang: 'en' as const, scheme: 'light' as const, device: { viewport: { width: 1280, height: 800 } } },
  { lang: 'ar' as const, scheme: 'dark' as const, device: devices['Pixel 7'] },
];

// A 160x160 PNG with a soft two-colour gradient, for the profile picture.
function avatarPNG(): Buffer {
  const size = 160;
  const raw = Buffer.alloc((size * 3 + 1) * size);
  for (let y = 0; y < size; y++) {
    raw[y * (size * 3 + 1)] = 0;
    for (let x = 0; x < size; x++) {
      const t = (x + y) / (2 * size);
      const o = y * (size * 3 + 1) + 1 + x * 3;
      raw[o] = Math.round(79 + (236 - 79) * t);
      raw[o + 1] = Math.round(70 + (72 - 70) * t);
      raw[o + 2] = Math.round(229 + (153 - 229) * t);
    }
  }
  const crcTable = Array.from({ length: 256 }, (_, n) => {
    let c = n;
    for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
    return c >>> 0;
  });
  const crc = (b: Buffer) => {
    let c = 0xffffffff;
    for (const v of b) c = crcTable[(c ^ v) & 0xff]! ^ (c >>> 8);
    return (c ^ 0xffffffff) >>> 0;
  };
  const chunk = (type: string, body: Buffer) => {
    const len = Buffer.alloc(4);
    len.writeUInt32BE(body.length);
    const tb = Buffer.concat([Buffer.from(type), body]);
    const c = Buffer.alloc(4);
    c.writeUInt32BE(crc(tb));
    return Buffer.concat([len, tb, c]);
  };
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(size, 0);
  ihdr.writeUInt32BE(size, 4);
  ihdr.set([8, 2, 0, 0, 0], 8);
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk('IHDR', ihdr),
    chunk('IDAT', deflateSync(raw)),
    chunk('IEND', Buffer.alloc(0)),
  ]);
}

async function person(browser: Browser, network: NetworkLog, options: Parameters<Browser['newContext']>[0], lang: string) {
  const context = await browser.newContext({ locale: lang === 'ar' ? 'ar' : 'en-US', ...options });
  await guard(context, network);
  await context.addInitScript((l) => {
    try {
      if (!localStorage.getItem('team-board.lang')) localStorage.setItem('team-board.lang', l);
    } catch {
      // No storage: the browser's language decides.
    }
  }, lang);
  const page = await context.newPage();
  await page.goto('/');
  return { context, page };
}

async function signUp(page: Page, name: string, email: string) {
  await page.locator('.auth > button.link').click();
  await page.locator('input[name=name]').fill(name);
  await page.locator('input[name=email]').fill(email);
  await page.locator('input[name=password]').fill(password);
  await page.locator('form button[type=submit]').click();
  await expect(page.getByTestId('nav-profile')).toBeVisible();
}

async function shoot(page: Page, file: string) {
  await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
  await page.waitForTimeout(400); // fonts and the last snapshot
  await page.screenshot({ path: `docs/${file}.png` });
  // The layout holds down to 360 px wide: no sideways scrolling on any screen.
  const size = page.viewportSize()!;
  await page.setViewportSize({ width: 360, height: size.height });
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  expect(overflow, `${file} at 360 px scrolls sideways`).toBeLessThanOrEqual(0);
  await page.setViewportSize(size);
}

for (const v of variants) {
  test(`screenshots: ${v.lang}`, async ({ browser, network }) => {
    const d = data[v.lang];
    const main = await person(browser, network, { ...v.device, deviceScaleFactor: 2, colorScheme: v.scheme }, v.lang);
    const other = await person(browser, network, { viewport: { width: 1280, height: 800 } }, v.lang);
    const third = await person(browser, network, { viewport: { width: 1280, height: 800 } }, v.lang);
    const mp = main.page;

    await expect(mp.locator('.auth')).toBeVisible();
    await shoot(mp, `${v.lang}-sign-in`);

    await signUp(mp, d.main, `lina-${v.lang}-${run}@example.com`);
    await signUp(other.page, d.other, `omar-${v.lang}-${run}@example.com`);
    await signUp(third.page, d.third, `sara-${v.lang}-${run}@example.com`);

    // A second team first, so that the list has two.
    for (const name of [d.secondTeam, d.team]) {
      await mp.goto('/#/teams');
      await mp.locator('input[name=teamName]').fill(name);
      await mp.locator('input[name=teamName]').press('Enter');
      await expect(mp.locator('.team-head h1')).toHaveText(name);
    }
    const code = (await mp.getByTestId('team-code').textContent()) ?? '';
    for (const p of [other.page, third.page]) {
      await p.locator('input[name=code]').fill(code);
      await p.locator('input[name=code]').press('Enter');
      await expect(p.locator('.tabs')).toBeVisible();
    }

    for (const title of [...d.tasks].reverse()) {
      await mp.locator('input[name=title]').fill(title);
      await mp.locator('input[name=title]').press('Enter');
      await expect(mp.getByTestId('task').filter({ hasText: title })).toBeVisible();
    }
    await other.page.locator('input[name=title]').fill(d.otherTask);
    await other.page.locator('input[name=title]').press('Enter');
    await expect(mp.getByTestId('task')).toHaveCount(d.tasks.length + 1);
    for (const i of d.done) await mp.getByTestId('task').filter({ hasText: d.tasks[i] }).locator('input[type=checkbox]').check();
    await other.page
      .getByTestId('task')
      .filter({ hasText: d.tasks[d.attachTo] })
      .locator('input[type=file]')
      .setInputFiles({ name: d.attachment, mimeType: 'application/pdf', buffer: Buffer.from('%PDF-1.4\n%%EOF\n') });
    await expect(mp.getByTestId('attachment-link')).toBeVisible();
    // Assignees: Omar takes the release notes.
    await mp.getByTestId('task').filter({ hasText: d.tasks[0] }).locator('select').selectOption({ label: d.other });

    await mp.getByTestId('nav-profile').click();
    await mp.locator('input[type=file]').setInputFiles({ name: 'avatar.png', mimeType: 'image/png', buffer: avatarPNG() });
    await expect(mp.locator('.profile-head img.avatar')).toBeVisible({ timeout: 30_000 });
    await shoot(mp, `${v.lang}-profile`);

    await mp.goto('/#/teams');
    await expect(mp.getByTestId('team-list').locator('li')).toHaveCount(2);
    await shoot(mp, `${v.lang}-teams`);

    await mp.goto(`/#/teams/${code}/tasks`);
    await expect(mp.getByTestId('attachment-link')).toBeVisible();
    await shoot(mp, `${v.lang}-tasks`);

    await mp.goto(`/#/teams/${code}/chat`);
    await other.page.goto(`/#/teams/${code}/chat`);
    for (const [who, text] of d.chat) {
      const p = who === 'main' ? mp : other.page;
      await p.locator('input[name=message]').fill(text);
      await p.locator('input[name=message]').press('Enter');
      await expect(mp.getByTestId('message').last()).toContainText(text);
    }
    await expect(mp.getByTestId('online').locator('li')).toHaveCount(3);
    await shoot(mp, `${v.lang}-chat`);

    for (const c of [main, other, third]) await c.context.close();
  });
}
