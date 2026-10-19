import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { LocaleSwitch } from '@/components/shell/LocaleSwitch';
import { LoginScreen } from '@/features/session/LoginScreen';
import { json, renderApp, stubApi } from '@/test/render';

const account = (locale: 'ar' | 'en') => ({ id: 'a1', email: 'owner@example.com', displayName: '', locale, createTime: '2026-10-18T00:00:00Z' });
const routes = (locale: 'ar' | 'en') => ({
  'GET /console/v1/status': () => json(200, { setupRequired: false, signupOpen: false, version: 'test', limits: {} }),
  'POST /console/v1/auth:login': () => json(200, { account: account(locale), csrfToken: 'csrf-1', expireTime: '2026-10-18T08:00:00Z' }),
  'PATCH /console/v1/me': (init: RequestInit) => json(200, { account: { ...account(locale), ...JSON.parse(String(init.body)) } }),
});

afterEach(() => vi.unstubAllGlobals());

async function signIn() {
  await userEvent.type(screen.getByRole('textbox'), 'owner@example.com');
  await userEvent.type(document.querySelector('input[type="password"]')!, 'secret-password');
  await userEvent.click(screen.getAllByRole('button').at(-1)!);
}

describe('language after sign-in', () => {
  it("follows the account's language when none was chosen on this visit", async () => {
    const fetchMock = stubApi(routes('en'));
    renderApp(<LoginScreen />);
    await signIn();
    await waitFor(() => expect(document.documentElement).toHaveAttribute('dir', 'ltr'));
    expect(fetchMock.mock.calls.some(([, init]) => init?.method === 'PATCH')).toBe(false);
  });

  it('saves a language picked on the sign-in page to the account, with the CSRF token', async () => {
    const fetchMock = stubApi(routes('ar'));
    renderApp(
      <>
        <LocaleSwitch />
        <LoginScreen />
      </>,
    );
    await userEvent.click(screen.getByRole('button', { name: 'التبديل إلى English' }));
    await signIn();
    await waitFor(() => expect(fetchMock.mock.calls.some(([, init]) => init?.method === 'PATCH')).toBe(true));
    const [, patch] = fetchMock.mock.calls.find(([, init]) => init?.method === 'PATCH')!;
    expect(JSON.parse(String(patch!.body))).toEqual({ locale: 'en' });
    expect((patch!.headers as Record<string, string>)['X-Aruma-CSRF']).toBe('csrf-1');
    expect(document.documentElement).toHaveAttribute('dir', 'ltr');
  });
});
