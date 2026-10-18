import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { AuthUser, FederatedProvider } from '@/lib/api/types';
import { json, renderInProject, stubApi } from '@/test/render';
import { AuthSection } from './AuthSection';
import { SignInMethodsScreen } from './SignInMethodsScreen';
import { UserScreen } from './UserScreen';
import { UsersScreen } from './UsersScreen';

const BASE = '/console/v1/projects/chat-app/auth';
const notFound = () => json(404, { error: { code: 404, status: 'NOT_FOUND', message: 'Requested entity was not found.' } });
const authError = (message: string, status = 400, rpc = 'INVALID_ARGUMENT') => () => json(status, { error: { code: status, status: rpc, message } });

const amal: AuthUser = {
  uid: 'u-amal',
  email: 'amal@example.com',
  emailVerified: true,
  disabled: false,
  providers: [{ providerId: 'password', email: 'amal@example.com' }],
  customClaims: '{"role": "admin"}',
  hasPassword: true,
  createTime: '2026-10-03T08:10:00Z',
  tokensValidAfterTime: '2026-10-03T08:10:00Z',
};

const config = {
  passwordEnabled: true,
  anonymousEnabled: false,
  clientSignupEnabled: true,
  enumerationProtection: true,
  authorizedDomains: ['localhost'],
  passwordMinLength: 6,
  passwordRequireLowercase: false,
  passwordRequireUppercase: false,
  passwordRequireNumeric: false,
  passwordRequireSymbol: false,
  passwordEnforcement: 'OFF',
  appName: '',
  updateTime: '2026-10-03T08:10:00Z',
  limits: {},
};

const body = (fetchMock: ReturnType<typeof stubApi>, method: string, path: string) => {
  const call = fetchMock.mock.calls.findLast(([url, init]) => String(url).split('?')[0] === path && (init?.method ?? 'GET') === method);
  return call ? (JSON.parse(String(call[1]?.body ?? 'null')) as unknown) : undefined;
};

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('UsersScreen', () => {
  it('lists users and searches by email prefix or uid', async () => {
    const fetchMock = stubApi({ [`GET ${BASE}/users`]: () => json(200, { users: [amal], nextPageToken: '', totalSize: 1 }) });
    renderInProject(<UsersScreen />);
    expect(await screen.findByRole('link', { name: 'amal@example.com' })).toHaveAttribute('href', '/project/auth/user/?id=chat-app&uid=u-amal');
    expect(screen.getByText('1–1 من 1')).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText('البحث عن مستخدمين'), 'AM');
    await waitFor(() => expect(fetchMock.mock.calls.some(([url]) => String(url).includes('query=AM'))).toBe(true));
  });

  it('adds a user and shows a too-short password on the field', async () => {
    const fetchMock = stubApi({
      [`GET ${BASE}/users`]: () => json(200, { users: [], nextPageToken: '', totalSize: 0 }),
      [`POST ${BASE}/users`]: authError('WEAK_PASSWORD : Password should be at least 8 characters'),
    });
    renderInProject(<UsersScreen />);
    await userEvent.click((await screen.findAllByRole('button', { name: 'إضافة مستخدم' }))[0]!);
    const dialog = screen.getByRole('dialog');
    await userEvent.click(within(dialog).getByRole('button', { name: 'إضافة المستخدم' }));
    expect(within(dialog).getByText('أدخل بريدًا إلكترونيًا.')).toBeInTheDocument();
    await userEvent.type(within(dialog).getByLabelText(/البريد الإلكتروني/), 'layla@example.com');
    await userEvent.type(within(dialog).getByLabelText(/كلمة المرور/), 'short');
    await userEvent.click(within(dialog).getByRole('button', { name: 'إضافة المستخدم' }));
    expect(await within(dialog).findByText('كلمة المرور أقصر مما تسمح به سياسة هذا المشروع.')).toBeInTheDocument();
    expect(body(fetchMock, 'POST', `${BASE}/users`)).toEqual({ email: 'layla@example.com', password: 'short' });
  });
});

