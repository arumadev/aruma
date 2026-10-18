import { generateKeyPairSync } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { initializeApp, cert, deleteApp } from 'firebase-admin/app';
import { getAuth } from 'firebase-admin/auth';
import { test, expect, call, value, rest, mailboxCode, arumaURL, expected, serviceAccountPath } from './fixtures.mjs';

// AC-A1, AC-A2, AC-A5, AC-A6, AC-A7 and the web half of AC-A3 with the official firebase package: every flow
// through the SDK's public API, with zero requests to Google (the network fixture).

const email = (major, name) => `${name}-${major}@example.com`;

// The official firebase-admin, as a developer's server uses it. With the emulator variable set
// it would mint unsigned tokens, so it must not be.
function adminAuth(name, credential = cert(serviceAccountPath)) {
  delete process.env.FIREBASE_AUTH_EMULATOR_HOST;
  const app = initializeApp({ credential, projectId: expected.projectId }, name);
  return { auth: getAuth(app), close: () => deleteApp(app) };
}

test('email and password: sign up, out, in, refresh, profile, password, delete', async ({ app, firebaseMajor, network }) => {
  const addr = email(firebaseMajor, 'life');
  const uid = await value(app, (auth, sdk, a) => sdk.createUserWithEmailAndPassword(auth, a.addr, 'secret1').then((c) => c.user.uid), { addr });
  expect(uid).toHaveLength(28);
  expect(network.aruma).toContain('/identitytoolkit.googleapis.com/v1/accounts:signUp');

  await value(app, (auth, sdk) => sdk.signOut(auth));
  expect(await value(app, (auth) => auth.currentUser)).toBeNull();

  const again = await value(app, (auth, sdk, a) => sdk.signInWithEmailAndPassword(auth, a.addr, 'secret1').then((c) => c.user.uid), { addr });
  expect(again).toBe(uid);

  // A forced refresh goes to the Secure Token endpoint and yields a token Aruma issued.
  const before = network.aruma.length;
  const result = await value(app, async (auth) => {
    await auth.currentUser.getIdToken(true);
    const r = await auth.currentUser.getIdTokenResult();
    return { iss: r.claims.iss, aud: r.claims.aud, provider: r.signInProvider, sub: r.claims.sub };
  });
  expect(network.aruma.slice(before)).toContain('/securetoken.googleapis.com/v1/token');
  expect(result).toEqual({ iss: `${arumaURL}/issuers/${expected.projectId}`, aud: expected.projectId, provider: 'password', sub: uid });

  const profile = await value(app, async (auth, sdk) => {
    await sdk.updateProfile(auth.currentUser, { displayName: 'سارة', photoURL: 'https://example.com/s.png' });
    await auth.currentUser.reload();
    return { name: auth.currentUser.displayName, photo: auth.currentUser.photoURL };
  });
  expect(profile).toEqual({ name: 'سارة', photo: 'https://example.com/s.png' });

  // A password change after a recent sign-in keeps this session and revokes the others.
  await value(app, async (auth, sdk, a) => {
    await sdk.reauthenticateWithCredential(auth.currentUser, sdk.EmailAuthProvider.credential(a.addr, 'secret1'));
    await sdk.updatePassword(auth.currentUser, 'secret2');
    await auth.currentUser.getIdToken(true);
  }, { addr });
  await value(app, (auth, sdk) => sdk.signOut(auth));
  expect(await call(app, (auth, sdk, a) => sdk.signInWithEmailAndPassword(auth, a.addr, 'secret1'), { addr })).toEqual({ code: 'auth/invalid-credential' });
  await value(app, (auth, sdk, a) => sdk.signInWithEmailAndPassword(auth, a.addr, 'secret2'), { addr });

  await value(app, (auth, sdk) => sdk.deleteUser(auth.currentUser));
  expect(await value(app, (auth) => auth.currentUser)).toBeNull();
  expect(await call(app, (auth, sdk, a) => sdk.signInWithEmailAndPassword(auth, a.addr, 'secret2'), { addr })).toEqual({ code: 'auth/invalid-credential' });
  expect(await value(app, () => window.e2e.authStates)).toEqual([null, uid, null, uid, null, uid, null]);
});

test('anonymous, then linked to email and password, keeps its uid (AC-A2)', async ({ app, firebaseMajor }) => {
  const anon = await value(app, (auth, sdk) => sdk.signInAnonymously(auth).then((c) => ({ uid: c.user.uid, anonymous: c.user.isAnonymous })));
  expect(anon.anonymous).toBe(true);
  const linked = await value(app, async (auth, sdk, a) => {
    const c = await sdk.linkWithCredential(auth.currentUser, sdk.EmailAuthProvider.credential(a.addr, 'secret1'));
    const r = await c.user.getIdTokenResult();
    return { uid: c.user.uid, anonymous: c.user.isAnonymous, providers: c.user.providerData.map((p) => p.providerId), provider: r.signInProvider };
  }, { addr: email(firebaseMajor, 'anon') });
  expect(linked).toEqual({ uid: anon.uid, anonymous: false, providers: ['password'], provider: 'password' });
});

