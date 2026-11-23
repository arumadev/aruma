import { config, expect, idToken, server, storageOrigin, switchURL, test, type Client } from './fixtures';
import type { Page } from '@playwright/test';

// Two (then four) users in browser contexts of their own drive the built app against a real
// backend: sign-up, a team, joining by code, live tasks, an attachment, chat, presence, a guest,
// a rules denial, Arabic, and a reload. Every request stays on this machine (fixtures.ts).

const run = Date.now().toString(36);
const password = 'correct-horse-battery';
const user = (name: string) => ({ name, email: `${name.toLowerCase()}-${run}@example.com` });

async function signUp(page: Page, who: { name: string; email: string }, pass = password) {
  await page.getByRole('button', { name: 'New here? Create an account' }).click();
  await page.getByLabel('Your name').fill(who.name);
  await page.getByLabel('Email').fill(who.email);
  await page.getByLabel('Password').fill(pass);
  await page.getByRole('button', { name: 'Create account', exact: true }).click();
}

async function signIn(page: Page, email: string, pass: string) {
  await page.getByLabel('Email').fill(email);
  await page.getByLabel('Password').fill(pass);
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
}

async function joinByCode(page: Page, code: string) {
  await page.getByLabel('Join code').fill(code.toLowerCase()); // codes are case-insensitive
  await page.getByRole('button', { name: 'Join', exact: true }).click();
  await expect(page).toHaveURL(new RegExp(`#/teams/${code}/tasks$`));
}

const tasks = (page: Page) => page.getByTestId('task');
const task = (page: Page, title: string) => tasks(page).filter({ hasText: title });
const online = (page: Page) => page.getByTestId('online').locator('li');
const messages = (page: Page) => page.getByTestId('message').locator('.message-text');

