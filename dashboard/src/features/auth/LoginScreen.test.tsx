import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { json, renderApp, router, stubApi } from '@/test/render';
import { LoginScreen } from './LoginScreen';

const status = (signupOpen = false) => () => json(200, { setupRequired: false, signupOpen, version: 'test', limits: {} });
const account = { id: 'a1', email: 'owner@example.com', displayName: 'Owner', locale: 'ar', createTime: '2026-10-18T00:00:00Z' };

afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

async function signIn(email = 'owner@example.com', password = 'secret-password') {
  await userEvent.type(screen.getByLabelText('البريد الإلكتروني'), email);
  await userEvent.type(screen.getByLabelText('كلمة المرور'), password);
  await userEvent.click(screen.getByRole('button', { name: 'تسجيل الدخول' }));
}

describe('LoginScreen', () => {
  it('signs in and goes to the requested page', async () => {
    const fetchMock = stubApi({
      'GET /console/v1/status': status(),
      'POST /console/v1/auth:login': () => json(200, { account, csrfToken: 'csrf', expireTime: '2026-10-18T08:00:00Z' }),
    });
    renderApp(<LoginScreen />, { search: 'next=%2Fproject%2Fkeys%2F%3Fid%3Dchat-app' });
    await signIn();
    await waitFor(() => expect(router.replace).toHaveBeenCalledWith('/project/keys/?id=chat-app'));
    const login = fetchMock.mock.calls.find(([url]) => String(url).endsWith('/auth:login'))!;
    expect(JSON.parse(String(login[1]!.body))).toEqual({ email: 'owner@example.com', password: 'secret-password' });
  });

  it('never redirects off-site after sign-in', async () => {
    stubApi({
      'GET /console/v1/status': status(),
      'POST /console/v1/auth:login': () => json(200, { account, csrfToken: 'csrf', expireTime: '2026-10-18T08:00:00Z' }),
    });
    renderApp(<LoginScreen />, { search: 'next=%2F%2Fevil.example' });
    await signIn();
    await waitFor(() => expect(router.replace).toHaveBeenCalledWith('/projects/'));
  });

  it.each([
    [401, 'UNAUTHENTICATED'],
    [400, 'INVALID_ARGUMENT'],
    [403, 'PERMISSION_DENIED'],
  ])('shows one generic message for a %i', async (code, rpcStatus) => {
    stubApi({
      'GET /console/v1/status': status(),
      'POST /console/v1/auth:login': () => json(code, { error: { code, status: rpcStatus, message: 'Account disabled.' } }),
    });
    renderApp(<LoginScreen />);
    await signIn();
    expect(await screen.findByRole('alert')).toHaveTextContent('البريد الإلكتروني أو كلمة المرور غير صحيحة.');
    expect(screen.queryByText('Account disabled.')).not.toBeInTheDocument();
  });

  it('shows when to retry after too many attempts and blocks the button meanwhile', async () => {
    stubApi({
      'GET /console/v1/status': status(),
      'POST /console/v1/auth:login': () =>
        json(429, { error: { code: 429, status: 'RESOURCE_EXHAUSTED', message: 'Too many attempts.' } }, { 'Retry-After': '12' }),
    });
    renderApp(<LoginScreen />);
    await signIn();
    expect(await screen.findByRole('alert')).toHaveTextContent('محاولات كثيرة. أعد المحاولة بعد 12 ثانية.');
    expect(screen.getByRole('button', { name: 'تسجيل الدخول' })).toBeDisabled();
  });

  it('asks for missing fields without calling the server', async () => {
    const fetchMock = stubApi({ 'GET /console/v1/status': status() });
    renderApp(<LoginScreen />);
    await userEvent.click(screen.getByRole('button', { name: 'تسجيل الدخول' }));
    expect(screen.getByText('أدخل بريدك الإلكتروني.')).toBeInTheDocument();
    expect(screen.getByText('أدخل كلمة المرور.')).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([url]) => String(url).endsWith('/auth:login'))).toBe(false);
  });

  it('offers sign-up only when the server allows it', async () => {
    stubApi({ 'GET /console/v1/status': status(true) });
    renderApp(<LoginScreen />);
    expect(await screen.findByRole('link', { name: 'أنشئ حسابًا' })).toHaveAttribute('href', '/signup/');
  });
});