test('custom token from the official createCustomToken (AC-A5)', async ({ app, firebaseMajor }) => {
  const admin = adminAuth(`custom-${firebaseMajor}`);
  const uid = `custom-${firebaseMajor}`;
  const token = await admin.auth.createCustomToken(uid, { tier: 'gold' });
  await admin.close();
  expect(JSON.parse(Buffer.from(token.split('.')[0], 'base64url')).alg).toBe('RS256');

  const signedIn = await value(app, async (auth, sdk, a) => {
    const c = await sdk.signInWithCustomToken(auth, a.token);
    const r = await c.user.getIdTokenResult();
    return { uid: c.user.uid, tier: r.claims.tier, provider: r.signInProvider };
  }, { token });
  expect(signedIn).toEqual({ uid, tier: 'gold', provider: 'custom' });
});

test('revocation signs the user out with auth/user-token-expired', async ({ app, firebaseMajor }) => {
  const addr = email(firebaseMajor, 'revoked');
  const uid = await value(app, (auth, sdk, a) => sdk.createUserWithEmailAndPassword(auth, a.addr, 'secret1').then((c) => c.user.uid), { addr });

  // The same user changes the password on another device, which revokes every other session.
  // Revocation has second precision, as Firebase's validSince: let the browser's session age.
  await new Promise((resolve) => setTimeout(resolve, 1100));
  const other = await rest('accounts:signInWithPassword', { email: addr, password: 'secret1', returnSecureToken: true });
  await rest('accounts:update', { idToken: other.idToken, password: 'secret2', returnSecureToken: true });

  expect(await call(app, (auth) => auth.currentUser.getIdToken(true))).toEqual({ code: 'auth/user-token-expired' });
  await expect.poll(() => value(app, (auth) => auth.currentUser)).toBeNull();
  expect(await value(app, () => window.e2e.authStates)).toEqual([null, uid, null]);
});

test('error codes reach the app as the expected auth/* codes', async ({ app, firebaseMajor }) => {
  const addr = email(firebaseMajor, 'errors');
  await value(app, (auth, sdk, a) => sdk.createUserWithEmailAndPassword(auth, a.addr, 'secret1'), { addr });
  await value(app, (auth, sdk) => sdk.signOut(auth));

  const cases = [
    ['email-already-in-use', (auth, sdk, a) => sdk.createUserWithEmailAndPassword(auth, a.addr, 'secret1')],
    ['weak-password', (auth, sdk, a) => sdk.createUserWithEmailAndPassword(auth, `weak-${a.addr}`, '123')],
    ['invalid-email', (auth, sdk) => sdk.createUserWithEmailAndPassword(auth, 'not-an-email', 'secret1')],
    ['missing-password', (auth, sdk, a) => sdk.createUserWithEmailAndPassword(auth, `nopw-${a.addr}`, '')],
    ['invalid-credential', (auth, sdk, a) => sdk.signInWithEmailAndPassword(auth, a.addr, 'wrong!!')],
    ['invalid-credential', (auth, sdk) => sdk.signInWithEmailAndPassword(auth, 'nobody@example.com', 'secret1')],
    ['invalid-custom-token', (auth, sdk) => sdk.signInWithCustomToken(auth, 'not.a.token')],
  ];
  for (const [code, fn] of cases) {
    expect(await call(app, fn, { addr }), code).toEqual({ code: `auth/${code}` });
  }

  // createCustomToken with a key Aruma never issued, under the real account's name.
  const sa = JSON.parse(readFileSync(serviceAccountPath, 'utf8'));
  const { privateKey } = generateKeyPairSync('rsa', { modulusLength: 2048 });
  const rogue = adminAuth(`rogue-${firebaseMajor}`, cert({ projectId: sa.project_id, clientEmail: sa.client_email, privateKey: privateKey.export({ type: 'pkcs8', format: 'pem' }) }));
  const forged = await rogue.auth.createCustomToken('intruder');
  await rogue.close();
  expect(await call(app, (auth, sdk, a) => sdk.signInWithCustomToken(auth, a.forged), { forged })).toEqual({ code: 'auth/invalid-custom-token' });
  expect(await value(app, (auth) => auth.currentUser)).toBeNull();
});