describe('UserScreen', () => {
  const routes = (extra: Parameters<typeof stubApi>[0] = {}) =>
    stubApi({ [`GET ${BASE}/users/u-amal`]: () => json(200, amal), [`PATCH ${BASE}/users/u-amal`]: () => json(200, amal), ...extra });

  it('checks claims as they are typed and saves them compact', async () => {
    const fetchMock = routes();
    renderInProject(<UserScreen />, { search: 'id=chat-app&uid=u-amal' });
    const editor = await screen.findByLabelText('المطالبات (JSON)');
    expect(screen.getByText('16 / 1000 بايت')).toBeInTheDocument();
    const save = screen.getByRole('button', { name: 'حفظ' });
    expect(save).toBeDisabled();

    await userEvent.clear(editor);
    await userEvent.type(editor, '{{"sub": 1}');
    expect(screen.getByText('الاسم "sub" محجوز لرمز الهوية. اختر اسمًا آخر.')).toBeInTheDocument();
    expect(save).toBeDisabled();

    await userEvent.clear(editor);
    await userEvent.type(editor, '{{"big": "' + 'x'.repeat(1000) + '"}');
    expect(screen.getByText('1010 / 1000 بايت')).toBeInTheDocument();
    expect(save).toBeDisabled();

    await userEvent.clear(editor);
    await userEvent.type(editor, '{{ "tier" : "gold" }');
    await userEvent.click(save);
    await waitFor(() => expect(body(fetchMock, 'PATCH', `${BASE}/users/u-amal`)).toEqual({ customClaims: '{"tier":"gold"}' }));
  });

  it('sends a password reset in the chosen language and shows the link', async () => {
    const link = 'https://console.example.com/__/auth/action?mode=resetPassword&oobCode=abc&lang=en';
    const fetchMock = routes({ [`POST ${BASE}/users/u-amal:sendPasswordReset`]: () => json(200, { link }) });
    renderInProject(<UserScreen />, { search: 'id=chat-app&uid=u-amal' });
    await userEvent.click(await screen.findByRole('button', { name: 'إرسال إعادة تعيين كلمة المرور' }));
    const dialog = screen.getByRole('alertdialog');
    await userEvent.click(within(dialog).getByRole('button', { name: /لغة الرسالة/ }));
    await userEvent.click(screen.getByRole('option', { name: 'English' }));
    await userEvent.click(within(dialog).getByRole('button', { name: 'إرسال إعادة تعيين كلمة المرور' }));
    expect(await screen.findByText(link)).toBeInTheDocument();
    expect(body(fetchMock, 'POST', `${BASE}/users/u-amal:sendPasswordReset`)).toEqual({ language: 'en' });
  });

  it('asks before disabling', async () => {
    const fetchMock = routes();
    renderInProject(<UserScreen />, { search: 'id=chat-app&uid=u-amal' });
    await userEvent.click(await screen.findByRole('button', { name: 'تعطيل' }));
    const dialog = screen.getByRole('alertdialog');
    await userEvent.click(within(dialog).getByRole('button', { name: 'تعطيل' }));
    await waitFor(() => expect(body(fetchMock, 'PATCH', `${BASE}/users/u-amal`)).toEqual({ disabled: true }));
  });
});

