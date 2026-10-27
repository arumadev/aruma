// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

// SR3-10, end to end through a real bundler: `vite build` of an app that uses the official firebase
// package's Firestore (the full SDK with WebChannel, and the lite SDK with fetch) leaves no
// firestore.googleapis.com in any file it emits, and an app on Aruma Auth without the Firestore
// rewrite does not build. Needs the devDependencies firebase and vite.
import { test, describe, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { build } from 'vite';
import aruma from '../src/vite.js';

const here = dirname(fileURLToPath(import.meta.url));
const GOOGLE = ['firestore', 'googleapis', 'com'].join('.');

let root;

before(() => {
  // Under the package, so that `firebase` resolves from its node_modules.
  root = mkdtempSync(join(here, '..', '.build-check-'));
  mkdirSync(join(root, 'src'));
  writeFileSync(join(root, 'index.html'), '<!doctype html><script type="module" src="/src/main.js"></script>');
  writeFileSync(
    join(root, 'src', 'main.js'),
    `import { initializeApp } from 'firebase/app';
import { getFirestore, doc, onSnapshot, setDoc } from 'firebase/firestore';
import { getFirestore as getLite, doc as liteDoc, getDoc } from 'firebase/firestore/lite';
const app = initializeApp({ projectId: 'p', apiKey: 'k' });
const db = getFirestore(app);
onSnapshot(doc(db, 'a/b'), (s) => console.log(s.data()));
setDoc(doc(db, 'a/c'), { x: 1 });
getDoc(liteDoc(getLite(app), 'a/b')).then((s) => console.log(s.data()));
`,
  );
});

after(() => rmSync(root, { recursive: true, force: true }));

async function bundle(plugins) {
  const out = await build({ root, logLevel: 'silent', configFile: false, plugins: [plugins].flat(), build: { write: false, minify: true } });
  return (Array.isArray(out) ? out : [out]).flatMap((o) => o.output).filter((f) => /\.(?:m?js|html)$/.test(f.fileName));
}

const code = (f) => (f.type === 'chunk' ? f.code : String(f.source));

describe('vite build, SR3-10', () => {
  test('firestore: true, https: the full and the lite SDK name Aruma and no file names Google', async () => {
    const files = await bundle(aruma({ url: 'https://aruma.example.com', firestore: true }));
    const all = files.map(code).join('\n');
    assert.ok(all.includes('google.firestore.v1'), 'the full SDK (WebChannel) is in the bundle');
    assert.match(all, /[`"']aruma\.example\.com[`"'],\w+=!0/, 'Aruma is the default host, ssl on');
    for (const f of files) assert.ok(!code(f).includes(GOOGLE), `${f.fileName} names Google's Firestore host`);
  });

  test('firestore: true, http on loopback: ssl is off', async () => {
    const files = await bundle(aruma({ url: 'http://127.0.0.1:9420', firestore: true }));
    const all = files.map(code).join('\n');
    assert.ok(/[`"']127\.0\.0\.1:9420[`"'],\w+=!1/.test(all), 'the host literal is followed by ssl = false');
    for (const f of files) assert.ok(!code(f).includes(GOOGLE), f.fileName);
  });

  test('Aruma Auth without the Firestore rewrite does not build', async () => {
    await assert.rejects(bundle(aruma({ url: 'https://aruma.example.com' })), /@firebase\/firestore.*Firestore is not enabled.*firestore: true/s);
  });

  test('the control: without the plugin the bundle holds Google\'s host', async () => {
    const files = await bundle([]);
    assert.ok(files.some((f) => code(f).includes(GOOGLE)));
  });
});
