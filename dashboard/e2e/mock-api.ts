import type { Page, Route } from '@playwright/test';
import type {
  Account,
  ApiKey,
  App,
  AuditEntry,
  Locale,
  Platform,
  Project,
  ServiceAccount,
  ServiceAccountKey,
  Session,
  ShaCertificate,
  Status,
  Token,
} from '../src/lib/api/types';
import { MockAuth } from './mock-auth';

// A deterministic, in-memory stand-in for /console/v1 (docs/api/console.md), installed with
// page.route. Each test gets a fresh copy, so writes are visible within a test only.

export const NOW = '2026-10-18T09:00:00Z';
const T = (iso: string) => `2026-10-${iso}Z`;
const PASSWORD = 'correct horse battery';

interface Store {
  status: Status;
  account: Account;
  signedIn: boolean;
  projects: Project[];
  apps: Record<string, App[]>;
  sha: Record<string, ShaCertificate[]>;
  keys: Record<string, ApiKey[]>;
  serviceAccounts: Record<string, ServiceAccount[]>;
  saKeys: Record<string, ServiceAccountKey[]>;
  audit: Record<string, AuditEntry[]>;
  sessions: Session[];
  tokens: Token[];
  failedLogins: number;
  counter: number;
}

function project(projectId: string, displayName: string, number: string, extra: Partial<Project> = {}): Project {
  return {
    name: `projects/${projectId}`,
    projectId,
    projectNumber: number,
    displayName,
    state: 'ACTIVE',
    etag: `1_${projectId}`,
    authDomain: 'console.example.com',
    storageBucket: `${projectId}.console.example.com`,
    createTime: T('02T10:15:00'),
    updateTime: T('02T10:15:00'),
    ...extra,
  };
}

function app(projectId: string, number: string, platform: Platform, suffix: string, extra: Partial<App>): App {
  const appId = `1:${number}:${platform.toLowerCase()}:${suffix}`;
  return {
    name: `projects/${projectId}/apps/${appId}`,
    appId,
    platform,
    displayName: '',
    state: 'ACTIVE',
    etag: `1_${suffix}`,
    createTime: T('03T08:00:00'),
    updateTime: T('03T08:00:00'),
    ...extra,
  };
}

function key(projectId: string, uid: string, keyString: string, extra: Partial<ApiKey>): ApiKey {
  return {
    name: `projects/${projectId}/apiKeys/${uid}`,
    uid,
    keyString,
    displayName: '',
    restrictions: {},
    state: 'ACTIVE',
    etag: `1_${uid}`,
    createTime: T('03T08:00:00'),
    updateTime: T('03T08:00:00'),
    ...extra,
  };
}

function audit(id: number, time: string, action: string, target: string): AuditEntry {
  return {
    id: String(id),
    time: T(time),
    actorType: 'account',
    actorId: '7c0e5a3e-1f0b-4a51-9a64-2d1f0c4b8e21',
    action,
    target,
    result: 'ok',
    ip: '203.0.113.7',
    details: {},
  };
}

