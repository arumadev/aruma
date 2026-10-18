import { test as base, expect } from '@playwright/test';
import { readFileSync } from 'node:fs';

const dir = process.env.ARUMA_E2E_DIR ?? new URL('../.generated/', import.meta.url).pathname;
export const arumaURL = process.env.ARUMA_URL;
export const config = JSON.parse(readFileSync(`${dir}/firebase-config.json`, 'utf8'));
export const expected = JSON.parse(readFileSync(`${dir}/expected.json`, 'utf8'));
export const serviceAccountPath = `${dir}/service-account.json`;

// Every Google host the SDKs could reach: APIs, static files, the auth handler domains,
// reCAPTCHA and analytics.
const google = /(^|\.)(googleapis\.com|gstatic\.com|google\.com|googleusercontent\.com|firebaseio\.com|firebaseapp\.com|web\.app|recaptcha\.net|google-analytics\.com|googletagmanager\.com|doubleclick\.net|withgoogle\.com)$/;
const local = new Set(['127.0.0.1', 'localhost']);

export const test = base.extend({
  firebaseMajor: ['13', { option: true }],

  // The network guard: requests to this machine (the Vite app and Aruma) go through; anything
  // else is aborted and recorded, and the test fails if there was any (AC-A1).
  network: [async ({ context }, use) => {
    const log = { aruma: [], blocked: [] };
    await context.route('**/*', (route) => {
      const url = new URL(route.request().url());
      if (local.has(url.hostname)) {
        if (url.origin === arumaURL) log.aruma.push(url.pathname);
        return route.continue();
      }
      log.blocked.push(url.href);
      return route.abort('blockedbyclient');
    });
    await use(log);
    expect(log.blocked.filter((u) => google.test(new URL(u).hostname)), 'requests to Google').toEqual([]);
    expect(log.blocked, 'requests leaving the machine').toEqual([]);
  }, { auto: true }],

  // The app, loaded with the firebase major version of the project.
  app: async ({ page, firebaseMajor }, use) => {
    await page.goto(`/?v=${firebaseMajor}`);
    await expect(page.locator('#status')).toHaveText(new RegExp(`^ready ${firebaseMajor}\\.`));
    await use(page);
  },
});

export { expect };

// call runs fn in the page with the app's auth, SDK and arg. A thrown Firebase error comes back
// as { code }, anything else as { value }.
export async function call(page, fn, arg) {
  return page.evaluate(async ([src, arg]) => {
    const { auth, sdk } = window.e2e;
    try {
      // eslint-disable-next-line no-new-func
      return { value: await new Function('auth', 'sdk', 'arg', `return (${src})(auth, sdk, arg)`)(auth, sdk, arg) };
    } catch (e) {
      return { code: e.code ?? String(e) };
    }
  }, [fn.toString(), arg]);
}

// value unwraps a call that must succeed.
export async function value(page, fn, arg) {
  const r = await call(page, fn, arg);
  expect(r.code, `unexpected error ${r.code}`).toBeUndefined();
  return r.value;
}

// rest calls Aruma the way a second device would, outside the browser.
export async function rest(path, body) {
  const res = await fetch(`${arumaURL}/identitytoolkit.googleapis.com/v1/${path}?key=${config.apiKey}`, {
    method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(body),
  });
  const json = await res.json();
  if (!res.ok) throw new Error(`${path}: ${res.status} ${JSON.stringify(json)}`);
  return json;
}

// mailboxCode reads the oobCode of the newest dev mailbox message to an address, as the user
// would follow the link (the console API, with the CLI token run.sh exports).
export async function mailboxCode(to) {
  const res = await fetch(`${arumaURL}/console/v1/projects/${expected.projectId}/auth/mailbox`, {
    headers: { authorization: `Bearer ${process.env.ARUMA_CLI_TOKEN}` },
  });
  const { messages } = await res.json();
  const message = messages.find((m) => m.to === to);
  if (!message) throw new Error(`no message to ${to} in the dev mailbox`);
  return new URL(message.link).searchParams.get('oobCode');
}
