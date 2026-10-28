import type {
  AuthConfig,
  AuthUser,
  EmailTemplate,
  EmailTemplateType,
  FederatedProvider,
  Locale,
  MailboxMessage,
  Mailbox,
  SigningKey,
  SmtpSettings,
} from '../src/lib/api/types';

// The Auth part of the mock console API: /projects/{project}/auth/… (docs/reference/console-api.md, and
// the provider and email shapes in INTEGRATION.md). Deterministic, one fresh copy per test.

const T = (iso: string) => `2026-10-${iso}Z`;
const NOW = '2026-10-18T09:00:00Z';

export interface AuthStore {
  users: AuthUser[];
  config: AuthConfig;
  keys: SigningKey[];
  redirectUri: string;
  providers: FederatedProvider[];
  templates: EmailTemplate[];
  smtp: SmtpSettings;
  /** null answers 404, as an instance that never keeps email does. */
  mailbox: Mailbox | null;
  calls: { method: string; path: string; body: unknown }[];
}

function user(uid: string, extra: Partial<AuthUser>): AuthUser {
  return {
    uid,
    emailVerified: false,
    disabled: false,
    providers: [],
    hasPassword: false,
    createTime: T('05T10:00:00'),
    tokensValidAfterTime: T('05T10:00:00'),
    ...extra,
  };
}

const DEFAULTS: Record<EmailTemplateType, Record<Locale, [string, string]>> = {
  VERIFY_EMAIL: {
    ar: ['وثّق بريدك الإلكتروني في %APP_NAME%', 'مرحبًا %DISPLAY_NAME%،\n\nاضغط الرابط التالي لتوثيق بريدك الإلكتروني:\n%LINK%\n\nإن لم تطلب ذلك فتجاهل هذه الرسالة.\n\nفريق %APP_NAME%'],
    en: ['Verify your email for %APP_NAME%', 'Hello %DISPLAY_NAME%,\n\nFollow this link to verify your email address:\n%LINK%\n\nIf you didn\'t ask for this, ignore this email.\n\nThe %APP_NAME% team'],
  },
  PASSWORD_RESET: {
    ar: ['أعد تعيين كلمة مرورك في %APP_NAME%', 'مرحبًا،\n\nاضغط الرابط التالي لإعادة تعيين كلمة مرور %EMAIL%:\n%LINK%\n\nفريق %APP_NAME%'],
    en: ['Reset your password for %APP_NAME%', 'Hello,\n\nFollow this link to reset the password of %EMAIL%:\n%LINK%\n\nThe %APP_NAME% team'],
  },
  VERIFY_AND_CHANGE_EMAIL: {
    ar: ['وثّق بريدك الجديد في %APP_NAME%', 'مرحبًا،\n\nاضغط الرابط التالي لتغيير بريدك إلى %NEW_EMAIL%:\n%LINK%'],
    en: ['Verify your new email for %APP_NAME%', 'Hello,\n\nFollow this link to change your email to %NEW_EMAIL%:\n%LINK%'],
  },
  RECOVER_EMAIL: {
    ar: ['تغيّر بريد حسابك في %APP_NAME%', 'مرحبًا،\n\nتغيّر بريد حسابك إلى %NEW_EMAIL%. إن لم تفعل ذلك فاضغط الرابط لاستعادته:\n%LINK%'],
    en: ['Your sign-in email for %APP_NAME% was changed', 'Hello,\n\nYour sign-in email was changed to %NEW_EMAIL%. If you didn\'t do this, follow this link to restore it:\n%LINK%'],
  },
};

const defaultTemplate = (type: EmailTemplateType, language: Locale): EmailTemplate => ({
  type,
  language,
  subject: DEFAULTS[type][language][0],
  body: DEFAULTS[type][language][1],
  customized: false,
});

