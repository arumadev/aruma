import { test as base, expect, type Browser, type BrowserContext, type Page } from '@playwright/test';
import { readFileSync } from 'node:fs';

export const config = JSON.parse(readFileSync(process.env.FIREBASE_CONFIG!, 'utf8')) as {
  projectId: string;
  storageBucket: string;
  databaseURL: string;
};
/** Where Storage's API answers: Aruma's URL, or the Storage emulator. */
export const storageOrigin = process.env.E2E_STORAGE_ORIGIN!;
export const backend = process.env.E2E_BACKEND ?? 'aruma';
/** Aruma's off switch (genconfig -switch): POST /down closes every connection, POST /up listens again. */
export const switchURL = process.env.ARUMA_SWITCH_URL;

export async function server(state: 'down' | 'up') {
  const res = await fetch(`${switchURL}/${state}`, { method: 'POST' });
  expect(res.status, await res.text()).toBe(200);
}

const local = new Set(['127.0.0.1', 'localhost']);

export interface NetworkLog {
  blocked: string[];
  sockets: string[];
}

// guard is the network guard of one browser context: HTTP to this machine goes through, anything
// else is aborted and recorded (the test fails on it). WebSockets do not pass through route();
// each page records its own, and the browser resolves no name but localhost (playwright.config.ts).
export async function guard(context: BrowserContext, log: NetworkLog) {
  await context.route('**/*', (route) => {
    const url = new URL(route.request().url());
    if (local.has(url.hostname) || url.protocol === 'data:' || url.protocol === 'blob:') return route.continue();
    log.blocked.push(url.href);
    return route.abort('blockedbyclient');
  });
  const watch = (page: Page) =>
    page.on('websocket', (ws) => {
      const url = new URL(ws.url());
      log.sockets.push(url.href);
      if (!local.has(url.hostname)) log.blocked.push(url.href);
    });
  context.pages().forEach(watch);
  context.on('page', watch);
}

export interface Client {
  context: BrowserContext;
  page: Page;
  errors: string[];
}

export const test = base.extend<{ network: NetworkLog; open: (options?: { name?: string }) => Promise<Client> }>({
  network: [
    async ({}, use) => {
      const log: NetworkLog = { blocked: [], sockets: [] };
      await use(log);
      expect(log.blocked, 'requests that would have left this machine').toEqual([]);
    },
    { auto: true },
  ],

  // open starts one more user: a browser context of its own (its own Auth session, database
  // connection and storage) with the app loaded.
  open: async ({ browser, network }, use) => {
    const contexts: BrowserContext[] = [];
    await use(async () => {
      const context = await newContext(browser);
      contexts.push(context);
      await guard(context, network);
      const page = await context.newPage();
      const errors: string[] = [];
      page.on('pageerror', (e) => errors.push(String(e)));
      await page.goto('/');
      return { context, page, errors };
    });
    for (const c of contexts) await c.close().catch(() => {});
  },
});

async function newContext(browser: Browser) {
  // English, whatever the machine's locale, so that the test reads the English strings.
  return browser.newContext({ locale: 'en-US' });
}

export { expect };

/** The signed-in user's ID token, read from the SDK's own storage in the page (IndexedDB). */
export async function idToken(page: Page): Promise<string> {
  return page.evaluate(
    () =>
      new Promise<string>((resolve, reject) => {
        const open = indexedDB.open('firebaseLocalStorageDb');
        open.onerror = () => reject(open.error);
        open.onsuccess = () => {
          const tx = open.result.transaction('firebaseLocalStorage', 'readonly');
          const all = tx.objectStore('firebaseLocalStorage').getAll();
          all.onsuccess = () => {
            const entry = all.result.find((r: { fbase_key: string }) => r.fbase_key.startsWith('firebase:authUser:'));
            if (!entry) reject(new Error('no signed-in user in IndexedDB'));
            else resolve(entry.value.stsTokenManager.accessToken);
          };
        };
      }),
  );
}
