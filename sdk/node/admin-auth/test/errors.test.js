import { test } from 'node:test';
import assert from 'node:assert/strict';
import { fromResponse } from '../lib/errors.js';

// Official FirebaseAuthError messages, as firebase-admin gives them for the same server codes.
test('a server code without detail gets the official message', () => {
  const e = fromResponse(400, { error: { message: 'EMAIL_EXISTS' } });
  assert.equal(e.code, 'auth/email-already-exists');
  assert.equal(e.message, 'The email address is already in use by another account.');
  assert.equal(fromResponse(400, { error: { message: 'USER_NOT_FOUND' } }).message, 'There is no user record corresponding to the provided identifier.');
});

test('a detail from the server is kept', () => {
  assert.equal(fromResponse(400, { error: { message: 'WEAK_PASSWORD : Password should be at least 6 characters' } }).message, 'Password should be at least 6 characters');
});