export function seedAuth(): AuthStore {
  const templates = (Object.keys(DEFAULTS) as EmailTemplateType[]).flatMap((type) => (['ar', 'en'] as Locale[]).map((l) => defaultTemplate(type, l)));
  return {
    users: [
      user('0bQ3pX7aKd2LmN8rT4vW6yZ1cE5g', {
        email: 'amal@example.com',
        emailVerified: true,
        displayName: 'أمل',
        providers: [{ providerId: 'password', email: 'amal@example.com' }],
        hasPassword: true,
        customClaims: '{"role": "admin"}',
        createTime: T('03T08:10:00'),
        lastSignInTime: T('18T07:42:00'),
        lastRefreshTime: T('18T08:40:00'),
      }),
      user('2Fh8kL1mN4pQ7rS0tU3vW6xY9zA2', {
        email: 'omar@example.com',
        emailVerified: true,
        displayName: 'Omar',
        providers: [
          { providerId: 'google.com', email: 'omar@example.com' },
          { providerId: 'password', email: 'omar@example.com' },
        ],
        hasPassword: true,
        createTime: T('04T12:00:00'),
        lastSignInTime: T('17T21:05:00'),
      }),
      user('5Jk2mP9qR6sT3uV0wX7yZ4aB1cD8', {
        email: 'huda@example.com',
        providers: [{ providerId: 'password', email: 'huda@example.com' }],
        hasPassword: true,
        createTime: T('09T15:30:00'),
      }),
      user('8Mn5pS2tV9wY6zB3cE0fH7jK4lN1', {
        email: 'yusuf@icloud.example',
        emailVerified: true,
        providers: [{ providerId: 'apple.com', email: 'yusuf@icloud.example' }],
        createTime: T('11T09:00:00'),
        lastSignInTime: T('16T10:00:00'),
      }),
      user('aQ4rT7wZ0cF3iL6oR9uX2aD5gJ8m', { createTime: T('12T18:00:00'), lastSignInTime: T('12T18:00:00') }),
      user('dS1vY4bE7hK0nQ3tW6zC9fI2lO5r', {
        email: 'spam@example.com',
        disabled: true,
        providers: [{ providerId: 'password', email: 'spam@example.com' }],
        hasPassword: true,
        createTime: T('14T02:00:00'),
      }),
    ],
    config: {
      passwordEnabled: true,
      anonymousEnabled: true,
      clientSignupEnabled: true,
      enumerationProtection: true,
      authorizedDomains: ['localhost', 'chat.example.com'],
      passwordMinLength: 8,
      passwordRequireLowercase: false,
      passwordRequireUppercase: false,
      passwordRequireNumeric: false,
      passwordRequireSymbol: false,
      passwordEnforcement: 'OFF',
      appName: 'دردشة',
      updateTime: T('05T10:00:00'),
      limits: {
        idTokenLifetimeSeconds: 3600,
        sessionCookieMaxSeconds: 1209600,
        signingKeyRotationDays: 30,
        requestsPerMinute: 300,
        requestBurst: 100,
        signUpsPerMinute: 2,
        signUpBurst: 100,
        passwordFailures: 10,
        passwordFailureWindowSeconds: 900,
        passwordBackoffMaxSeconds: 900,
        emailCodeLifetimeSeconds: { VERIFY_EMAIL: 259200, PASSWORD_RESET: 3600, VERIFY_AND_CHANGE_EMAIL: 259200, RECOVER_EMAIL: 259200 },
      },
    },
    keys: [
      { kid: '3f9a1c0e7b2d4f6a8c1e3b5d7f9a0c2e4b6d8f1a', purpose: 'id_token', state: 'retired', createTime: T('01T09:00:00'), activateTime: T('01T09:00:00'), retireTime: T('15T09:00:00') },
      { kid: '7c2e4a6b8d0f1a3c5e7b9d1f3a5c7e9b0d2f4a6c', purpose: 'id_token', state: 'active', createTime: T('01T09:00:00'), activateTime: T('15T09:00:00') },
      { kid: 'b1d3f5a7c9e0b2d4f6a8c0e1b3d5f7a9c1e2b4d6', purpose: 'id_token', state: 'next', createTime: T('15T09:00:00') },
      { kid: 'e5a7c9b1d3f0e2a4c6b8d0f1e3a5c7b9d2f4a6e8', purpose: 'session_cookie', state: 'active', createTime: T('02T10:00:00'), activateTime: T('02T10:00:00') },
      { kid: 'f0b2d4e6a8c1f3b5d7e9a0c2f4b6d8e1a3c5f7b9', purpose: 'session_cookie', state: 'next', createTime: T('02T10:00:00') },
    ],
    redirectUri: 'https://console.example.com/__/auth/handler',
    providers: [
      { providerId: 'google.com', enabled: true, clientId: '123456789012-abc123def456.apps.googleusercontent.com', clientSecretSet: true },
      { providerId: 'apple.com', enabled: false, servicesId: '', teamId: '', keyId: '', privateKeySet: false },
    ],
    templates,
    smtp: {
      host: 'smtp.example.com',
      port: 587,
      username: 'auth@chat.example.com',
      passwordSet: true,
      tls: 'starttls',
      senderName: 'دردشة',
      senderAddress: 'no-reply@chat.example.com',
      replyTo: '',
      devMailbox: true,
    },
    mailbox: {
      enabled: true,
      messages: [
        {
          id: 'm2',
          to: 'huda@example.com',
          type: 'VERIFY_EMAIL',
          language: 'ar',
          subject: 'وثّق بريدك الإلكتروني في دردشة',
          text: 'مرحبًا،\n\nاضغط الرابط التالي لتوثيق بريدك الإلكتروني.',
          html: '<p>مرحبًا،</p>',
          link: 'https://console.example.com/__/auth/action?mode=verifyEmail&oobCode=Xk3PqL9vRt2&apiKey=AruyV75Naa-SgWP5xR9myyNzpUGZxYVC3lAgz0X&lang=ar',
          createTime: T('18T08:55:00'),
        },
        {
          id: 'm1',
          to: 'omar@example.com',
          type: 'PASSWORD_RESET',
          language: 'en',
          subject: 'Reset your password for دردشة',
          text: 'Hello,\n\nFollow this link to reset your password.',
          html: '<p>Hello,</p>',
          link: 'https://console.example.com/__/auth/action?mode=resetPassword&oobCode=Bn7WqA4zYd1&apiKey=AruyV75Naa-SgWP5xR9myyNzpUGZxYVC3lAgz0X&lang=en',
          createTime: T('17T21:00:00'),
        },
      ],
    },
    calls: [],
  };
}

