// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

// Compiled by `npm run typecheck`, never run: the declarations must give the official types.

import { initializeApp, cert } from 'firebase-admin/app';
import type { DecodedIdToken, UserRecord, ListUsersResult, GetUsersResult, DeleteUsersResult } from 'firebase-admin/auth';
import { getAuth, type ArumaAuth } from '../index.js';

export async function usage(): Promise<void> {
  const app = initializeApp({ credential: cert('service-account.json') });
  const auth: ArumaAuth = getAuth(app, { url: 'https://aruma.example.com' });
  const decoded: DecodedIdToken = await auth.verifyIdToken('token', true);
  const cookie: DecodedIdToken = await auth.verifySessionCookie(await auth.createSessionCookie('t', { expiresIn: 300_000 }));
  const user: UserRecord = await auth.createUser({ uid: decoded.uid, email: cookie.email });
  const updated: UserRecord = await auth.updateUser(user.uid, { displayName: null, disabled: true });
  const byEmail: UserRecord = await auth.getUserByEmail('a@example.com');
  const many: GetUsersResult = await auth.getUsers([{ uid: updated.uid }, { email: byEmail.email ?? '' }]);
  const page: ListUsersResult = await auth.listUsers(10, many.users[0]?.uid);
  const deleted: DeleteUsersResult = await auth.deleteUsers(page.users.map((u) => u.uid));
  await auth.setCustomUserClaims(user.uid, deleted.failureCount ? null : { role: 'admin' });
  await auth.revokeRefreshTokens((await auth.getUser(user.uid)).uid);
  await auth.deleteUser(user.uid);
  const token: string = await auth.createCustomToken(user.uid, { plan: 'pro' });
  void token;
  // @ts-expect-error verifyIdToken takes a string
  await auth.verifyIdToken(42);
}
