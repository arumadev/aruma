// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

import { test, describe } from 'node:test';
import assert from 'node:assert/strict';
import {
  AUTH_HOSTS,
  ArumaConfigError,
  install,
  installFromConfig,
  isLoopback,
  matchAuth,
  parseBase,
  rewriteURL,
  status,
  uninstall,
} from '../src/index.js';
import { installStorage } from '../src/storage.js';

const KEY = 'AIzaFakeKeyForTests';

// A fake global with a recording fetch, so each test is isolated from globalThis.
function fakeGlobal() {
  const calls = [];
  const original = async (input, init) => {
    calls.push({ input, init });
    return new Response('{}', { status: 200, headers: { 'content-type': 'application/json' } });
  };
  return { g: { fetch: original }, original, calls };
}

// Captures console output while fn runs.
async function captureConsole(fn) {
  const lines = [];
  const saved = { info: console.info, error: console.error, log: console.log, warn: console.warn };
  for (const k of Object.keys(saved)) console[k] = (...a) => lines.push(a.join(' '));
  try {
    await fn();
  } finally {
    Object.assign(console, saved);
  }
  return lines;
}

describe('rewrite rules', () => {
  const base = parseBase('https://aruma.example.com');

  test('the two Auth hosts keep path and query', () => {
    assert.deepEqual(AUTH_HOSTS, ['identitytoolkit.googleapis.com', 'securetoken.googleapis.com']);
    assert.equal(
      rewriteURL(`https://identitytoolkit.googleapis.com/v1/accounts:signUp?key=${KEY}`, base),
      `https://aruma.example.com/identitytoolkit.googleapis.com/v1/accounts:signUp?key=${KEY}`,
    );
    assert.equal(
      rewriteURL(`https://identitytoolkit.googleapis.com/v2/recaptchaConfig?key=${KEY}&clientType=CLIENT_TYPE_WEB`, base),
      `https://aruma.example.com/identitytoolkit.googleapis.com/v2/recaptchaConfig?key=${KEY}&clientType=CLIENT_TYPE_WEB`,
    );
    assert.equal(
      rewriteURL(`https://securetoken.googleapis.com/v1/token?key=${KEY}`, base),
      `https://aruma.example.com/securetoken.googleapis.com/v1/token?key=${KEY}`,
    );
    assert.equal(
      rewriteURL('https://identitytoolkit.googleapis.com/v1/projects', base),
      'https://aruma.example.com/identitytoolkit.googleapis.com/v1/projects',
    );
  });

  test('a path prefix, a port and trailing slashes on the Aruma URL', () => {
    assert.equal(
      rewriteURL('https://securetoken.googleapis.com/v1/token', 'https://example.com:8443/aruma///'),
      'https://example.com:8443/aruma/securetoken.googleapis.com/v1/token',
    );
    assert.equal(
      rewriteURL('https://securetoken.googleapis.com/v1/token', 'http://localhost:8093/'),
      'http://localhost:8093/securetoken.googleapis.com/v1/token',
    );
  });

  test('host case, :443 and a trailing dot are the same host', () => {
    assert.ok(matchAuth('https://IdentityToolkit.GoogleAPIs.com/v1/x'));
    assert.ok(matchAuth('https://identitytoolkit.googleapis.com:443/v1/x'));
    assert.equal(
      rewriteURL('https://securetoken.googleapis.com./v1/token', base),
      'https://aruma.example.com/securetoken.googleapis.com/v1/token',
    );
  });

  test('dot segments cannot escape the host prefix', () => {
    assert.equal(
      rewriteURL('https://identitytoolkit.googleapis.com/v1/../../console/v1/x', base),
      'https://aruma.example.com/identitytoolkit.googleapis.com/console/v1/x',
    );
    assert.equal(
      rewriteURL('https://identitytoolkit.googleapis.com/%2e%2e/%2E%2E/admin', base),
      'https://aruma.example.com/identitytoolkit.googleapis.com/admin',
    );
  });

  test('everything else is not an Auth endpoint', () => {
    for (const url of [
      'http://identitytoolkit.googleapis.com/v1/accounts:signUp', // not https
      'https://identitytoolkit.googleapis.com:8443/v1/x', // not the default port
      'https://user:pw@identitytoolkit.googleapis.com/v1/x', // credentials
      'https://www.googleapis.com/identitytoolkit/v3/relyingparty/getAccountInfo', // the iOS v3 family
      'https://firebaseinstallations.googleapis.com/v1/projects/p/installations',
      'https://apis.google.com/js/api.js',
      'https://identitytoolkit.googleapis.com.evil.example/v1/x',
      'https://evil.identitytoolkit.googleapis.com/v1/x',
      'https://xidentitytoolkit.googleapis.com/v1/x',
      'https://aruma.example.com/identitytoolkit.googleapis.com/v1/x', // already rewritten
      '/identitytoolkit.googleapis.com/v1/x', // relative
      'not a url',
      'wss://securetoken.googleapis.com/v1/token',
    ]) {
      assert.equal(matchAuth(url), null, url);
      assert.equal(rewriteURL(url, base), null, url);
    }
  });
});

