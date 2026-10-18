// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

// The side-effect entry reads globalThis.__ARUMA__. Each test file runs in its own process, so
// this one may change the real globalThis.fetch.
import { test } from 'node:test';
import assert from 'node:assert/strict';

test('@aruma/web/auto installs from globalThis.__ARUMA__', async () => {
  const calls = [];
  globalThis.fetch = async (input) => {
    calls.push(input);
    return new Response('{}');
  };
  globalThis.__ARUMA__ = { url: 'http://127.0.0.1:8093' };
  await import('../src/auto.js');
  const { status } = await import('../src/index.js');
  assert.deepEqual(status(), { url: 'http://127.0.0.1:8093' });
  await fetch('https://identitytoolkit.googleapis.com/v1/accounts:signUp?key=k', { method: 'POST' });
  await fetch('https://example.com/');
  assert.deepEqual(calls, ['http://127.0.0.1:8093/identitytoolkit.googleapis.com/v1/accounts:signUp?key=k', 'https://example.com/']);
});
