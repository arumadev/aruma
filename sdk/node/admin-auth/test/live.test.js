// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

// The library against a live Aruma server, next to the official firebase-admin initialised with
// an Aruma key file: AC-A5, AC-A10 to AC-A14 and AC-A20. No connection may leave the machine.

import net from 'node:net';
import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { initializeApp, cert, deleteApp } from 'firebase-admin/app';
import { getAuth as getFirebaseAuth, UserRecord, FirebaseAuthError } from 'firebase-admin/auth';
import { getAuth } from '../index.js';
import { startAruma, clientCall, signUp, refresh, nextSecond } from './server.js';

const connections = [];
const connect = net.Socket.prototype.connect;
net.Socket.prototype.connect = function (...args) {
  const opts = typeof args[0] === 'object' && !Array.isArray(args[0]) ? args[0] : { port: args[0], host: args[1] };
  connections.push(opts.host ?? 'localhost');
  return connect.apply(this, args);
};

let server, app, auth;

before(async () => {
  server = await startAruma();
  // The developer's existing setup: firebase-admin with the key file Aruma issued.
  app = initializeApp({ credential: cert(server.keyPath), projectId: server.expected.projectId }, 'live');
  auth = getAuth(app); // the one changed line
});

after(async () => {
  await deleteApp(app);
  await server?.stop();
  const remote = connections.filter((h) => !['127.0.0.1', 'localhost', '::1'].includes(h));
  assert.deepEqual(remote, [], 'connections left the machine');
});

async function rejectsWith(promise, code) {
  await assert.rejects(promise, (err) => {
    assert.ok(err instanceof FirebaseAuthError, `not a FirebaseAuthError: ${err}`);
    assert.equal(err.code, `auth/${code}`, err.message);
    return true;
  });
}

test('getAuth reads the server from the key file and shares one instance per app', () => {
  assert.equal(auth.url, server.url);
  assert.equal(getAuth(app), auth);
});

// AC-A10
test('verifyIdToken accepts a valid token and returns the official decoded shape', async () => {
  const up = await signUp(server, 'verify@example.com');
  assert.equal(up.status, 200, JSON.stringify(up.body));
  const decoded = await auth.verifyIdToken(up.body.idToken);
  assert.equal(decoded.uid, up.body.localId);
  assert.equal(decoded.sub, up.body.localId);
  assert.equal(decoded.aud, server.expected.projectId);
  assert.equal(decoded.iss, `${server.url}/issuers/${server.expected.projectId}`);
  assert.equal(decoded.email, 'verify@example.com');
  assert.equal(decoded.firebase.sign_in_provider, 'password');
  assert.equal(typeof decoded.auth_time, 'number');
  assert.equal((await auth.verifyIdToken(up.body.idToken, true)).uid, up.body.localId);
  // The official verifier, pointed at Google, cannot accept an Aruma token: hence this library.
  await rejectsWith(getFirebaseAuth(app).verifyIdToken('not.a.token'), 'argument-error');
});

// AC-A5
test('the official createCustomToken signs in a client unchanged', async () => {
  for (const token of [
    await getFirebaseAuth(app).createCustomToken('custom-1', { plan: 'pro' }),
    await auth.createCustomToken('custom-1', { plan: 'pro' }),
  ]) {
    const res = await clientCall(server, '/identitytoolkit.googleapis.com/v1/accounts:signInWithCustomToken', { token, returnSecureToken: true });
    assert.equal(res.status, 200, JSON.stringify(res.body));
    const decoded = await auth.verifyIdToken(res.body.idToken, true);
    assert.equal(decoded.uid, 'custom-1');
    assert.equal(decoded.plan, 'pro');
    assert.equal(decoded.firebase.sign_in_provider, 'custom');
  }
});

// AC-A11
test('revoke, then refresh fails and checkRevoked reports id-token-revoked', async () => {
  const up = await signUp(server, 'revoke@example.com');
  await nextSecond();
  await auth.revokeRefreshTokens(up.body.localId);
  const r = await refresh(server, up.body.refreshToken);
  assert.equal(r.body.error?.message, 'TOKEN_EXPIRED');
  assert.equal((await auth.verifyIdToken(up.body.idToken)).uid, up.body.localId, 'the signature stays valid');
  await rejectsWith(auth.verifyIdToken(up.body.idToken, true), 'id-token-revoked');
  const user = await auth.getUser(up.body.localId);
  assert.ok(Date.parse(user.tokensValidAfterTime) > 0);
});

// AC-A12
test('a disabled user is refused until enabled again', async () => {
  const up = await signUp(server, 'disable@example.com');
  const user = await auth.updateUser(up.body.localId, { disabled: true });
  assert.equal(user.disabled, true);
  await rejectsWith(auth.verifyIdToken(up.body.idToken, true), 'user-disabled');
  assert.equal((await refresh(server, up.body.refreshToken)).body.error?.message, 'USER_DISABLED');
  await auth.updateUser(up.body.localId, { disabled: false });
  assert.equal((await refresh(server, up.body.refreshToken)).status, 200);
  await auth.verifyIdToken(up.body.idToken, true);
});

