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

/**
 * Types `text` into the field labelled `label`, and makes sure the app kept it.
 *
 * Flutter takes what is typed into a field only once it has attached its text input to the field,
 * which happens on a frame after the click that focuses it; on attaching, it writes the field's
 * own state (empty) into the element. Text typed before that is lost: the field shows empty, and
 * a Send that follows sends nothing. A click usually takes long enough, but not one that went
 * through at once after waiting for a snack bar to leave the field (CI saw "first" lost that
 * way). So the text is checked again after a few frames, and typed again if Flutter dropped it.
 */
export async function fill(page, label, text) {
  const field = page.locator('flt-semantics-host').getByRole('textbox', { name: label, exact: true }).first();
  await field.click();
  await expect(async () => {
    if ((await field.inputValue()) !== text) {
      await field.click();
      await field.fill(text);
    }
    await page.evaluate(() => new Promise((done) => {
      let frames = 3;
      const next = () => (--frames ? requestAnimationFrame(next) : setTimeout(done, 100));
      requestAnimationFrame(next);
    }));
    expect(await field.inputValue(), `the text of the field "${label}"`).toBe(text);
  }).toPass({ timeout: 15_000 });
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
