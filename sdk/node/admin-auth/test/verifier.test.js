// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

// The verifier against a local issuer with keys of its own: every way a token can be forged
// or misused must fail with the code firebase-admin would give.

import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { generateKeyPairSync, sign, createHmac } from 'node:crypto';
import { Verifier, ID_TOKEN, SESSION_COOKIE } from '../lib/verifier.js';

const projectId = 'demo-app';
const { privateKey, publicKey } = generateKeyPairSync('rsa', { modulusLength: 2048 });
const other = generateKeyPairSync('rsa', { modulusLength: 2048 });
const kid = 'key-1';
let server, url;
let discoveryIssuer = null; // overrides the issuer the discovery document claims
let jwksURI = null;

before(async () => {
  server = createServer((req, res) => {
    const m = /^\/(issuers|session-issuers)\/([^/]+)\/\.well-known\/(openid-configuration|jwks\.json)$/.exec(req.url);
    if (!m) {
      res.writeHead(404).end();
      return;
    }
    const iss = `${url}/${m[1]}/${m[2]}`;
    res.setHeader('content-type', 'application/json');
    res.setHeader('cache-control', 'public, max-age=300');
    if (m[3] === 'openid-configuration') {
      res.end(JSON.stringify({ issuer: discoveryIssuer ?? iss, jwks_uri: jwksURI ?? `${iss}/.well-known/jwks.json` }));
      return;
    }
    // Each issuer has its own key, so ID tokens and cookies cannot stand in for each other.
    const key = m[1] === 'issuers' ? publicKey : other.publicKey;
    res.end(JSON.stringify({ keys: [{ ...key.export({ format: 'jwk' }), kid, alg: 'RS256', use: 'sig' }] }));
  });
  await new Promise((r) => server.listen(0, '127.0.0.1', r));
  url = `http://127.0.0.1:${server.address().port}`;
});

after(() => server.close());

const b64 = (v) => Buffer.from(JSON.stringify(v)).toString('base64url');

function token({ header = {}, claims = {}, key = privateKey, issuer = 'issuers' } = {}) {
  const now = Math.floor(Date.now() / 1000);
  const h = { alg: 'RS256', kid, typ: 'JWT', ...header };
  const c = { iss: `${url}/${issuer}/${projectId}`, aud: projectId, sub: 'user-1', iat: now, exp: now + 3600, auth_time: now, ...claims };
  const signing = `${b64(h)}.${b64(c)}`;
  return `${signing}.${sign('RSA-SHA256', Buffer.from(signing), key).toString('base64url')}`;
}

const ids = () => new Verifier({ url, projectId, kind: ID_TOKEN });

async function rejects(promise, code, re) {
  await assert.rejects(promise, (err) => {
    assert.equal(err.code, `auth/${code}`);
    if (re) assert.match(err.message, re);
    return true;
  });
}

test('a genuine ID token verifies and carries uid', async () => {
  const decoded = await ids().verify(token({ claims: { role: 'admin' } }));
  assert.equal(decoded.uid, 'user-1');
  assert.equal(decoded.role, 'admin');
});

test('expired tokens are id-token-expired, with no clock tolerance', async () => {
  const now = Math.floor(Date.now() / 1000);
  await rejects(ids().verify(token({ claims: { iat: now - 3600, exp: now } })), 'id-token-expired');
});

test('forged and misused tokens are argument-error', async (t) => {
  const now = Math.floor(Date.now() / 1000);
  const good = token();
  const [h, p] = good.split('.');
  const cases = {
    'alg none': `${b64({ alg: 'none', kid })}.${p}.`,
    'unsigned RS256': `${h}.${p}.`,
    'HS256 with the public key as secret': (() => {
      const hh = b64({ alg: 'HS256', kid });
      const mac = createHmac('sha256', publicKey.export({ format: 'pem', type: 'spki' })).update(`${hh}.${p}`).digest('base64url');
      return `${hh}.${p}.${mac}`;
    })(),
    'no kid': token({ header: { kid: undefined } }),
    'unknown kid': token({ header: { kid: 'nope' } }),
    'other key': token({ key: other.privateKey }),
    'modified payload': `${h}.${b64({ ...JSON.parse(Buffer.from(p, 'base64url')), sub: 'admin' })}.${good.split('.')[2]}`,
    'other project': token({ claims: { aud: 'other-app' } }),
    'Google issuer': token({ claims: { iss: `https://securetoken.google.com/${projectId}` } }),
    'session cookie': token({ issuer: 'session-issuers', key: other.privateKey }),
    'empty sub': token({ claims: { sub: '' } }),
    'long sub': token({ claims: { sub: 'x'.repeat(129) } }),
    'future iat': token({ claims: { iat: now + 3600 } }),
    'no auth_time': token({ claims: { auth_time: undefined } }),
    'future auth_time': token({ claims: { auth_time: now + 3600 } }),
    'not a JWT': 'abc.def',
    'garbage segments': 'a.b.c',
    // The same signature bytes, spelled with the unused low bits of the last character set.
    'non-canonical signature': (() => {
      const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_';
      const sig = good.split('.')[2];
      return `${h}.${p}.${sig.slice(0, -1)}${alphabet[alphabet.indexOf(sig.at(-1)) + 1]}`;
    })(),
  };
  for (const [name, tok] of Object.entries(cases)) {
    await t.test(name, () => rejects(ids().verify(tok), 'argument-error'));
  }
  await t.test('empty', () => rejects(ids().verify(''), 'argument-error'));
  await t.test('expired and forged is reported as forged', () =>
    rejects(ids().verify(token({ key: other.privateKey, claims: { iat: now - 7200, exp: now - 3600 } })), 'argument-error'));
});

test('a custom token is named as such', async () => {
  const custom = `${b64({ alg: 'RS256', typ: 'JWT' })}.${b64({ aud: 'https://identitytoolkit.googleapis.com/google.identity.identitytoolkit.v1.IdentityToolkit' })}.sig`;
  await rejects(ids().verify(custom), 'argument-error', /custom token/);
});

test('session cookies verify under their own issuer and code', async () => {
  const cookies = new Verifier({ url, projectId, kind: SESSION_COOKIE });
  const cookie = token({ issuer: 'session-issuers', key: other.privateKey });
  assert.equal((await cookies.verify(cookie)).uid, 'user-1');
  await rejects(cookies.verify(token()), 'argument-error');
  const now = Math.floor(Date.now() / 1000);
  await rejects(cookies.verify(token({ issuer: 'session-issuers', key: other.privateKey, claims: { exp: now - 1 } })), 'session-cookie-expired');
});

test('the discovery document must describe the issuer and point at the same server', async (t) => {
  await t.test('wrong issuer', async () => {
    discoveryIssuer = 'https://evil.example/issuers/demo-app';
    try {
      await rejects(ids().verify(token()), 'argument-error', /public keys/);
    } finally {
      discoveryIssuer = null;
    }
  });
  await t.test('keys elsewhere', async () => {
    jwksURI = 'https://evil.example/jwks.json';
    try {
      await rejects(ids().verify(token()), 'argument-error', /public keys/);
    } finally {
      jwksURI = null;
    }
  });
});