export function seed(locale: Locale): Store {
  const chat = '123456789012';
  return {
    status: { setupRequired: false, signupOpen: false, version: '0.1.0', limits: { servers: 1, maxServiceAccountKeys: 10, deletedRetentionDays: 30 } },
    account: { id: '7c0e5a3e-1f0b-4a51-9a64-2d1f0c4b8e21', email: 'layla@example.com', displayName: 'Layla', locale, createTime: T('01T09:00:00') },
    signedIn: true,
    projects: [
      project('chat-app', 'Chat App', chat),
      project('maps-demo', 'Maps Demo', '987654321098'),
      project('old-store', 'Old Store', '555555555555', {
        state: 'DELETED',
        deleteTime: T('10T12:00:00'),
        expireTime: '2026-11-09T12:00:00Z',
      }),
    ],
    apps: {
      'chat-app': [
        app('chat-app', chat, 'ANDROID', '6b7fd59bb891c192', { displayName: 'Chat Android', packageName: 'com.example.chat' }),
        app('chat-app', chat, 'IOS', '6241dad7727ddf8b', { bundleId: 'com.example.chat' }),
        app('chat-app', chat, 'WEB', 'e45759ef1249b924', { displayName: 'Chat Web' }),
      ],
      'maps-demo': [],
      'old-store': [],
    },
    sha: {
      '1:123456789012:android:6b7fd59bb891c192': [
        { name: 'sha/1', id: '0f2b1c5e-0000-4000-8000-000000000001', shaHash: 'abcdef0123456789abcdef0123456789abcdef01', certType: 'SHA_1' },
        {
          name: 'sha/2',
          id: '0f2b1c5e-0000-4000-8000-000000000002',
          shaHash: '5f1c9e0a7b3d2c4e6f8a0b1c2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c0d1e',
          certType: 'SHA_256',
        },
      ],
    },
    keys: {
      'chat-app': [
        key('chat-app', 'a1d9c1f2-0000-4000-8000-00000000000a', 'Aru_KLJ_x6mRHi32Jx4MozSpdnVxKwxjZj2ly0k', {
          displayName: 'Android key (auto created)',
          platform: 'ANDROID',
        }),
        key('chat-app', 'a1d9c1f2-0000-4000-8000-00000000000b', 'AruyV75Naa-SgWP5xR9myyNzpUGZxYVC3lAgz0X', {
          displayName: 'Browser key (auto created)',
          platform: 'WEB',
          restrictions: { browserKeyRestrictions: { allowedReferrers: ['https://chat.example.com/*', 'http://localhost:5173/*'] } },
        }),
        key('chat-app', 'a1d9c1f2-0000-4000-8000-00000000000c', 'AruTO1t641VSlgosv8q_3TAS8Y6TB6DxXSJW10e', {
          displayName: 'iOS key (auto created)',
          platform: 'IOS',
        }),
        key('chat-app', 'a1d9c1f2-0000-4000-8000-00000000000d', 'AruQx2b7Lr0Yc9W1nE4tKpS6vHd3Mj8Fg5Za0Bq', {
          displayName: 'Old test key',
          state: 'DISABLED',
        }),
      ],
    },
    serviceAccounts: {
      'chat-app': [
        {
          name: 'projects/chat-app/serviceAccounts/5b2e',
          id: '5b2e0c3d-0000-4000-8000-000000000005',
          email: 'backend-server@chat-app.sa.console.example.com',
          uniqueId: '239747524080533984228',
          displayName: 'Backend server',
          disabled: false,
          createTime: T('05T11:30:00'),
        },
      ],
    },
    saKeys: {
      '5b2e0c3d-0000-4000-8000-000000000005': [
        { keyId: '6120a0ce0671e7b41f5b063885e76810ac18bcc4', keyAlgorithm: 'KEY_ALG_RSA_2048', validAfterTime: T('05T11:31:00'), disabled: false, createTime: T('05T11:31:00') },
      ],
    },
    audit: {
      'chat-app': [
        audit(8, '05T11:31:00', 'serviceaccount.key.create', 'backend-server@chat-app.sa.console.example.com'),
        audit(7, '05T11:30:00', 'serviceaccount.create', 'backend-server@chat-app.sa.console.example.com'),
        audit(6, '04T16:20:00', 'apikey.update', 'a1d9c1f2-0000-4000-8000-00000000000b'),
        audit(5, '04T16:00:00', 'app.sha.add', '1:123456789012:android:6b7fd59bb891c192'),
        audit(4, '03T08:02:00', 'app.create', '1:123456789012:web:e45759ef1249b924'),
        audit(3, '03T08:01:00', 'app.create', '1:123456789012:ios:6241dad7727ddf8b'),
        audit(2, '03T08:00:00', 'app.create', '1:123456789012:android:6b7fd59bb891c192'),
        audit(1, '02T10:15:00', 'project.create', 'projects/chat-app'),
      ],
    },
    sessions: [
      {
        id: 's1',
        current: true,
        ip: '203.0.113.7',
        userAgent: 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36',
        createTime: T('18T08:00:00'),
        lastSeenTime: T('18T08:59:00'),
        expireTime: T('18T16:00:00'),
      },
      {
        id: 's2',
        current: false,
        ip: '198.51.100.24',
        userAgent: 'Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:140.0) Gecko/20100101 Firefox/140.0',
        createTime: T('17T19:10:00'),
        lastSeenTime: T('17T20:45:00'),
        expireTime: T('18T03:10:00'),
      },
    ],
    tokens: [{ id: 't1', name: 'deploy script', prefix: 'aruma_pat_Q7fK', createTime: T('06T10:00:00'), lastUsedTime: T('17T22:00:00'), expireTime: '2027-01-04T10:00:00Z' }],
    failedLogins: 0,
    counter: 0,
  };
}