describe('Aruma URL validation', () => {
  test('https and loopback http are accepted', () => {
    assert.equal(parseBase('https://aruma.example.com').href, 'https://aruma.example.com');
    assert.equal(parseBase('  https://aruma.example.com/base/ ').href, 'https://aruma.example.com/base');
    assert.equal(parseBase(new URL('https://aruma.example.com')).href, 'https://aruma.example.com');
    for (const url of ['http://localhost:8093', 'http://127.0.0.1:1', 'http://127.255.0.9', 'http://[::1]:8080', 'http://LOCALHOST']) {
      assert.equal(parseBase(url).protocol, 'http', url);
    }
  });

  test('http to another host needs allowInsecure', () => {
    for (const url of ['http://aruma.example.com', 'http://10.0.0.2:8080', 'http://128.0.0.1', 'http://localhost.example.com']) {
      assert.throws(() => parseBase(url), ArumaConfigError, url);
      assert.equal(parseBase(url, { allowInsecure: true }).protocol, 'http');
    }
  });

  test('malformed URLs are refused, without echoing them', () => {
    for (const url of [
      '',
      '   ',
      'aruma.example.com',
      '/relative',
      'ftp://aruma.example.com',
      'javascript:alert(1)',
      'https://aruma.example.com/?key=secret-value',
      'https://aruma.example.com/#secret-value',
      'https://aruma.example.com?',
      'https://user:secret-value@aruma.example.com',
      'https://identitytoolkit.googleapis.com',
      'https://securetoken.googleapis.com/x',
      undefined,
      42,
    ]) {
      assert.throws(
        () => parseBase(url),
        (e) => e instanceof ArumaConfigError && !e.message.includes('secret-value'),
        String(url),
      );
    }
  });

  test('loopback detection', () => {
    for (const h of ['localhost', '127.0.0.1', '127.1.2.3', '[::1]', '::1']) assert.ok(isLoopback(h), h);
    for (const h of ['127.0.0.256', '127.0.0', '0.0.0.0', '10.0.0.1', 'localhost.evil', '::2', '']) assert.ok(!isLoopback(h), h);
  });
});