test('email verification through the dev mailbox (AC-A6)', async ({ app, firebaseMajor }) => {
  const addr = email(firebaseMajor, 'verify');
  await value(app, (auth, sdk, a) => sdk.createUserWithEmailAndPassword(auth, a.addr, 'secret1'), { addr });
  await value(app, (auth, sdk) => sdk.sendEmailVerification(auth.currentUser, { url: 'http://localhost/done' }));
  const code = await mailboxCode(addr);

  const info = await value(app, (auth, sdk, a) => sdk.checkActionCode(auth, a.code).then((i) => ({ operation: i.operation, email: i.data.email })), { code });
  expect(info).toEqual({ operation: 'VERIFY_EMAIL', email: addr });
  const verified = await value(app, async (auth, sdk, a) => {
    await sdk.applyActionCode(auth, a.code);
    await auth.currentUser.reload();
    await auth.currentUser.getIdToken(true);
    return { user: auth.currentUser.emailVerified, claim: (await auth.currentUser.getIdTokenResult()).claims.email_verified };
  }, { code });
  expect(verified).toEqual({ user: true, claim: true });
  // A code works once.
  expect(await call(app, (auth, sdk, a) => sdk.applyActionCode(auth, a.code), { code })).toEqual({ code: 'auth/invalid-action-code' });
});

test('password reset through the dev mailbox (AC-A7)', async ({ app, firebaseMajor }) => {
  const addr = email(firebaseMajor, 'reset');
  await value(app, (auth, sdk, a) => sdk.createUserWithEmailAndPassword(auth, a.addr, 'secret1'), { addr });
  await value(app, (auth, sdk) => sdk.signOut(auth));
  await value(app, (auth, sdk, a) => sdk.sendPasswordResetEmail(auth, a.addr), { addr });
  // Under enumeration protection an unknown address succeeds the same way.
  await value(app, (auth, sdk) => sdk.sendPasswordResetEmail(auth, 'nobody@example.com'));
  const code = await mailboxCode(addr);

  expect(await value(app, (auth, sdk, a) => sdk.verifyPasswordResetCode(auth, a.code), { code })).toBe(addr);
  expect(await call(app, (auth, sdk, a) => sdk.confirmPasswordReset(auth, a.code, '123'), { code })).toEqual({ code: 'auth/weak-password' });
  await value(app, (auth, sdk, a) => sdk.confirmPasswordReset(auth, a.code, 'secret2'), { code });
  expect(await call(app, (auth, sdk, a) => sdk.confirmPasswordReset(auth, a.code, 'secret3'), { code })).toEqual({ code: 'auth/invalid-action-code' });

  expect(await call(app, (auth, sdk, a) => sdk.signInWithEmailAndPassword(auth, a.addr, 'secret1'), { addr })).toEqual({ code: 'auth/invalid-credential' });
  const user = await value(app, (auth, sdk, a) => sdk.signInWithEmailAndPassword(auth, a.addr, 'secret2').then((c) => ({ email: c.user.email, verified: c.user.emailVerified })), { addr });
  expect(user).toEqual({ email: addr, verified: true });
});

test('email change after verifying the new address (verifyBeforeUpdateEmail)', async ({ app, firebaseMajor }) => {
  const addr = email(firebaseMajor, 'move');
  const next = email(firebaseMajor, 'moved');
  await value(app, (auth, sdk, a) => sdk.createUserWithEmailAndPassword(auth, a.addr, 'secret1'), { addr });
  await value(app, (auth, sdk, a) => sdk.verifyBeforeUpdateEmail(auth.currentUser, a.next), { next });
  const code = await mailboxCode(next);
  const info = await value(app, (auth, sdk, a) => sdk.checkActionCode(auth, a.code).then((i) => ({ operation: i.operation, email: i.data.email, previous: i.data.previousEmail })), { code });
  // The SDK reports the new address as email and the current one as previousEmail.
  expect(info).toEqual({ operation: 'VERIFY_AND_CHANGE_EMAIL', email: next, previous: addr });
  await value(app, (auth, sdk, a) => sdk.applyActionCode(auth, a.code), { code });
  await value(app, (auth, sdk) => sdk.signOut(auth));
  const moved = await value(app, (auth, sdk, a) => sdk.signInWithEmailAndPassword(auth, a.next, 'secret1').then((c) => ({ email: c.user.email, verified: c.user.emailVerified })), { next });
  expect(moved).toEqual({ email: next, verified: true });
});

// Production mode with the fetch redirect: the SDK looks fetch up on every request, so installing
// @aruma/web after firebase/auth was imported and used works the same as installing it first.
test('production mode: installed after firebase/auth, the next request still reaches Aruma', async ({ app, mode, network }) => {
  test.skip(mode !== 'production', 'only the fetch redirect is installed at run time');
  const result = await value(app, async () => {
    // The same global state any copy of @aruma/web uses.
    const aruma = await import('/@id/@aruma/web');
    const before = aruma.status();
    aruma.uninstall();
    const off = aruma.status();
    aruma.install(before); // { url }: now after the SDK was imported and initialised
    const c = await window.e2e.sdk.signInAnonymously(window.e2e.auth);
    return { before, off, after: aruma.status(), anonymous: c.user.isAnonymous };
  });
  expect(result).toEqual({ before: { url: arumaURL }, off: null, after: { url: arumaURL }, anonymous: true });
  expect(network.aruma).toContain('/identitytoolkit.googleapis.com/v1/accounts:signUp');
});
