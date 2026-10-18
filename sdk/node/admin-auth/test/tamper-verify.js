// Puts ID tokens to this library for the repository's tamper suite
// (test/acceptance/auth_tamper_test.go). It reads one JSON request per line on stdin,
//
//   {"url": "<Aruma server>", "key": "<service account key file JSON>", "token": "…", "checkRevoked": true}
//
// and answers one line per request: {"ok": true} or {"ok": false, "error": "<auth/… code>"}.
// Apps are kept per server and key, as an application keeps its one app.
import { createInterface } from 'node:readline';
import { initializeApp, cert } from 'firebase-admin/app';
import { getAuth } from '../index.js';

const auths = new Map();

function authFor({ url, key }) {
  const id = `${url}|${key}`;
  if (!auths.has(id)) {
    const sa = JSON.parse(key);
    const app = initializeApp({ credential: cert(sa), projectId: sa.project_id }, `tamper-${auths.size}`);
    auths.set(id, getAuth(app, { url }));
  }
  return auths.get(id);
}

for await (const line of createInterface({ input: process.stdin })) {
  let res;
  try {
    const req = JSON.parse(line);
    await authFor(req).verifyIdToken(req.token, req.checkRevoked);
    res = { ok: true };
  } catch (e) {
    res = { ok: false, error: e.code ?? String(e) };
  }
  process.stdout.write(`${JSON.stringify(res)}\n`);
}
