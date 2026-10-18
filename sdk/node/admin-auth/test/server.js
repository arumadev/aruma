// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

// Starts a real Aruma server for the live tests: test/smoke/genconfig -serve creates a fresh
// database, a project with apps and a service account key, then keeps serving. Needs Go and the
// local Postgres (ARUMA_TEST_DATABASE_URL). With ARUMA_TEST_DIR and ARUMA_URL set, an already
// running server is used instead.

import { execFileSync, spawn } from 'node:child_process';
import { mkdtempSync, readFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('../../../../', import.meta.url));

export async function startAruma() {
  if (process.env.ARUMA_TEST_DIR && process.env.ARUMA_URL) {
    return describe(process.env.ARUMA_URL, process.env.ARUMA_TEST_DIR, process.env.ARUMA_CLI_TOKEN, () => {});
  }
  const bin = join(root, '.cache', 'bin', 'genconfig');
  execFileSync('go', ['build', '-o', bin, './test/smoke/genconfig'], { cwd: root, stdio: 'inherit' });
  const dir = mkdtempSync(join(tmpdir(), 'aruma-admin-auth-'));
  const child = spawn(bin, ['-out', dir, '-serve'], { cwd: root, stdio: ['ignore', 'pipe', 'inherit'] });
  const exports = await new Promise((resolve, reject) => {
    let out = '';
    child.stdout.on('data', (chunk) => {
      out += chunk;
      if (out.includes('ARUMA_CLI_TOKEN=') && out.endsWith('\n')) {
        resolve(Object.fromEntries([...out.matchAll(/^export (\w+)=(.*)$/gm)].map((m) => [m[1], m[2]])));
      }
    });
    child.on('exit', (code) => reject(new Error(`genconfig exited with ${code}`)));
  });
  const stop = () => new Promise((resolve) => {
    if (child.exitCode !== null) {
      resolve();
      return;
    }
    child.removeAllListeners('exit');
    child.on('exit', () => resolve());
    child.kill('SIGTERM'); // genconfig drops its database on the way out
  });
  return describe(exports.ARUMA_URL, dir, exports.ARUMA_CLI_TOKEN, stop);
}

function describe(url, dir, cliToken, stop) {
  const expected = JSON.parse(readFileSync(join(dir, 'expected.json'), 'utf8'));
  return { url, dir, cliToken, expected, keyPath: join(dir, 'service-account.json'), stop };
}

/** A client SDK call, as the web SDK makes it with the project's API key. */
export async function clientCall(server, path, body) {
  const res = await fetch(`${server.url}${path}?key=${server.expected.web.apiKey}`, {
    method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(body),
  });
  return { status: res.status, body: await res.json() };
}

export const signUp = (server, email, password = 'secret1') =>
  clientCall(server, '/identitytoolkit.googleapis.com/v1/accounts:signUp', { email, password, returnSecureToken: true });

export const refresh = (server, refreshToken) =>
  clientCall(server, '/securetoken.googleapis.com/v1/token', { grantType: 'refresh_token', refreshToken });

/** Waits for a new second: validSince has one-second resolution, as in Firebase. */
export const nextSecond = () => new Promise((r) => setTimeout(r, 1010 - (Date.now() % 1000)));