// AC-A13
test('custom claims reach the token at the next refresh, not before', async () => {
  const up = await signUp(server, 'claims@example.com');
  await auth.setCustomUserClaims(up.body.localId, { role: 'admin' });
  assert.equal((await auth.verifyIdToken(up.body.idToken)).role, undefined);
  const r = await refresh(server, up.body.refreshToken);
  assert.equal((await auth.verifyIdToken(r.body.id_token)).role, 'admin');
  assert.deepEqual((await auth.getUser(up.body.localId)).customClaims, { role: 'admin' });

  await rejectsWith(auth.setCustomUserClaims(up.body.localId, { sub: 'x' }), 'reserved-claim');
  await rejectsWith(auth.setCustomUserClaims(up.body.localId, { big: 'x'.repeat(1000) }), 'claims-too-large');
  await auth.setCustomUserClaims(up.body.localId, null);
  assert.equal((await auth.getUser(up.body.localId)).customClaims, undefined);
});

// AC-A14
test('user management returns official UserRecords and error codes', async () => {
  const created = await auth.createUser({ uid: 'admin-made', email: 'Made@Example.com', password: 'secret1', displayName: 'سارة', photoURL: 'https://example.com/p.png' });
  assert.ok(created instanceof UserRecord);
  assert.equal(created.uid, 'admin-made');
  assert.equal(created.email, 'Made@Example.com');
  assert.equal(created.passwordHash, undefined, 'the placeholder hash is hidden as firebase-admin hides it');
  assert.equal(created.providerData[0].providerId, 'password');
  assert.ok(created.metadata.creationTime);
  const json = created.toJSON();
  for (const k of ['uid', 'email', 'emailVerified', 'displayName', 'photoURL', 'disabled', 'metadata', 'providerData', 'tokensValidAfterTime']) {
    assert.ok(k in json, `toJSON lacks ${k}`);
  }

  await rejectsWith(auth.createUser({ email: 'made@example.com' }), 'email-already-exists');
  await rejectsWith(auth.createUser({ uid: 'admin-made' }), 'uid-already-exists');
  await rejectsWith(auth.createUser({ email: 'w@example.com', password: '1' }), 'invalid-password');
  await rejectsWith(auth.getUser('ghost'), 'user-not-found');
  await rejectsWith(auth.getUserByEmail('ghost@example.com'), 'user-not-found');
  await rejectsWith(auth.getUser(''), 'invalid-uid');

  assert.equal((await auth.getUserByEmail('made@example.com')).uid, 'admin-made');
  const many = await auth.getUsers([{ uid: 'admin-made' }, { email: 'MADE@example.com' }, { uid: 'ghost' }, { phoneNumber: '+15555550100' }]);
  assert.equal(many.users.length, 1);
  assert.deepEqual(many.notFound, [{ uid: 'ghost' }, { phoneNumber: '+15555550100' }]);

  const updated = await auth.updateUser('admin-made', { displayName: null, photoURL: 'https://example.com/q.png', emailVerified: true });
  assert.equal(updated.displayName, undefined);
  assert.equal(updated.photoURL, 'https://example.com/q.png');
  assert.equal(updated.emailVerified, true);
  await rejectsWith(auth.updateUser('ghost', { displayName: 'x' }), 'user-not-found');

  for (let i = 0; i < 3; i++) await auth.createUser({ uid: `page-${i}` });
  const seen = [];
  let pageToken;
  do {
    const page = await auth.listUsers(2, pageToken);
    assert.ok(page.users.length <= 2);
    seen.push(...page.users.map((u) => u.uid));
    pageToken = page.pageToken;
  } while (pageToken);
  assert.equal(new Set(seen).size, seen.length, 'a user was listed twice');
  assert.ok(['admin-made', 'page-0', 'page-1', 'page-2'].every((u) => seen.includes(u)));
  await rejectsWith(auth.listUsers(1001), 'argument-error');

  const del = await auth.deleteUsers(['page-0', 'page-1', 'ghost']);
  assert.deepEqual([del.successCount, del.failureCount, del.errors.length], [3, 0, 0]);
  await auth.deleteUser('page-2');
  await rejectsWith(auth.deleteUser('page-2'), 'user-not-found');
});

// AC-A20
test('session cookies: create, verify, revoke and duration limits', async () => {
  const up = await signUp(server, 'cookie@example.com');
  const cookie = await auth.createSessionCookie(up.body.idToken, { expiresIn: 60 * 60 * 1000 });
  const decoded = await auth.verifySessionCookie(cookie, true);
  assert.equal(decoded.uid, up.body.localId);
  assert.equal(decoded.iss, `${server.url}/session-issuers/${server.expected.projectId}`);
  assert.equal(decoded.exp - decoded.iat, 3600);
  await rejectsWith(auth.verifyIdToken(cookie), 'argument-error');
  await rejectsWith(auth.verifySessionCookie(up.body.idToken), 'argument-error');
  await rejectsWith(auth.createSessionCookie(up.body.idToken, { expiresIn: 60 * 1000 }), 'invalid-session-cookie-duration');
  await rejectsWith(auth.createSessionCookie(up.body.idToken, { expiresIn: 15 * 24 * 3600 * 1000 }), 'invalid-session-cookie-duration');
  await rejectsWith(auth.createSessionCookie('nope', { expiresIn: 3600 * 1000 }), 'invalid-id-token');

  await nextSecond();
  await auth.revokeRefreshTokens(up.body.localId);
  assert.equal((await auth.verifySessionCookie(cookie)).uid, up.body.localId);
  await rejectsWith(auth.verifySessionCookie(cookie, true), 'session-cookie-revoked');
  await rejectsWith(auth.createSessionCookie(up.body.idToken, { expiresIn: 3600 * 1000 }), 'id-token-expired');
});
