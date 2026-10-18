// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

import { test, describe } from 'node:test';
import assert from 'node:assert/strict';
import aruma, { rewriteAuthLiterals } from '../src/vite.js';
import { ArumaConfigError, parseBase } from '../src/index.js';

// The lines of @firebase/auth (11, 12 and 13) that mode 'build' rewrites, as they are published.
const AUTH_SNIPPET = `
const EID_FROM_APIHOST = new Map([
    ["identitytoolkit.googleapis.com" /* DefaultConfig.API_HOST */, 'p'], // production
    ['staging-identitytoolkit.sandbox.googleapis.com', 's'], // staging
]);
        const config = {
            apiKey,
            authDomain,
            clientPlatform,
            apiHost: "identitytoolkit.googleapis.com" /* DefaultConfig.API_HOST */,
            tokenApiHost: "securetoken.googleapis.com" /* DefaultConfig.TOKEN_API_HOST */,
            apiScheme: "https" /* DefaultConfig.API_SCHEME */,
            sdkClientVersion: _getClientVersion(clientPlatform)
        };
const doc = 'https://identitytoolkit.googleapis.com/v1/x';
`;
const AUTH_ID = '/app/node_modules/@firebase/auth/dist/esm/index-LY22yodV.js';

describe('vite plugin, mode fetch', () => {
  test('not configured: a plugin that does nothing', () => {
    for (const url of [undefined, '', '  ']) {
      const p = aruma({ url });
      assert.deepEqual(Object.keys(p), ['name']);
    }
  });

  test('a refused URL stops the build', () => {
    assert.throws(() => aruma({ url: 'http://aruma.example.com' }), ArumaConfigError);
    assert.throws(() => aruma({ url: 'https://aruma.example.com', mode: 'other' }), /unknown mode/);
  });

  test('injects the install first in index.html, from a virtual module', () => {
    const p = aruma({ url: 'https://aruma.example.com/', log: true });
    const tags = p.transformIndexHtml.handler();
    assert.equal(p.transformIndexHtml.order, 'pre');
    assert.deepEqual(tags, [
      { tag: 'script', attrs: { type: 'module' }, children: "import 'virtual:aruma-web/install';", injectTo: 'head-prepend' },
    ]);
    const id = p.resolveId('virtual:aruma-web/install');
    assert.equal(id, '\0virtual:aruma-web/install');
    assert.equal(p.resolveId('firebase/auth'), null);
    assert.equal(
      p.load(id),
      "import { install } from '@aruma/web';\ninstall({\"url\":\"https://aruma.example.com\",\"allowInsecure\":false,\"log\":true});\n",
    );
    assert.equal(p.load('/src/main.js'), null);
    assert.deepEqual(p.config(), { optimizeDeps: { include: ['@aruma/web'] } });
  });
});

describe('vite plugin, mode build', () => {
  test('rewrites the host literals, consistently with EID_FROM_APIHOST', () => {
    const r = rewriteAuthLiterals(AUTH_SNIPPET, parseBase('https://aruma.example.com/base'));
    assert.equal(r.hosts, 3);
    assert.equal(r.scheme, 0);
    assert.match(r.code, /\["aruma\.example\.com\/base\/identitytoolkit\.googleapis\.com" \/\* DefaultConfig\.API_HOST \*\/, 'p'\]/);
    assert.match(r.code, /apiHost: "aruma\.example\.com\/base\/identitytoolkit\.googleapis\.com"/);
    assert.match(r.code, /tokenApiHost: "aruma\.example\.com\/base\/securetoken\.googleapis\.com"/);
    assert.match(r.code, /apiScheme: "https"/);
    // Other strings are left alone.
    assert.match(r.code, /'staging-identitytoolkit\.sandbox\.googleapis\.com'/);
    assert.match(r.code, /'https:\/\/identitytoolkit\.googleapis\.com\/v1\/x'/);
  });

  test('http Aruma on loopback also rewrites the scheme', () => {
    const r = rewriteAuthLiterals(AUTH_SNIPPET, parseBase('http://127.0.0.1:8093'));
    assert.equal(r.hosts, 3);
    assert.equal(r.scheme, 1);
    assert.match(r.code, /apiHost: "127\.0\.0\.1:8093\/identitytoolkit\.googleapis\.com"/);
    assert.match(r.code, /apiScheme: "http"/);
  });

  function runBuild(plugin, modules) {
    const errors = [];
    const ctx = { error: (m) => { errors.push(m); throw new Error(m); } };
    plugin.buildStart.call(ctx);
    const out = modules.map(([id, code]) => plugin.transform.call(ctx, code, id));
    try {
      plugin.buildEnd.call(ctx);
    } catch {
      // recorded in errors
    }
    return { out, errors };
  }

  test('transforms @firebase/auth only, and fails the build when the literals are gone', () => {
    const p = aruma({ url: 'http://localhost:8093', mode: 'build' });
    assert.equal(p.enforce, 'pre');
    const ok = runBuild(p, [
      [AUTH_ID, AUTH_SNIPPET],
      ['/app/src/main.js', 'const h = "identitytoolkit.googleapis.com";'],
      ['/app/node_modules/@firebase/firestore/dist/index.esm.js', '"securetoken.googleapis.com"'],
    ]);
    assert.deepEqual(ok.errors, []);
    assert.match(ok.out[0].code, /apiHost: "localhost:8093\/identitytoolkit\.googleapis\.com"/);
    assert.equal(ok.out[1], null);
    assert.equal(ok.out[2], null);

    const none = runBuild(p, [['/app/src/main.js', 'x']]);
    assert.deepEqual(none.errors, [], 'an app without firebase/auth builds');

    const changed = runBuild(p, [[AUTH_ID, 'const config = { apiHost: DEFAULT_HOST };']]);
    assert.equal(changed.errors.length, 1);
    assert.match(changed.errors[0], /not found/);
  });

  test('needs Vite 8 (Rolldown) so that the dev server is rewritten too', () => {
    const p = aruma({ url: 'https://aruma.example.com', mode: 'build' });
    assert.throws(() => p.config.call({ meta: { rollupVersion: '4.0.0' } }), /Vite 8/);
    const cfg = p.config.call({ meta: { rolldownVersion: '1.0.0' } });
    const [rewriter] = cfg.optimizeDeps.rolldownOptions.plugins;
    assert.equal(rewriter.name, 'aruma-web:build:https://aruma.example.com');
    assert.equal(typeof rewriter.transform, 'function');
  });
});
