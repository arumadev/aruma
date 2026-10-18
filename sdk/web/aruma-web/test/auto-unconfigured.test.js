// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

import { test } from 'node:test';
import assert from 'node:assert/strict';

test('@aruma/web/auto without configuration leaves fetch alone', async () => {
  const original = globalThis.fetch;
  delete globalThis.__ARUMA__;
  await import('../src/auto.js');
  const { status } = await import('../src/index.js');
  assert.equal(globalThis.fetch, original);
  assert.equal(status(), null);
});

test('@aruma/web/auto with a refused URL does not throw and fails Auth requests closed', async () => {
  const calls = [];
  globalThis.fetch = async (input) => {
    calls.push(input);
    return new Response('{}');
  };
  globalThis.__ARUMA__ = 'http://aruma.example.com';
  const saved = console.error;
  const errors = [];
  console.error = (...a) => errors.push(a.join(' '));
  try {
    await import('../src/auto.js?refused');
  } finally {
    console.error = saved;
  }
  assert.equal(errors.length, 1);
  await assert.rejects(fetch('https://securetoken.googleapis.com/v1/token'));
  assert.deepEqual(calls, []);
});
