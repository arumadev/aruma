import AxeBuilder from '@axe-core/playwright';
import { expect, test as base, type Page } from '@playwright/test';
import type { Locale } from '../src/lib/api/types';
import { MockApi, NOW } from './mock-api';

export interface Options {
  /** The interface language this project runs in. */
  appLocale: Locale;
}

interface Fixtures {
  api: MockApi;
  /**
   * Waits for fonts and settled rendering, then compares a screenshot: the full page, or only
   * the viewport for modal states, whose fixed overlay covers just the viewport.
   */
  snapshot: (name: string, options?: { modal?: boolean }) => Promise<void>;
  /** Fails on serious or critical axe-core accessibility violations. */
  checkA11y: () => Promise<void>;
  /** Picks the message for the project's language. */
  l: <T>(ar: T, en: T) => T;
}

export const test = base.extend<Options & Fixtures>({
  appLocale: ['ar', { option: true }],

  api: [
    async ({ page, appLocale }, use) => {
      const api = new MockApi(appLocale);
      await api.install(page);
      await page.clock.setFixedTime(new Date(NOW));
      // The language this device last used, as a returning visitor would have it.
      await page.addInitScript((locale) => localStorage.setItem('aruma.locale', locale), appLocale);
      await use(api);
    },
    { auto: true },
  ],

  snapshot: async ({ page }, use) => {
    await use(async (name, options) => {
      await page.evaluate(() => document.fonts.ready);
      await expect(page.getByRole('status', { name: /Loading|جارٍ التحميل/ })).toHaveCount(0);
      await expect(page).toHaveScreenshot(`${name}.png`, { fullPage: !options?.modal });
    });
  },

  checkA11y: async ({ page }, use) => {
    await use(async () => {
      const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa']).analyze();
      const serious = results.violations.filter((v) => v.impact === 'serious' || v.impact === 'critical');
      expect(serious.map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(' ')).join(', ')}`)).toEqual([]);
    });
  },

  l: async ({ appLocale }, use) => {
    await use(<T,>(ar: T, en: T) => (appLocale === 'ar' ? ar : en));
  },
});

export { expect };

/** Opens a page and waits until its main heading is shown. */
export async function open(page: Page, path: string, heading: string | RegExp): Promise<void> {
  await page.goto(path);
  await expect(page.getByRole('heading', { level: 1, name: heading })).toBeVisible();
}