test('two users share a team: tasks, an attachment, chat and presence, live', async ({ open }) => {
  const alice = user('Alice');
  const bob = user('Bob');

  let a: Client, b: Client, g: Client;
  let code = '';

  await test.step('sign-up refuses a weak password with a readable message, then succeeds', async () => {
    a = await open();
    await signUp(a.page, alice, '123');
    await expect(a.page.getByRole('alert')).toHaveText('This password is too weak. Use at least 6 characters.');
    await a.page.getByLabel('Password').fill(password);
    await a.page.getByRole('button', { name: 'Create account', exact: true }).click();
    await expect(a.page.getByRole('heading', { name: 'My teams' })).toBeVisible();
    await expect(a.page.getByText('You are not in a team yet.')).toBeVisible();
  });

  await test.step('Alice creates a team', async () => {
    await a.page.getByLabel('Team name').fill('Launch crew');
    await a.page.getByRole('button', { name: 'Create', exact: true }).click();
    await expect(a.page.getByRole('heading', { name: 'Launch crew' })).toBeVisible();
    code = (await a.page.getByTestId('team-code').textContent()) ?? '';
    expect(code).toMatch(/^[A-Z2-9]{6}$/);
    await expect(a.page.getByText('No tasks yet.')).toBeVisible();
  });

  await test.step('a wrong password is refused with a readable message; Bob signs up and joins by code', async () => {
    b = await open();
    await signIn(b.page, bob.email, 'not-the-password');
    await expect(b.page.getByRole('alert')).toHaveText('The email or password is wrong.');
    await signUp(b.page, bob);
    await expect(b.page.getByRole('heading', { name: 'My teams' })).toBeVisible();
    await joinByCode(b.page, code);
    await expect(b.page.getByRole('heading', { name: 'Launch crew' })).toBeVisible();
    // Alice's team list shows the team too (a collection group query on members).
    await a.page.getByRole('link', { name: 'Teams', exact: true }).click();
    await expect(a.page.getByTestId('team-list')).toContainText('Launch crew');
    await a.page.getByTestId('team-list').getByRole('link').first().click();
  });

  await test.step('tasks added, toggled and deleted are seen live by the other user', async () => {
    await a.page.getByPlaceholder('New task').fill('Write the README');
    await a.page.getByRole('button', { name: 'Add', exact: true }).click();
    await a.page.getByPlaceholder('New task').fill('Order pizza');
    await a.page.getByRole('button', { name: 'Add', exact: true }).click();
    await expect(tasks(b.page)).toHaveCount(2);
    await expect(task(b.page, 'Write the README')).toContainText('Added by Alice');

    await b.page.getByRole('checkbox', { name: 'Mark “Write the README” as done' }).check();
    await expect(a.page.getByRole('checkbox', { name: 'Mark “Write the README” as done' })).toBeChecked();
    await expect(a.page.getByTestId('task-summary')).toHaveText('1 open, 1 done');
    // Open tasks first: the done one moves to the end.
    await expect(tasks(a.page).last()).toContainText('Write the README');

    // Bob may not delete Alice's task (only its author or the owner): no button for him.
    await expect(task(b.page, 'Order pizza').getByRole('button', { name: 'Delete' })).toHaveCount(0);
    await task(a.page, 'Order pizza').getByRole('button', { name: 'Delete “Order pizza”' }).click();
    await expect(tasks(b.page)).toHaveCount(1);
  });

  let attachmentPath = '';
  await test.step('Bob attaches a file; Alice downloads it', async () => {
    const body = `Release checklist ${run}\n`;
    await task(b.page, 'Write the README').locator('input[type=file]').setInputFiles({
      name: 'checklist.txt',
      mimeType: 'text/plain',
      buffer: Buffer.from(body),
    });
    const link = task(a.page, 'Write the README').getByTestId('attachment-link');
    await expect(link).toHaveText(/checklist\.txt/);
    const href = (await link.getAttribute('href')) ?? '';
    attachmentPath = decodeURIComponent(new URL(href).pathname.split('/o/')[1] ?? '');
    expect(attachmentPath).toMatch(new RegExp(`^teams/${code}/tasks/[^/]+/checklist\\.txt$`));
    const [tab] = await Promise.all([a.page.waitForEvent('popup'), link.click()]);
    await expect(tab.locator('body')).toHaveText(body.trim());
    await tab.close();
  });

  await test.step('a 3 MB upload shows its progress', async () => {
    // Slow the upload's chunks down a little, so that its progress stays on screen long enough.
    await a.page.route(/\/v0\/b\/.*upload_id=/, async (route) => {
      await new Promise((r) => setTimeout(r, 400));
      await route.fallback();
    });
    await task(a.page, 'Write the README').locator('input[type=file]').setInputFiles({
      name: 'big.bin',
      mimeType: 'application/octet-stream',
      buffer: Buffer.alloc(3 * 1024 * 1024, 7),
    });
    await expect(task(a.page, 'Write the README').getByRole('progressbar')).toBeVisible();
    await expect(task(b.page, 'Write the README').getByTestId('attachment-link')).toHaveText(/big\.bin/, { timeout: 60_000 });
    await expect(task(a.page, 'Write the README').getByRole('progressbar')).toHaveCount(0);
    attachmentPath = attachmentPath.replace(/checklist\.txt$/, 'big.bin');
  });

  await test.step('a non-member is refused: the team page says so, and Storage refuses the file', async () => {
    const c = await open();
    await signUp(c.page, user('Carol'));
    await expect(c.page.getByRole('heading', { name: 'My teams' })).toBeVisible();
    await c.page.goto(`/#/teams/${code}/tasks`);
    const notice = c.page.getByTestId('not-member');
    await expect(notice).toContainText('Permission denied: the security rules do not allow you to do this.');
    await expect(notice).toContainText('You are not a member of this team');
    await expect(c.page.getByTestId('task')).toHaveCount(0);

    // The attachment's metadata (what getDownloadURL reads), with each user's own ID token.
    const object = `${storageOrigin}/v0/b/${config.storageBucket}/o/${encodeURIComponent(attachmentPath)}`;
    const as = async (who: Client) =>
      (await who.page.request.get(object, { headers: { Authorization: `Firebase ${await idToken(who.page)}` } })).status();
    expect(await as(c), 'a non-member reads the attachment').toBe(403);
    expect(await as(b), 'a member reads the attachment').toBe(200);
    await c.context.close();
  });

  await test.step('chat: messages arrive in order, and both users are online', async () => {
    await a.page.getByRole('link', { name: 'Chat', exact: true }).click();
    await b.page.getByRole('link', { name: 'Chat', exact: true }).click();
    await expect(a.page.getByText('No messages yet.')).toBeVisible();
    await expect(online(a.page)).toHaveCount(2);
    await expect(online(a.page).filter({ hasText: 'Bob' })).toBeVisible();
    await expect(online(b.page).filter({ hasText: 'Alice' })).toBeVisible();

    const send = async (c: Client, text: string) => {
      await c.page.getByPlaceholder('Write a message').fill(text);
      await c.page.getByRole('button', { name: 'Send' }).click();
    };
    await send(a, 'Morning! Who takes the README?');
    await expect(messages(b.page)).toHaveCount(1);
    await send(b, 'Me, it is done already.');
    await expect(messages(a.page)).toHaveCount(2);
    await send(a, 'Great, thanks.');
    const expected = ['Morning! Who takes the README?', 'Me, it is done already.', 'Great, thanks.'];
    await expect(messages(a.page)).toHaveText(expected);
    await expect(messages(b.page)).toHaveText(expected);
  });

  await test.step("Bob's presence disappears when his browser closes", async () => {
    await b.context.close();
    await expect(online(a.page).filter({ hasText: 'Bob' })).toHaveCount(0, { timeout: 30_000 });
    await expect(online(a.page)).toHaveCount(1);
  });

  await test.step('a guest joins the team, chats and shows as online', async () => {
    g = await open();
    await g.page.getByRole('button', { name: 'Continue as guest' }).click();
    await expect(g.page.getByRole('heading', { name: 'My teams' })).toBeVisible();
    await joinByCode(g.page, code);
    await g.page.getByRole('link', { name: 'Chat', exact: true }).click();
    await expect(messages(g.page)).toHaveCount(3);
    await g.page.getByPlaceholder('Write a message').fill('Hello from a guest');
    await g.page.getByRole('button', { name: 'Send' }).click();
    await expect(messages(a.page).last()).toHaveText('Hello from a guest');
    await expect(online(a.page).filter({ hasText: /Guest [A-Z0-9]{4}/ })).toBeVisible();
    await g.page.getByTestId('nav-profile').click();
    await expect(g.page.getByText('You are using a guest account.')).toBeVisible();
    await g.page.goBack();
    await expect(messages(g.page)).toHaveCount(4);
  });

  // Aruma only: the emulators have no switch to take them down and up again.
  if (switchURL) {
    await test.step('offline: a banner while the server is down; a message sent meanwhile arrives', async () => {
      await server('down');
      await expect(a.page.getByTestId('offline-banner')).toBeVisible({ timeout: 30_000 });
      await a.page.getByPlaceholder('Write a message').fill('Sent while offline');
      await a.page.getByRole('button', { name: 'Send' }).click();
      await expect(messages(a.page).last()).toHaveText('Sent while offline');
      await server('up');
      await expect(a.page.getByTestId('offline-banner')).toHaveCount(0, { timeout: 60_000 });
      await expect(messages(g.page).last()).toHaveText('Sent while offline', { timeout: 30_000 });
      // Back online, Alice is marked online again (presence is written on each connection).
      await expect(online(g.page).filter({ hasText: 'Alice' })).toBeVisible({ timeout: 30_000 });
    });
  }

  await test.step('Arabic: right to left, remembered; a reload keeps the session', async () => {
    await a.page.getByTestId('language-switch').click();
    await expect(a.page.locator('html')).toHaveAttribute('dir', 'rtl');
    await expect(a.page.locator('html')).toHaveAttribute('lang', 'ar');
    await expect(a.page.getByRole('link', { name: 'المحادثة' })).toHaveAttribute('aria-current', 'page');
    await a.page.reload();
    await expect(a.page.locator('html')).toHaveAttribute('dir', 'rtl');
    await expect(messages(a.page)).toHaveCount(switchURL ? 5 : 4);
    await expect(a.page.getByRole('heading', { name: 'Launch crew' })).toBeVisible();
    await a.page.getByTestId('language-switch').click();
    await expect(a.page.locator('html')).toHaveAttribute('dir', 'ltr');
  });

  await test.step('sign out, then sign in again', async () => {
    await a.page.getByTestId('nav-profile').click();
    await a.page.getByRole('button', { name: 'Sign out' }).click();
    await expect(a.page.getByRole('heading', { name: 'Sign in' })).toBeVisible();
    await signIn(a.page, alice.email, password);
    await expect(a.page.getByTestId('team-list')).toContainText('Launch crew');
  });

  for (const c of [a!, b!, g!]) expect(c.errors, 'uncaught errors in the page').toEqual([]);
});