type Reply = { status: number; body?: unknown; headers?: Record<string, string>; raw?: string };
const ok = (body: unknown): Reply => ({ status: 200, body });
const empty = (): Reply => ({ status: 204 });
const fail = (status: number, rpc: string, message: string, field?: string): Reply => ({
  status,
  body: {
    error: {
      code: status,
      status: rpc,
      message,
      ...(field && { details: [{ '@type': 'type.googleapis.com/google.rpc.BadRequest', fieldViolations: [{ field, description: message }] }] }),
    },
  },
});
const notFound = () => fail(404, 'NOT_FOUND', 'Requested entity was not found.');
const download = (name: string, contentType: string, raw: string): Reply => ({
  status: 200,
  raw,
  headers: { 'Content-Type': contentType, 'Content-Disposition': `attachment; filename=${name}` },
});

const PROJECT_ID = /^[a-z][a-z0-9-]{4,28}[a-z0-9]$/;

export class MockApi {
  readonly store: Store;
  readonly auth = new MockAuth();
  readonly calls: { method: string; path: string; body: unknown; csrf: string | null }[] = [];

  constructor(locale: Locale) {
    this.store = seed(locale);
  }

  async install(page: Page): Promise<void> {
    await page.route('**/console/v1/**', (route) => this.handle(route));
  }

  private async handle(route: Route): Promise<void> {
    const req = route.request();
    const url = new URL(req.url());
    const path = url.pathname.replace(/^\/console\/v1/, '');
    const body = req.postData() ? (JSON.parse(req.postData()!) as Record<string, unknown>) : {};
    this.calls.push({ method: req.method(), path, body, csrf: req.headers()['x-aruma-csrf'] ?? null });
    const reply = this.dispatch(req.method(), path, url.searchParams, body);
    await route.fulfill({
      status: reply.status,
      headers: reply.headers ?? (reply.body !== undefined ? { 'Content-Type': 'application/json' } : {}),
      body: reply.raw ?? (reply.body !== undefined ? JSON.stringify(reply.body) : ''),
    });
  }

  private next(prefix: string): string {
    this.store.counter += 1;
    return `${prefix}${String(this.store.counter).padStart(4, '0')}`;
  }