describe('SignInMethodsScreen', () => {
  const google: FederatedProvider = { providerId: 'google.com', enabled: true, clientId: 'client.apps.googleusercontent.com', clientSecretSet: true };
  const apple: FederatedProvider = { providerId: 'apple.com', enabled: false, servicesId: '', teamId: '', keyId: '', privateKeySet: false };
  const providers = () => json(200, { redirectUri: 'https://console.example.com/__/auth/handler', providers: [google, apple] });

  it('never shows a saved secret and sends one only when replaced', async () => {
    const fetchMock = stubApi({
      [`GET ${BASE}/config`]: () => json(200, config),
      [`GET ${BASE}/providers`]: providers,
      [`PATCH ${BASE}/providers/google.com`]: () => json(200, google),
    });
    renderInProject(<SignInMethodsScreen />);
    const secret = await screen.findByLabelText('سرّ العميل');
    expect(secret).toHaveValue('');
    expect(screen.getByText('محفوظ، ولن يُعرض مجددًا. أدخل قيمة جديدة فقط إن أردت استبداله.')).toBeInTheDocument();
    expect(screen.getByText('https://console.example.com/__/auth/handler')).toBeInTheDocument();

    const card = secret.closest('section') as HTMLElement;
    await userEvent.click(within(card).getByRole('button', { name: 'حفظ' }));
    await waitFor(() => expect(body(fetchMock, 'PATCH', `${BASE}/providers/google.com`)).toEqual({ enabled: true, clientId: google.clientId }));
    await userEvent.type(secret, 'new-secret');
    await userEvent.click(within(card).getByRole('button', { name: 'حفظ' }));
    await waitFor(() =>
      expect(body(fetchMock, 'PATCH', `${BASE}/providers/google.com`)).toEqual({ enabled: true, clientId: google.clientId, clientSecret: 'new-secret' }),
    );
  });

  it('checks Apple credentials before enabling it', async () => {
    const fetchMock = stubApi({ [`GET ${BASE}/config`]: () => json(200, config), [`GET ${BASE}/providers`]: providers });
    renderInProject(<SignInMethodsScreen />);
    const teamId = await screen.findByLabelText('معرّف الفريق');
    const card = teamId.closest('section') as HTMLElement;
    await userEvent.click(within(card).getByText('مفعّل', { selector: 'label *, label' }));
    await userEvent.type(teamId, 'abc');
    await userEvent.click(within(card).getByRole('button', { name: 'حفظ' }));
    expect(within(card).getAllByText('مطلوب لتفعيل هذا المزوّد.')).toHaveLength(3);
    expect(within(card).getByText('10 أحرف لاتينية كبيرة أو أرقام، كما تعرضه Apple.')).toBeInTheDocument();
    expect(body(fetchMock, 'PATCH', `${BASE}/providers/apple.com`)).toBeUndefined();
  });

  it('saves switches at once', async () => {
    const fetchMock = stubApi({
      [`GET ${BASE}/config`]: () => json(200, config),
      [`GET ${BASE}/providers`]: providers,
      [`PATCH ${BASE}/config`]: () => json(200, { ...config, clientSignupEnabled: false }),
    });
    renderInProject(<SignInMethodsScreen />);
    await userEvent.click(await screen.findByRole('switch', { name: 'منع إنشاء الحسابات من التطبيقات' }));
    await waitFor(() => expect(body(fetchMock, 'PATCH', `${BASE}/config`)).toEqual({ clientSignupEnabled: false }));
    expect(await screen.findByRole('switch', { name: 'منع إنشاء الحسابات من التطبيقات' })).toBeChecked();
  });

  it('refuses a domain with a scheme before calling the server', async () => {
    const fetchMock = stubApi({ [`GET ${BASE}/config`]: () => json(200, config), [`GET ${BASE}/providers`]: providers });
    renderInProject(<SignInMethodsScreen />);
    await userEvent.type(await screen.findByLabelText('إضافة نطاق'), 'https://app.example.com');
    await userEvent.click(screen.getByRole('button', { name: 'إضافة' }));
    expect(screen.getByText('أدخل اسم مضيف، مثل app.example.com، دون https:// ودون مسار.')).toBeInTheDocument();
    expect(body(fetchMock, 'PATCH', `${BASE}/config`)).toBeUndefined();
  });
});

describe('AuthSection', () => {
  it.each([
    ['a 404', notFound],
    ['SMTP with no earlier messages', () => json(200, { enabled: false, messages: [] })],
  ])('hides the Mailbox tab for %s', async (_, mailbox) => {
    const fetchMock = stubApi({ [`GET ${BASE}/mailbox`]: mailbox });
    renderInProject(<AuthSection>content</AuthSection>);
    await waitFor(() => expect(fetchMock).toHaveBeenCalled());
    expect(screen.getByRole('tab', { name: 'المستخدمون' })).toHaveAttribute('aria-selected', 'true');
    expect(screen.queryByRole('tab', { name: 'صندوق البريد' })).not.toBeInTheDocument();
  });

  it('shows the Mailbox tab in dev mode', async () => {
    stubApi({ [`GET ${BASE}/mailbox`]: () => json(200, { enabled: true, messages: [] }) });
    renderInProject(<AuthSection>content</AuthSection>);
    expect(await screen.findByRole('tab', { name: 'صندوق البريد' })).toHaveAttribute('href', '/project/auth/mailbox/?id=chat-app');
  });
});
