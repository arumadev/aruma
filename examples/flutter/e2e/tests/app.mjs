// Driving the Flutter web app through its semantics tree (lib/main.dart turns it on): the fields
// are <input> elements with their label, the buttons and tabs carry their text or tooltip.
import { expect } from './fixtures.mjs';
import { guard } from './fixtures.mjs';

/** A new browser context (one user) on the app, with the network guard. */
export async function openApp(browser, network, { colorScheme = 'light' } = {}) {
  const context = await browser.newContext({ colorScheme });
  await guard(context, network);
  const page = await context.newPage();
  page.errors = [];
  page.on('pageerror', (e) => page.errors.push(String(e)));
  await page.goto('/');
  await page.locator('flt-semantics-host').waitFor({ state: 'attached', timeout: 60_000 });
  return { context, page };
}

/** The element whose text or label is `name` (a string matches exactly, a RegExp anywhere). */
export function node(page, name) {
  const host = page.locator('flt-semantics-host');
  return host.getByText(name).or(host.getByLabel(name)).first();
}

export async function see(page, name, timeout) {
  await expect(node(page, name)).toBeVisible(timeout ? { timeout } : undefined);
}

export async function gone(page, name, timeout) {
  await expect(node(page, name)).toHaveCount(0, timeout ? { timeout } : undefined);
}

export async function tap(page, name) {
  const host = page.locator('flt-semantics-host');
  const exact = typeof name === 'string';
  let target = host.getByRole('button', { name, exact });
  for (const role of ['tab', 'menuitem', 'menuitemcheckbox', 'menuitemradio', 'radio']) {
    target = target.or(host.getByRole(role, { name, exact }));
  }
  target = target.first();
  await target.click();
}

export async function fill(page, label, text) {
  const field = page.locator('flt-semantics-host').getByRole('textbox', { name: label, exact: true }).first();
  await field.click();
  await field.fill(text);
}

/** Clicks `name`, which opens the browser's file chooser, and gives it the file. */
export async function chooseFile(page, name, file) {
  const chooser = page.waitForEvent('filechooser');
  await tap(page, name);
  await (await chooser).setFiles(file);
}

/** The top edge of each text, to compare their order on the screen. */
export async function tops(page, texts) {
  const out = [];
  for (const t of texts) out.push((await node(page, t).boundingBox()).y);
  return out;
}

/** The "Online: …" line of the chat, as the user reads it (polled by expect.poll). */
export function online(page) {
  const host = page.locator('flt-semantics-host');
  const line = host.locator('[aria-label^="Online: "]').or(host.getByText(/^Online: /)).first();
  return () => line.evaluate((e) => e.getAttribute('aria-label') ?? e.textContent, null, { timeout: 5_000 }).catch(() => '');
}