  private dispatch(method: string, path: string, q: URLSearchParams, body: Record<string, unknown>): Reply {
    const s = this.store;
    const seg = path.split('/').filter(Boolean).map(decodeURIComponent);
    const route = `${method} ${path}`;

    if (route === 'GET /status') return ok(s.status);
    if (route === 'POST /setup') {
      if (!s.status.setupRequired) return fail(403, 'PERMISSION_DENIED', 'Setup is complete.');
      if (body.setupCode !== 'SETUP-CODE') return fail(403, 'PERMISSION_DENIED', 'The setup code is invalid.');
      s.status.setupRequired = false;
      s.signedIn = true;
      s.account = { ...s.account, email: String(body.email), displayName: String(body.displayName ?? ''), locale: (body.locale as Locale) ?? 'ar' };
      return ok({ account: s.account, csrfToken: 'csrf-token', expireTime: '2026-10-18T17:00:00Z' });
    }
    if (route === 'POST /auth:login') {
      if (s.failedLogins >= 3) return { ...fail(429, 'RESOURCE_EXHAUSTED', 'Too many attempts. Try again later.'), headers: { 'Content-Type': 'application/json', 'Retry-After': '42' } };
      if (body.password !== PASSWORD) {
        s.failedLogins += 1;
        return fail(401, 'UNAUTHENTICATED', 'The email or password is incorrect.');
      }
      s.signedIn = true;
      return ok({ account: s.account, csrfToken: 'csrf-token', expireTime: '2026-10-18T17:00:00Z' });
    }

    // Everything below needs a session.
    if (!s.signedIn) return fail(401, 'UNAUTHENTICATED', 'Request had invalid authentication credentials.');
    if (route === 'POST /auth:logout') {
      s.signedIn = false;
      return empty();
    }
    if (route === 'GET /me') return ok({ account: s.account, csrfToken: 'csrf-token' });
    if (route === 'PATCH /me') {
      s.account = { ...s.account, ...(body as Partial<Account>) };
      return ok({ account: s.account });
    }
    if (route === 'POST /me:changePassword') {
      if (body.currentPassword !== PASSWORD) return fail(400, 'INVALID_ARGUMENT', 'The current password is incorrect.', 'currentPassword');
      // Changing the password signs out other sessions and revokes every CLI token.
      s.sessions = s.sessions.filter((x) => x.current);
      s.tokens = [];
      return empty();
    }
    if (route === 'GET /me/sessions') return ok({ sessions: s.sessions });
    if (method === 'DELETE' && seg[0] === 'me' && seg[1] === 'sessions') {
      s.sessions = s.sessions.filter((x) => x.id !== seg[2]);
      return empty();
    }
    if (route === 'GET /me/tokens') return ok({ tokens: s.tokens });
    if (route === 'POST /me/tokens') {
      if (body.currentPassword !== PASSWORD) return fail(400, 'INVALID_ARGUMENT', 'The current password is incorrect.', 'currentPassword');
      const token: Token = { id: this.next('t'), name: String(body.name), prefix: 'aruma_pat_Xw3z', createTime: NOW };
      s.tokens.push(token);
      return ok({ ...token, token: 'aruma_pat_Xw3zR8kQmVtN2pL6yHc4BfJ0sDgE7aUo' });
    }
    if (method === 'DELETE' && seg[0] === 'me' && seg[1] === 'tokens') {
      s.tokens = s.tokens.filter((x) => x.id !== seg[2]);
      return empty();
    }

    // Projects
    if (route === 'GET /projects') {
      return ok({ projects: s.projects.filter((p) => q.get('showDeleted') === 'true' || p.state === 'ACTIVE'), nextPageToken: '' });
    }
    if (route === 'GET /projects:suggestId') {
      const base = (q.get('displayName') ?? '').toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '') || 'project';
      return ok({ projectId: base.length >= 6 ? base : `${base}-app` });
    }
    if (route === 'GET /projects:checkId') {
      const id = q.get('projectId') ?? '';
      return ok({ projectId: id, valid: PROJECT_ID.test(id), available: PROJECT_ID.test(id) && !s.projects.some((p) => p.projectId === id) });
    }
    if (route === 'POST /projects') {
      const id = String(body.projectId);
      if (s.projects.some((p) => p.projectId === id)) return fail(409, 'ALREADY_EXISTS', 'Project ID is already in use.');
      const created = project(id, String(body.displayName), '246813579024', { createTime: NOW, updateTime: NOW });
      s.projects.push(created);
      s.apps[id] = [];
      s.keys[id] = [];
      s.serviceAccounts[id] = [];
      s.audit[id] = [audit(100, '18T09:00:00', 'project.create', `projects/${id}`)];
      return ok(created);
    }
    if (seg[0]?.startsWith('projects')) return this.projectScoped(method, seg, q, body);
    return notFound();
  }

  private projectScoped(method: string, seg: string[], q: URLSearchParams, body: Record<string, unknown>): Reply {
    const s = this.store;
    const [first, ...rest] = seg;
    const projectRef = rest[0] ?? '';
    const [ref, verb] = rest.length === 1 ? projectRef.split(':') : [projectRef, undefined];
    const p = s.projects.find((x) => x.projectId === ref || x.projectNumber === ref);
    if (first !== 'projects' || !p) return notFound();
    const sub = rest.slice(1);
    const update = (patch: Partial<Project>) => {
      Object.assign(p, patch, { etag: `1_${this.next('e')}`, updateTime: NOW });
      return ok(p);
    };

    if (sub.length === 0) {
      if (method === 'GET' && !verb) return ok(p);
      if (method === 'PATCH') return update(body as Partial<Project>);
      if (method === 'POST' && verb === 'delete') {
        if (body.confirmProjectId !== p.projectId) return fail(400, 'INVALID_ARGUMENT', 'Type the project ID.', 'confirmProjectId');
        return update({ state: 'DELETED', deleteTime: NOW, expireTime: '2026-11-17T09:00:00Z' });
      }
      if (method === 'POST' && verb === 'restore') return update({ state: 'ACTIVE', deleteTime: undefined, expireTime: undefined });
    }
    const [kind, idWithVerb, child, childId] = sub;
    if (kind === 'auth') return this.auth.dispatch(p.projectId, method, sub.slice(1), q, body);
    if (kind === 'auditLog') return ok({ entries: s.audit[p.projectId] ?? [], nextPageToken: '' });
    if (kind === 'flutterOptions') return download('firebase_options.dart', 'text/x-dart; charset=utf-8', '// firebase_options.dart\n');

    if (kind === 'apps') {
      const apps = (s.apps[p.projectId] ??= []);
      if (!idWithVerb) {
        if (method === 'GET') return ok({ apps: apps.filter((a) => q.get('showDeleted') === 'true' || a.state === 'ACTIVE'), nextPageToken: '' });
        const platform = body.platform as Platform;
        if (platform === 'ANDROID' && apps.some((a) => a.packageName === body.packageName)) return fail(409, 'ALREADY_EXISTS', 'Exists.');
        const created = app(p.projectId, p.projectNumber, platform, this.next('00000000000c'), {
          displayName: String(body.displayName ?? ''),
          ...(body.packageName ? { packageName: String(body.packageName) } : {}),
          ...(body.bundleId ? { bundleId: String(body.bundleId) } : {}),
          createTime: NOW,
          updateTime: NOW,
        });
        apps.push(created);
        return ok(created);
      }
      const [appId, appVerb] = idWithVerb.split(/:(?=remove$|undelete$)/);
      const a = apps.find((x) => x.appId === appId);
      if (!a) return notFound();
      if (!child) {
        if (method === 'GET') return ok(a);
        if (method === 'PATCH') return ok(Object.assign(a, body, { etag: `1_${this.next('e')}` }));
        if (appVerb === 'remove') return ok(Object.assign(a, { state: 'DELETED', deleteTime: NOW, expireTime: '2026-11-17T09:00:00Z' }));
        if (appVerb === 'undelete') return ok(Object.assign(a, { state: 'ACTIVE', deleteTime: undefined, expireTime: undefined }));
      }
      if (child === 'config') {
        if (q.get('format') === 'snippet') {
          return {
            status: 200,
            headers: { 'Content-Type': 'text/javascript; charset=utf-8' },
            raw: `const firebaseConfig = {\n  apiKey: "AruyV75Naa-SgWP5xR9myyNzpUGZxYVC3lAgz0X",\n  authDomain: "console.example.com",\n  projectId: "${p.projectId}",\n  storageBucket: "${p.storageBucket}",\n  messagingSenderId: "${p.projectNumber}",\n  appId: "${a.appId}",\n};\n`,
          };
        }
        const file = { ANDROID: ['google-services.json', 'application/json'], IOS: ['GoogleService-Info.plist', 'application/x-plist'], WEB: ['firebase-config.json', 'application/json'] }[a.platform];
        return download(file[0]!, file[1]!, '{}\n');
      }
      if (child === 'sha') {
        const certs = (s.sha[a.appId] ??= []);
        if (method === 'GET') return ok({ certificates: certs });
        if (method === 'POST') {
          const hex = String(body.shaHash).replace(/:/g, '').toLowerCase();
          if (certs.some((c) => c.shaHash === hex)) return fail(409, 'ALREADY_EXISTS', 'Exists.');
          const cert: ShaCertificate = { name: 'sha/new', id: this.next('0f2b1c5e-0000-4000-8000-00000000'), shaHash: hex, certType: hex.length === 40 ? 'SHA_1' : 'SHA_256' };
          certs.push(cert);
          return ok(cert);
        }
        if (method === 'DELETE') {
          s.sha[a.appId] = certs.filter((c) => c.id !== childId);
          return empty();
        }
      }
    }

    if (kind === 'apiKeys') {
      const keys = (s.keys[p.projectId] ??= []);
      if (!idWithVerb) {
        if (method === 'GET') return ok({ keys });
        const created = key(p.projectId, this.next('a1d9c1f2-0000-4000-8000-0000000'), 'AruNEWk3yAbCdEfGhIjKlMnOpQrStUvWxYz0123', {
          displayName: String(body.displayName ?? ''),
          restrictions: (body.restrictions as ApiKey['restrictions']) ?? {},
        });
        keys.push(created);
        return ok(created);
      }
      const [uid, keyVerb] = idWithVerb.split(':');
      const k = keys.find((x) => x.uid === uid);
      if (!k) return notFound();
      if (method === 'PATCH') return ok(Object.assign(k, body, { etag: `1_${this.next('e')}` }));
      if (keyVerb === 'disable') return ok(Object.assign(k, { state: 'DISABLED' }));
      if (keyVerb === 'enable') return ok(Object.assign(k, { state: 'ACTIVE' }));
      if (keyVerb === 'rotate') {
        const rotated = key(p.projectId, this.next('a1d9c1f2-0000-4000-8000-0000000'), 'AruROT8tedKeyAbCdEfGhIjKlMnOpQrStUvW012', {
          displayName: `${k.displayName} (rotated)`,
          platform: k.platform,
          rotatedFrom: k.uid,
        });
        k.platform = undefined;
        keys.push(rotated);
        return ok(rotated);
      }
      return ok(k);
    }

    if (kind === 'serviceAccounts') {
      const list = (s.serviceAccounts[p.projectId] ??= []);
      if (!idWithVerb) {
        if (method === 'GET') return ok({ serviceAccounts: list });
        const accountId = String(body.accountId);
        if (list.some((x) => x.email.startsWith(`${accountId}@`))) return fail(409, 'ALREADY_EXISTS', 'Exists.');
        const created: ServiceAccount = {
          name: `projects/${p.projectId}/serviceAccounts/${accountId}`,
          id: this.next('5b2e0c3d-0000-4000-8000-00000000'),
          email: `${accountId}@${p.projectId}.sa.console.example.com`,
          uniqueId: '108231947582039485721',
          displayName: String(body.displayName ?? ''),
          disabled: false,
          createTime: NOW,
        };
        list.push(created);
        return ok(created);
      }
      const sa = list.find((x) => x.id === idWithVerb);
      if (!sa) return notFound();
      const saKeys = (s.saKeys[sa.id] ??= []);
      if (!child && method === 'DELETE') {
        s.serviceAccounts[p.projectId] = list.filter((x) => x.id !== sa.id);
        return empty();
      }
      if (child === 'keys') {
        if (method === 'GET') return ok({ keys: saKeys });
        if (method === 'POST') {
          const keyId = `9d3c${this.next('').padStart(36, '0')}`;
          saKeys.push({ keyId, keyAlgorithm: 'KEY_ALG_RSA_2048', validAfterTime: NOW, disabled: false, createTime: NOW });
          return download(`${p.projectId}-${keyId.slice(0, 12)}.json`, 'application/json', '{"type":"service_account"}\n');
        }
        if (method === 'DELETE') {
          s.saKeys[sa.id] = saKeys.filter((k) => k.keyId !== childId);
          return empty();
        }
      }
    }
    return notFound();
  }
}