describe('install: the three input forms', () => {
  const url = 'https://aruma.example.com';
  const google = `https://identitytoolkit.googleapis.com/v1/accounts:signInWithPassword?key=${KEY}`;
  const aruma = `https://aruma.example.com/identitytoolkit.googleapis.com/v1/accounts:signInWithPassword?key=${KEY}`;

  test('a string keeps the init object as is', async () => {
    const { g, calls } = fakeGlobal();
    assert.equal(install({ url, globalObject: g }), true);
    const controller = new AbortController();
    const init = { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-Client-Version': 'Chrome/JsCore/13.0.0' }, body: '{"email":"a@example.com"}', signal: controller.signal, referrerPolicy: 'no-referrer' };
    await g.fetch(google, init);
    assert.equal(calls.length, 1);
    assert.equal(calls[0].input, aruma);
    assert.equal(calls[0].init, init);
  });

  test('a URL object', async () => {
    const { g, calls } = fakeGlobal();
    install({ url, globalObject: g });
    const init = { method: 'POST', body: 'x' };
    await g.fetch(new URL(google), init);
    assert.equal(calls[0].input, aruma);
    assert.equal(calls[0].init, init);
  });

  test('a Request keeps method, headers, body, signal and options', async () => {
    const { g, calls } = fakeGlobal();
    install({ url, globalObject: g });
    const controller = new AbortController();
    const req = new Request(google, {
      method: 'POST',
      headers: { 'content-type': 'application/json', 'x-firebase-gmpid': '1:2:web:3' },
      body: '{"returnSecureToken":true}',
      signal: controller.signal,
      redirect: 'manual',
      referrerPolicy: 'no-referrer',
    });
    await g.fetch(req);
    const out = calls[0].input;
    assert.ok(out instanceof Request);
    assert.equal(calls[0].init, undefined);
    assert.equal(out.url, aruma);
    assert.equal(out.method, 'POST');
    assert.equal(out.headers.get('content-type'), 'application/json');
    assert.equal(out.headers.get('x-firebase-gmpid'), '1:2:web:3');
    assert.equal(await out.text(), '{"returnSecureToken":true}');
    assert.equal(out.redirect, 'manual');
    assert.equal(out.referrerPolicy, 'no-referrer');
    assert.equal(out.signal.aborted, false);
    controller.abort();
    assert.equal(out.signal.aborted, true);
  });

  test('a Request with an init: the init wins, as in fetch', async () => {
    const { g, calls } = fakeGlobal();
    install({ url, globalObject: g });
    await g.fetch(new Request(google, { method: 'POST', body: 'old' }), { method: 'PUT', body: 'new', headers: { 'x-a': '1' } });
    const out = calls[0].input;
    assert.equal(out.url, aruma);
    assert.equal(out.method, 'PUT');
    assert.equal(out.headers.get('x-a'), '1');
    assert.equal(await out.text(), 'new');
  });

  test('a GET Request has no body', async () => {
    const { g, calls } = fakeGlobal();
    install({ url, globalObject: g });
    await g.fetch(new Request('https://securetoken.googleapis.com/v1/x?key=k'));
    assert.equal(calls[0].input.url, 'https://aruma.example.com/securetoken.googleapis.com/v1/x?key=k');
    assert.equal(calls[0].input.method, 'GET');
    assert.equal(calls[0].input.body, null);
  });
});

describe('pass-through', () => {
  test('other requests reach the original fetch untouched (same objects)', async () => {
    const { g, calls } = fakeGlobal();
    install({ url: 'https://aruma.example.com', globalObject: g });
    const init = { method: 'POST', body: 'b' };
    const req = new Request('https://firestore.googleapis.com/google.firestore.v1.Firestore/Listen');
    const u = new URL('https://www.googleapis.com/identitytoolkit/v3/relyingparty/getAccountInfo');
    await g.fetch('https://firebaseinstallations.googleapis.com/v1/x', init);
    await g.fetch(req);
    await g.fetch(u, init);
    await g.fetch('/relative/path');
    await g.fetch('http://identitytoolkit.googleapis.com/v1/x');
    assert.equal(calls[0].input, 'https://firebaseinstallations.googleapis.com/v1/x');
    assert.equal(calls[0].init, init);
    assert.equal(calls[1].input, req);
    assert.equal(calls[2].input, u);
    assert.equal(calls[3].input, '/relative/path');
    assert.equal(calls[4].input, 'http://identitytoolkit.googleapis.com/v1/x');
  });

  test('the original fetch is called with the global as this', async () => {
    let self;
    const g = {
      fetch() {
        self = this;
        return Promise.resolve(new Response(''));
      },
    };
    install({ url: 'https://aruma.example.com', globalObject: g });
    const f = g.fetch; // as FetchProvider calls it: detached
    await f('https://identitytoolkit.googleapis.com/v1/x');
    assert.equal(self, g);
    await f('https://example.com/');
    assert.equal(self, g);
  });
});

describe('safety', () => {
  test('not configured: nothing is installed', () => {
    for (const url of [undefined, null, '', '  ']) {
      const { g, original } = fakeGlobal();
      assert.equal(install({ url, globalObject: g }), false);
      assert.equal(g.fetch, original);
      assert.equal(status({ globalObject: g }), null);
      assert.equal(installFromConfig(url === undefined ? undefined : { url }, { globalObject: g }), false);
      assert.equal(g.fetch, original);
    }
  });

  test('configured but refused: install throws and Auth requests fail without reaching Google', async () => {
    const { g, calls } = fakeGlobal();
    const lines = await captureConsole(() => {
      assert.throws(() => install({ url: 'http://aruma.example.com', globalObject: g }), ArumaConfigError);
    });
    assert.equal(lines.length, 1);
    assert.match(lines[0], /refused/);
    assert.deepEqual(Object.keys(status({ globalObject: g })), ['blocked']);
    await assert.rejects(g.fetch(`https://identitytoolkit.googleapis.com/v1/accounts:signUp?key=${KEY}`, { method: 'POST', body: 'pw' }), TypeError);
    await assert.rejects(g.fetch(new Request('https://securetoken.googleapis.com/v1/token', { method: 'POST', body: 'rt' })), TypeError);
    assert.equal(calls.length, 0);
    // Other requests are not affected.
    await g.fetch('https://example.com/');
    assert.equal(calls.length, 1);
  });

  test('installFromConfig never throws for a refused URL, and still fails closed', async () => {
    const { g, calls } = fakeGlobal();
    const lines = await captureConsole(() => {
      assert.equal(installFromConfig({ url: 'https://aruma.example.com/?key=x' }, { globalObject: g }), false);
    });
    assert.equal(lines.length, 1);
    assert.ok(!lines[0].includes('key=x'));
    await assert.rejects(g.fetch('https://securetoken.googleapis.com/v1/token'));
    assert.equal(calls.length, 0);
  });

  test('a later refused install replaces a working one with the failure, not with Google', async () => {
    const { g, calls } = fakeGlobal();
    install({ url: 'https://aruma.example.com', globalObject: g });
    await captureConsole(() => assert.throws(() => install({ url: 'http://evil.example', globalObject: g })));
    await assert.rejects(g.fetch('https://securetoken.googleapis.com/v1/token'));
    assert.equal(calls.length, 0);
  });

  test('allowInsecure, from options or config strings', () => {
    const { g } = fakeGlobal();
    assert.equal(install({ url: 'http://aruma.internal:8080', allowInsecure: true, globalObject: g }), true);
    assert.deepEqual(status({ globalObject: g }), { url: 'http://aruma.internal:8080' });
    const other = fakeGlobal();
    assert.equal(installFromConfig({ url: 'http://aruma.internal:8080', allowInsecure: 'true' }, { globalObject: other.g }), true);
  });

  test('logs carry method, host and path only: no key, query, headers, token or body', async () => {
    const { g } = fakeGlobal();
    const secret = 'refresh-token-SECRET';
    const lines = await captureConsole(async () => {
      install({ url: 'https://aruma.example.com', log: true, globalObject: g });
      await g.fetch(`https://securetoken.googleapis.com/v1/token?key=${KEY}`, {
        method: 'POST',
        headers: { authorization: `Bearer ${secret}` },
        body: `grant_type=refresh_token&refresh_token=${secret}`,
      });
      await g.fetch(new Request(`https://identitytoolkit.googleapis.com/v1/accounts:lookup?key=${KEY}`, { method: 'POST', body: `{"idToken":"${secret}"}` }));
    });
    assert.equal(lines.length, 3);
    assert.match(lines[1], /POST https:\/\/securetoken\.googleapis\.com\/v1\/token -> https:\/\/aruma\.example\.com\/securetoken\.googleapis\.com\/v1\/token$/);
    assert.match(lines[2], /POST https:\/\/identitytoolkit\.googleapis\.com\/v1\/accounts:lookup -> /);
    for (const line of lines) {
      assert.ok(!line.includes(KEY), line);
      assert.ok(!line.includes(secret), line);
      assert.ok(!line.includes('?'), line);
    }
  });

  test('silent by default', async () => {
    const { g } = fakeGlobal();
    const lines = await captureConsole(async () => {
      install({ url: 'https://aruma.example.com', globalObject: g });
      await g.fetch('https://securetoken.googleapis.com/v1/token');
    });
    assert.deepEqual(lines, []);
  });

  test('no global fetch: refused clearly', () => {
    assert.throws(() => install({ url: 'https://aruma.example.com', globalObject: {} }), ArumaConfigError);
  });
});

describe('idempotence', () => {
  test('fetch is wrapped once; a second install only updates the settings', async () => {
    const { g, calls } = fakeGlobal();
    install({ url: 'https://one.example.com', globalObject: g });
    const wrapper = g.fetch;
    install({ url: 'https://one.example.com', globalObject: g });
    assert.equal(g.fetch, wrapper);
    install({ url: 'https://two.example.com/p', globalObject: g });
    assert.equal(g.fetch, wrapper);
    await g.fetch('https://securetoken.googleapis.com/v1/token');
    assert.equal(calls.length, 1);
    assert.equal(calls[0].input, 'https://two.example.com/p/securetoken.googleapis.com/v1/token');
  });

  test('uninstall restores the original fetch; a later wrapper keeps working', async () => {
    const { g, original, calls } = fakeGlobal();
    install({ url: 'https://aruma.example.com', globalObject: g });
    uninstall({ globalObject: g });
    assert.equal(g.fetch, original);
    assert.equal(status({ globalObject: g }), null);

    install({ url: 'https://aruma.example.com', globalObject: g });
    const ours = g.fetch;
    g.fetch = (input, init) => ours(input, init); // someone wraps fetch after us
    uninstall({ globalObject: g });
    await g.fetch('https://securetoken.googleapis.com/v1/token');
    assert.equal(calls.at(-1).input, 'https://securetoken.googleapis.com/v1/token'); // passes through
    install({ url: 'https://aruma.example.com', globalObject: g }); // the same wrapper becomes active again
    await g.fetch('https://securetoken.googleapis.com/v1/token');
    assert.equal(calls.at(-1).input, 'https://aruma.example.com/securetoken.googleapis.com/v1/token');
  });

  test('two wrapped globals do not share state', async () => {
    const a = fakeGlobal();
    const b = fakeGlobal();
    install({ url: 'https://a.example.com', globalObject: a.g });
    install({ url: 'https://b.example.com', globalObject: b.g });
    await a.g.fetch('https://securetoken.googleapis.com/v1/token');
    await b.g.fetch('https://securetoken.googleapis.com/v1/token');
    assert.equal(a.calls[0].input, 'https://a.example.com/securetoken.googleapis.com/v1/token');
    assert.equal(b.calls[0].input, 'https://b.example.com/securetoken.googleapis.com/v1/token');
  });
});

// SR3-06: with Auth redirected and Storage not, a Storage SDK request to Google's host would carry
// the Aruma ID token there (@firebase/storage 0.14.6, request.ts:237).
describe('the Storage guard', () => {
  const google = 'https://firebasestorage.googleapis.com/v0/b/p.appspot.com/o/a?alt=media';

  // A browser-like global: the recording fetch and an XMLHttpRequest that records what reaches it.
  function browserGlobal() {
    const { g, calls } = fakeGlobal();
    const xhr = [];
    class XHR extends EventTarget {
      open(method, url) {
        this.log = { method, url, headers: {} };
      }
      setRequestHeader(name, value) {
        this.log.headers[name] = value;
      }
      send() {
        xhr.push(this.log);
      }
    }
    g.XMLHttpRequest = XHR;
    return { g, calls, xhr };
  }

  // What the SDK's XhrConnection does: open, its headers, send; resolves on load or error.
  function sdkXHR(g, url, headers) {
    const x = new g.XMLHttpRequest();
    const done = new Promise((resolve) => x.addEventListener('error', () => resolve('error')));
    x.open('GET', url, true);
    for (const [k, v] of Object.entries(headers)) x.setRequestHeader(k, v);
    x.send();
    return done;
  }

  const sdkHeaders = { Authorization: 'Firebase ID_TOKEN', 'X-Firebase-Storage-Version': 'webjs/13.1.0' };

  test('an SDK XHR to Google is never sent, and its token never set', async () => {
    const { g, xhr } = browserGlobal();
    install({ url: 'https://aruma.example.com', globalObject: g });
    const lines = await captureConsole(async () => {
      assert.equal(await sdkXHR(g, google, sdkHeaders), 'error');
      assert.equal(await sdkXHR(g, google, { 'X-Firebase-Storage-Version': 'webjs/13.1.0' }), 'error'); // signed out
    });
    assert.deepEqual(xhr, []);
    assert.equal(lines.length, 1, 'reported once');
    assert.match(lines[0], /Storage request to firebasestorage\.googleapis\.com was blocked.*storage: true/);
  });

  test('an SDK fetch to Google is refused (the Node build), a plain link fetch is not', async () => {
    const { g, calls } = fakeGlobal();
    install({ url: 'https://aruma.example.com', globalObject: g });
    await captureConsole(async () => {
      await assert.rejects(g.fetch(google, { headers: sdkHeaders }), /Storage request .* was blocked/);
      await assert.rejects(g.fetch(new Request(google, { headers: { authorization: 'Firebase t' } })), /blocked/);
    });
    assert.equal(calls.length, 0);
    await g.fetch(`${google}&token=abc`); // a download link: no ID token, left alone
    assert.equal(calls[0].input, `${google}&token=abc`);
  });

  test('other hosts, Aruma, and Storage not on Google pass', async () => {
    const { g, xhr } = browserGlobal();
    install({ url: 'https://aruma.example.com', globalObject: g });
    sdkXHR(g, 'https://aruma.example.com/v0/b/p.appspot.com/o/a', sdkHeaders);
    sdkXHR(g, 'https://example.com/x', { Authorization: 'Bearer x' });
    assert.deepEqual(xhr.map((r) => [r.url, r.headers.Authorization]), [
      ['https://aruma.example.com/v0/b/p.appspot.com/o/a', 'Firebase ID_TOKEN'],
      ['https://example.com/x', 'Bearer x'],
    ]);
  });

  test('not installed, or uninstalled: no guard', async () => {
    const { g, xhr } = browserGlobal();
    sdkXHR(g, google, sdkHeaders);
    assert.equal(xhr.length, 1);
    assert.equal(install({ globalObject: g }), false);
    install({ url: 'https://aruma.example.com', globalObject: g });
    uninstall({ globalObject: g });
    sdkXHR(g, google, sdkHeaders);
    assert.equal(xhr.length, 2);
  });

  test('a refused Aruma URL guards too', async () => {
    const { g, xhr } = browserGlobal();
    await captureConsole(async () => {
      assert.throws(() => install({ url: 'http://aruma.example.com', globalObject: g }), ArumaConfigError);
      assert.equal(await sdkXHR(g, google, sdkHeaders), 'error');
    });
    assert.deepEqual(xhr, []);
  });

  test('with the Storage fallback installed, in either order, the request goes to Aruma', () => {
    for (const order of [['auth', 'storage'], ['storage', 'auth']]) {
      const { g, xhr } = browserGlobal();
      for (const what of order) {
        if (what === 'auth') install({ url: 'https://aruma.example.com', globalObject: g });
        else installStorage({ url: 'https://aruma.example.com', globalObject: g });
      }
      sdkXHR(g, google, sdkHeaders);
      assert.deepEqual(xhr.map((r) => [r.url, r.headers.Authorization]), [
        ['https://aruma.example.com/v0/b/p.appspot.com/o/a?alt=media', 'Firebase ID_TOKEN'],
      ], order.join(' then '));
    }
  });
});