type Reply = { status: number; body?: unknown };
const ok = (body: unknown): Reply => ({ status: 200, body });
const empty = (): Reply => ({ status: 204 });
const fail = (status: number, rpc: string, message: string): Reply => ({ status, body: { error: { code: status, status: rpc, message } } });
const notFound = () => fail(404, 'NOT_FOUND', 'Requested entity was not found.');
const authFail = (code: string, rpc = 'INVALID_ARGUMENT', status = 400) => fail(status, rpc, code);

const RESERVED = ['sub', 'iss', 'aud', 'exp', 'iat', 'firebase', 'user_id'];

export class MockAuth {
  private readonly stores: Record<string, AuthStore> = {};
  private counter = 0;

  /** The Auth data of a project, created on first use. */
  store(projectId: string): AuthStore {
    return (this.stores[projectId] ??= projectId === 'chat-app' ? seedAuth() : { ...seedAuth(), users: [] });
  }

  dispatch(projectId: string, method: string, seg: string[], q: URLSearchParams, body: Record<string, unknown>): Reply {
    const s = this.store(projectId);
    s.calls.push({ method, path: seg.join('/'), body });
    const [kind, idWithVerb, language] = seg;

    if (kind === 'users') return this.users(s, method, idWithVerb, q, body);
    if (kind === 'config') {
      if (method === 'GET') return ok(s.config);
      const domains = body.authorizedDomains as string[] | undefined;
      if (domains?.some((d) => !/^[a-z0-9.-]+$/.test(d))) return authFail('INVALID_CONFIG : invalid authorized domain');
      Object.assign(s.config, body, { updateTime: NOW });
      return ok(s.config);
    }
    if (kind === 'signingKeys' && method === 'GET') return ok({ keys: s.keys });
    if (kind === 'signingKeys:rotate') {
      const purpose = body.purpose as SigningKey['purpose'];
      const active = s.keys.find((k) => k.purpose === purpose && k.state === 'active');
      const next = s.keys.find((k) => k.purpose === purpose && k.state === 'next');
      if (active) {
        if (body.emergency) s.keys = s.keys.filter((k) => k !== active);
        else Object.assign(active, { state: 'retired', retireTime: NOW });
      }
      if (next) Object.assign(next, { state: 'active', activateTime: NOW });
      this.counter += 1;
      s.keys.push({ kid: `${String(this.counter).padStart(2, '0')}${'a'.repeat(38)}`, purpose, state: 'next', createTime: NOW });
      return ok({ keys: s.keys });
    }
    if (kind === 'providers') {
      if (method === 'GET') return ok({ redirectUri: s.redirectUri, providers: s.providers });
      const p = s.providers.find((x) => x.providerId === idWithVerb);
      if (!p) return notFound();
      const { clientSecret, privateKey, ...rest } = body;
      const next = { ...p, ...rest };
      if (clientSecret !== undefined) next.clientSecretSet = clientSecret !== '';
      if (privateKey !== undefined) next.privateKeySet = privateKey !== '';
      const complete = p.providerId === 'google.com' ? next.clientId && next.clientSecretSet : next.servicesId && next.teamId && next.keyId && next.privateKeySet;
      if (next.enabled && !complete) return authFail('INVALID_CONFIG : missing credentials');
      Object.assign(p, next);
      return ok(p);
    }
    if (kind === 'templates') {
      if (!idWithVerb) return ok({ templates: s.templates });
      const i = s.templates.findIndex((x) => x.type === idWithVerb && x.language === language);
      if (i < 0) return notFound();
      if (method === 'PUT') {
        if (!String(body.body).includes('%LINK%')) return authFail('INVALID_TEMPLATE : the body must contain %LINK%');
        s.templates[i] = { ...s.templates[i]!, subject: String(body.subject), body: String(body.body), customized: true };
      }
      if (method === 'DELETE') s.templates[i] = defaultTemplate(idWithVerb as EmailTemplateType, language as Locale);
      return ok(s.templates[i]);
    }
    if (kind === 'smtp:test') {
      if (s.smtp.devMailbox) this.deliver(s, { to: String(body.to), type: 'TEST', language: 'en', subject: 'Test message from Aruma', link: '' });
      return ok({ devMailbox: s.smtp.devMailbox });
    }
    if (kind === 'smtp') {
      if (method === 'PATCH') {
        const { password, ...rest } = body;
        if (rest.devMailbox === true && !s.mailbox) return authFail('DEV_MAILBOX_DISABLED : not allowed on this instance');
        Object.assign(s.smtp, rest, password !== undefined ? { passwordSet: password !== '' } : {});
        if (s.mailbox) s.mailbox.enabled = s.smtp.devMailbox;
      }
      return ok(s.smtp);
    }
    if (kind === 'mailbox') {
      if (!s.mailbox) return notFound();
      if (method === 'DELETE') {
        s.mailbox.messages = [];
        return empty();
      }
      return ok(s.mailbox);
    }
    return notFound();
  }

  /** Keeps a message in the dev mailbox, newest first. */
  private deliver(s: AuthStore, m: Pick<MailboxMessage, 'to' | 'type' | 'language' | 'subject' | 'link'>): void {
    this.counter += 1;
    s.mailbox?.messages.unshift({ ...m, id: `m${this.counter + 10}`, text: m.subject, html: `<p>${m.subject}</p>`, createTime: NOW });
  }

  private users(s: AuthStore, method: string, idWithVerb: string | undefined, q: URLSearchParams, body: Record<string, unknown>): Reply {
    if (!idWithVerb) {
      if (method === 'POST') {
        const email = String(body.email ?? '').toLowerCase();
        if (s.users.some((u) => u.email === email)) return authFail('EMAIL_EXISTS', 'ALREADY_EXISTS', 409);
        if (String(body.password ?? '').length < s.config.passwordMinLength) return authFail('WEAK_PASSWORD : Password should be at least 8 characters');
        this.counter += 1;
        const created = user(String(body.uid ?? `new${String(this.counter).padStart(25, '0')}`), {
          email,
          displayName: body.displayName as string | undefined,
          providers: [{ providerId: 'password', email }],
          hasPassword: true,
          createTime: NOW,
          tokensValidAfterTime: NOW,
        });
        s.users.push(created);
        return ok(created);
      }
      const query = (q.get('query') ?? '').toLowerCase();
      const matches = s.users
        .filter((u) => !query || u.email?.startsWith(query) || u.uid === q.get('query'))
        .sort((a, b) => (a.uid < b.uid ? -1 : 1));
      const size = Number(q.get('pageSize') ?? 100);
      const after = q.get('pageToken') ?? '';
      const rest = matches.filter((u) => u.uid > after);
      const page = rest.slice(0, size);
      return ok({ users: page, nextPageToken: rest.length > size ? page.at(-1)!.uid : '', totalSize: matches.length });
    }
    const [uid, verb] = idWithVerb.split(':');
    const u = s.users.find((x) => x.uid === uid);
    if (!u) return authFail('USER_NOT_FOUND', 'NOT_FOUND', 404);
    if (verb === 'revokeSessions') return ok(Object.assign(u, { tokensValidAfterTime: NOW }));
    if (verb === 'sendPasswordReset') {
      const link = `https://console.example.com/__/auth/action?mode=resetPassword&oobCode=Rz8LmQ2${this.counter}&lang=${String(body.language ?? 'ar')}`;
      if (s.smtp.devMailbox) this.deliver(s, { to: u.email ?? '', type: 'PASSWORD_RESET', language: (body.language as Locale) ?? 'ar', subject: 'Reset', link });
      return ok({ link });
    }
    if (method === 'DELETE') {
      s.users = s.users.filter((x) => x !== u);
      return empty();
    }
    if (method === 'PATCH') {
      if (typeof body.customClaims === 'string') {
        if (body.customClaims.length > 1000) return authFail('CLAIMS_TOO_LARGE');
        const claims = body.customClaims ? (JSON.parse(body.customClaims) as Record<string, unknown>) : {};
        const reserved = Object.keys(claims).find((k) => RESERVED.includes(k));
        if (reserved) return authFail(`FORBIDDEN_CLAIM : ${reserved}`);
        u.customClaims = body.customClaims || undefined;
      }
      const { customClaims: _ignored, ...rest } = body;
      Object.assign(u, rest);
    }
    return ok(u);
  }
}
