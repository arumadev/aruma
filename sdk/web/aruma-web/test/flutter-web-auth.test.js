// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

// `aruma-web flutter-web` with the `auth` product: firebase-auth.js (the CDN build firebase_core_web
// would load from www.gstatic.com) rewritten for Aruma and loaded before flutter_bootstrap.js.
import { test, describe } from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync, spawnSync } from 'node:child_process';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { authBase, rewriteAuthFile } from '../src/auth.js';
import { DEFAULT_PRODUCTS, flutterWeb, patchIndex, webPlugins } from '../src/flutter-web.js';
import { CDN_SNIPPET } from './storage-fixtures.js';

const CLI = fileURLToPath(new URL('../src/cli.js', import.meta.url));

// web/index.html as `flutter create` writes it (3.44).
const INDEX = `<!DOCTYPE html>
<html>
<head>
  <base href="$FLUTTER_BASE_HREF">
  <title>app</title>
</head>
<body>
  <script src="flutter_bootstrap.js" async></script>
</body>
</html>
`;

// The parts of firebase-auth.js 13.1.0 (and 12.19.0, written the same way) the rule reads: the
// eid map keyed on the API host, the URL builder, and the defaults of _getAuthConfig.
const AUTH_CDN =
  'import{_registerComponent as e}from"https://www.gstatic.com/firebasejs/13.1.0/firebase-app.js";' +
  'const Z=new Map([["identitytoolkit.googleapis.com","p"],["staging-identitytoolkit.sandbox.googleapis.com","s"],["test-identitytoolkit.sandbox.googleapis.com","t"]]);' +
  'async function _getFinalTarget(e,t,r,n){const i=`${t}${r}?${n}`;return e.config.emulator?_emulatorUrl(e.config,i):`${e.config.apiScheme}://${i}`}' +
  'const c={apiKey:o,authDomain:a,clientPlatform:r,apiHost:"identitytoolkit.googleapis.com",tokenApiHost:"securetoken.googleapis.com",apiScheme:"https",sdkClientVersion:_getClientVersion(r)};';

// A Flutter project with firebase_core_web pinning 13.1.0, the FlutterFire web plugins named in
// `plugins`, and a firebase npm package with the CDN builds of Auth and Storage.
function project({ plugins = ['firebase_auth_web', 'firebase_storage_web'], auth = AUTH_CDN, index = INDEX } = {}) {
  const dir = mkdtempSync(join(tmpdir(), 'aruma-flutter-auth-'));
  const core = join(dir, 'pub', 'firebase_core_web-4.1.0');
  mkdirSync(join(core, 'lib', 'src'), { recursive: true });
  writeFileSync(join(core, 'lib', 'src', 'firebase_sdk_version.dart'), "const String supportedFirebaseJsSdkVersion = '13.1.0';\n");
  mkdirSync(join(dir, '.dart_tool'));
  const packages = [{ name: 'firebase_core_web', rootUri: pathToFileURL(core).href, packageUri: 'lib/' }, ...plugins.map((name) => ({ name, rootUri: '../pub/x', packageUri: 'lib/' }))];
  writeFileSync(join(dir, '.dart_tool', 'package_config.json'), JSON.stringify({ packages }));
  mkdirSync(join(dir, 'web'));
  writeFileSync(join(dir, 'web', 'index.html'), index);
  const fb = join(dir, 'node_modules', 'firebase');
  mkdirSync(fb, { recursive: true });
  writeFileSync(join(fb, 'package.json'), JSON.stringify({ name: 'firebase', version: '13.1.0' }));
  writeFileSync(join(fb, 'firebase-auth.js'), auth);
  writeFileSync(join(fb, 'firebase-storage.js'), `import{x}from"https://www.gstatic.com/firebasejs/13.1.0/firebase-app.js";${CDN_SNIPPET}`);
  writeFileSync(join(fb, 'firebase-firestore.js'), 'import{x}from"https://www.gstatic.com/firebasejs/13.1.0/firebase-app.js";const Or="firestore.googleapis.com",Lr=!0;');
  return dir;
}

function withProject(options, fn) {
  const dir = project(options);
  try {
    fn(dir);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

const read = (...p) => readFileSync(join(...p), 'utf8');
const blocks = (html) => html.split('<!-- @aruma/web flutter-web:').length - 1;

describe('flutter-web: auth', () => {
  test('the default products are auth and storage', () => {
    assert.deepEqual(DEFAULT_PRODUCTS, ['auth', 'storage']);
    withProject({}, (dir) => {
      const r = flutterWeb({ url: 'http://127.0.0.1:9420', project: dir });
      assert.deepEqual(r.products, ['auth', 'storage']);
      assert.deepEqual(r.files.map((f) => f.split(/[\\/]/).pop()), ['firebase-auth-13.1.0.js', 'firebase-storage-13.1.0.js']);
      // `file` stays Storage's, as before Auth was a product.
      assert.match(r.file, /firebase-storage-13\.1\.0\.js$/);
      assert.deepEqual(r.warnings, []);
    });
  });

  test('firebase-auth.js: both Auth hosts become paths on Aruma, and http its scheme', () => {
    withProject({}, (dir) => {
      const r = flutterWeb({ url: 'http://127.0.0.1:9420', project: dir, products: ['auth', 'storage'] });
      const js = read(r.files[0]);
      assert.match(js, /^\/\* firebase-auth\.js 13\.1\.0 of the firebase npm package \(Apache-2\.0, Google LLC\), modified by @aruma\/web flutter-web: its Auth hosts are 127\.0\.0\.1:9420\/identitytoolkit\.googleapis\.com and 127\.0\.0\.1:9420\/securetoken\.googleapis\.com over http\. \*\//);
      assert.match(js, /apiHost:"127\.0\.0\.1:9420\/identitytoolkit\.googleapis\.com",tokenApiHost:"127\.0\.0\.1:9420\/securetoken\.googleapis\.com",apiScheme:"http"/);
      // The eid map keys on the same literal and follows it; the sandbox hosts are not Google's production ones.
      assert.match(js, /\["127\.0\.0\.1:9420\/identitytoolkit\.googleapis\.com","p"\],\["staging-identitytoolkit\.sandbox\.googleapis\.com","s"\]/);
      assert.match(js, /from"https:\/\/www\.gstatic\.com\/firebasejs\/13\.1\.0\/firebase-app\.js"/);
      assert.doesNotMatch(js, /["'`](?:identitytoolkit|securetoken)\.googleapis\.com/);
      // https and a path prefix: the scheme stays, the prefix comes before the Google host.
      flutterWeb({ url: 'https://aruma.example.com', project: dir, products: ['auth', 'storage'] });
      assert.match(read(r.files[0]), /apiHost:"aruma\.example\.com\/identitytoolkit\.googleapis\.com",tokenApiHost:"aruma\.example\.com\/securetoken\.googleapis\.com",apiScheme:"https"/);
    });
    // Auth alone may have a path prefix (Storage and Firestore may not).
    withProject({ plugins: ['firebase_auth_web'] }, (dir) => {
      const r = flutterWeb({ url: 'https://aruma.example.com/base', project: dir, products: ['auth'] });
      assert.match(read(r.file), /apiHost:"aruma\.example\.com\/base\/identitytoolkit\.googleapis\.com",tokenApiHost:"aruma\.example\.com\/base\/securetoken\.googleapis\.com"/);
      assert.equal(r.host, 'aruma.example.com');
      assert.throws(() => flutterWeb({ url: 'https://aruma.example.com/base', project: dir }), /Storage must be the root of its host/);
    });
  });

  test('index.html: Auth is ignored and set first, before flutter_bootstrap.js, once', () => {
    withProject({}, (dir) => {
      const r = flutterWeb({ url: 'http://127.0.0.1:9420', project: dir });
      const html = read(r.index);
      assert.doesNotMatch(html, /<script src="flutter_bootstrap\.js"/);
      assert.match(html, /<!-- @aruma\/web flutter-web: Firebase Auth and Storage on Aruma\. Generated;/);
      assert.match(html, /flutterfire_ignore_scripts = \[\.\.\.\(window\.flutterfire_ignore_scripts \?\? \[\]\), 'auth', 'storage'\];/);
      const auth = html.indexOf("window.firebase_auth = await import('./aruma/firebase-auth-13.1.0.js');");
      const storage = html.indexOf("window.firebase_storage = await import('./aruma/firebase-storage-13.1.0.js');");
      const bootstrap = html.indexOf("bootstrap.src = 'flutter_bootstrap.js'");
      assert.ok(auth > 0 && auth < storage && storage < bootstrap, html);
      assert.match(html, /bootstrap\.async = true;/);
      // Twice: nothing more changes, in index.html or in web/aruma/.
      const files = r.files.map((f) => read(f));
      const again = flutterWeb({ url: 'http://127.0.0.1:9420', project: dir });
      assert.equal(read(r.index), html);
      assert.deepEqual(again.files.map((f) => read(f)), files);
      assert.deepEqual(again.removed, []);
      assert.equal(blocks(html), 1);
    });
  });

  test('a run without auth takes it out again, with a warning; a run with it puts it back', () => {
    withProject({}, (dir) => {
      const first = flutterWeb({ url: 'http://127.0.0.1:9420', project: dir });
      const html = read(first.index);
      const off = flutterWeb({ url: 'http://127.0.0.1:9420', project: dir, products: ['storage'] });
      const without = read(off.index);
      assert.equal(blocks(without), 1);
      assert.doesNotMatch(without, /firebase_auth|'auth'/);
      assert.match(without, /Firebase Storage on Aruma/);
      assert.equal(existsSync(first.files[0]), false);
      assert.deepEqual(off.removed, [first.files[0]]);
      assert.equal(off.warnings.length, 1);
      assert.match(off.warnings[0], /uses firebase_auth on the web .* sign-up and sign-in send the user's email and password to identitytoolkit\.googleapis\.com/);
      // Back on: the same page as the first run.
      flutterWeb({ url: 'http://127.0.0.1:9420', project: dir });
      assert.equal(read(first.index), html);
      assert.equal(existsSync(first.files[0]), true);
    });
    // A project without firebase_auth: no warning.
    withProject({ plugins: ['firebase_storage_web'] }, (dir) => {
      assert.deepEqual(flutterWeb({ url: 'http://127.0.0.1:9420', project: dir, products: ['storage'] }).warnings, []);
    });
  });

  test('a block written before auth was a product is replaced, and files of another version are removed', () => {
    const old = `<!-- @aruma/web flutter-web: Firebase Storage on Aruma. Generated; run \`npx aruma-web flutter-web\` again to change it. -->
<script type="module">
  window.flutterfire_ignore_scripts = [...(window.flutterfire_ignore_scripts ?? []), 'storage'];
  window.firebase_storage = await import('./aruma/firebase-storage-13.0.0.js');
  const bootstrap = document.createElement('script');
  bootstrap.src = 'flutter_bootstrap.js';
  bootstrap.async = false;
  document.body.append(bootstrap);
</script>
<!-- /@aruma/web flutter-web -->`;
    withProject({ index: INDEX.replace('<script src="flutter_bootstrap.js" async></script>', old) }, (dir) => {
      mkdirSync(join(dir, 'web', 'aruma'));
      writeFileSync(join(dir, 'web', 'aruma', 'firebase-storage-13.0.0.js'), 'old');
      writeFileSync(join(dir, 'web', 'aruma', 'mine.js'), 'not the tool\'s');
      const r = flutterWeb({ url: 'http://127.0.0.1:9420', project: dir });
      const html = read(r.index);
      assert.equal(blocks(html), 1);
      assert.match(html, /'auth', 'storage'\]/);
      assert.match(html, /bootstrap\.async = false;/);
      assert.doesNotMatch(html, /13\.0\.0/);
      assert.equal(existsSync(join(dir, 'web', 'aruma', 'firebase-storage-13.0.0.js')), false);
      assert.equal(read(dir, 'web', 'aruma', 'mine.js'), 'not the tool\'s');
    });
  });

  test('with Auth on Aruma, Storage and Firestore the project uses must be on Aruma too', () => {
    withProject({ plugins: ['firebase_auth_web', 'firebase_storage_web', 'cloud_firestore_web'] }, (dir) => {
      assert.throws(() => flutterWeb({ url: 'http://127.0.0.1:9420', project: dir }), /uses cloud_firestore on the web \(cloud_firestore_web\), and with Auth on Aruma its requests would carry the user's Aruma ID token to Google's Firestore host; add firestore to --products/);
      assert.throws(() => flutterWeb({ url: 'http://127.0.0.1:9420', project: dir, products: ['auth', 'firestore'] }), /uses firebase_storage .* add storage to --products/);
      // Nothing was written by the refused runs.
      assert.equal(read(dir, 'web', 'index.html'), INDEX);
      assert.equal(existsSync(join(dir, 'web', 'aruma')), false);
      const r = flutterWeb({ url: 'http://127.0.0.1:9420', project: dir, products: ['firestore', 'storage', 'auth'] });
      assert.deepEqual(r.products, ['auth', 'storage', 'firestore']);
      assert.match(read(r.index), /'auth', 'storage', 'firestore'\]/);
      assert.match(read(r.index), /Firebase Auth, Storage and Firestore on Aruma/);
    });
    assert.deepEqual([...webPlugins(tmpdir())], []);
  });

  test('fails on a firebase-auth.js it does not know, a bad URL, and a page that loads Auth from gstatic', () => {
    // No apiHost default (another release): nothing found to rewrite.
    withProject({ auth: AUTH_CDN.replace('apiHost:"identitytoolkit.googleapis.com",', '') }, (dir) => {
      assert.throws(() => flutterWeb({ url: 'https://aruma.example.com', project: dir }), /found 0 apiHost, 1 tokenApiHost and 1 apiScheme defaults; this Firebase Auth release is not supported/);
    });
    // A Google Auth host the rule does not rewrite (in a URL, not a host literal).
    withProject({ auth: `${AUTH_CDN}const u="https://securetoken.googleapis.com/v1/token";` }, (dir) => {
      assert.throws(() => flutterWeb({ url: 'https://aruma.example.com', project: dir }), /a Google Auth host is still in the code after the rewrite/);
    });
    // On http the scheme default must be there, once.
    withProject({ auth: AUTH_CDN.replace(',apiScheme:"https"', '') }, (dir) => {
      assert.doesNotThrow(() => flutterWeb({ url: 'https://aruma.example.com', project: dir }));
      assert.throws(() => flutterWeb({ url: 'http://127.0.0.1:9420', project: dir }), /found 1 apiHost, 1 tokenApiHost and 0 apiScheme defaults/);
    });
    withProject({}, (dir) => {
      assert.throws(() => flutterWeb({ url: 'http://aruma.example.com', project: dir }), /not loopback/);
      assert.throws(() => flutterWeb({ url: 'https://identitytoolkit.googleapis.com', project: dir }), /Google Auth host/);
      assert.throws(() => flutterWeb({ url: 'https://aruma.example.com', project: dir, products: ['auth', 'database'] }), /products are 'auth', 'storage' and 'firestore'/);
      assert.throws(() => flutterWeb({ url: 'https://aruma.example.com', project: dir, products: [] }), /products are/);
    });
    const gstatic = INDEX.replace('</head>', '  <script src="https://www.gstatic.com/firebasejs/10.7.0/firebase-auth-compat.js"></script>\n</head>');
    withProject({ index: gstatic }, (dir) => {
      assert.throws(() => flutterWeb({ url: 'https://aruma.example.com', project: dir }), /loads the Firebase SDK from www\.gstatic\.com itself .*firebase-auth-compat\.js/);
      // Without auth, that script is not this step's business (the warning says Auth is on Google).
      assert.doesNotThrow(() => flutterWeb({ url: 'https://aruma.example.com', project: dir, products: ['storage'] }));
    });
    assert.throws(() => patchIndex(INDEX.replace('</body>', '<script src="//www.gstatic.com/firebasejs/13.1.0/firebase-storage.js"></script></body>'), 'f.js'), /from www\.gstatic\.com itself/);
  });

  test('the firebase npm package of this repository: its firebase-auth.js passes the rule', () => {
    const pkg = dirname(createRequire(import.meta.url).resolve('firebase/package.json'));
    const code = readFileSync(join(pkg, 'firebase-auth.js'), 'utf8');
    for (const url of ['http://127.0.0.1:9420', 'https://aruma.example.com', 'https://aruma.example.com/base']) {
      const base = authBase(url);
      const r = rewriteAuthFile(code, base);
      assert.equal(r.ok, true, `${url}: ${JSON.stringify({ ...r, code: undefined })}`);
      assert.equal(r.hosts, 3);
      assert.equal(r.scheme, base.protocol === 'https' ? 0 : 1);
    }
    assert.throws(() => authBase("https://aruma.example.com/a'b"), /plain path/);
  });

  test('the command line: auth and storage by default, the warning on stderr, exit 1 when refused', () => {
    withProject({}, (dir) => {
      const out = execFileSync(process.execPath, [CLI, 'flutter-web', '--url', 'http://127.0.0.1:9420', '--project', dir, '--firebase', join(dir, 'node_modules', 'firebase')], { encoding: 'utf8' });
      assert.match(out, /^@aruma\/web flutter-web: Firebase JS SDK 13\.1\.0, auth and storage on 127\.0\.0\.1:9420\n {2}wrote .*firebase-auth-13\.1\.0\.js\n {2}wrote .*firebase-storage-13\.1\.0\.js\n {2}patched .*index\.html\n$/);
      const off = spawnSync(process.execPath, [CLI, 'flutter-web', '--url', 'http://127.0.0.1:9420', '--project', dir, '--products', 'storage'], { encoding: 'utf8' });
      assert.equal(off.status, 0, off.stderr);
      assert.match(off.stdout, /, storage on 127\.0\.0\.1:9420\n {2}wrote .*firebase-storage-13\.1\.0\.js\n {2}removed .*firebase-auth-13\.1\.0\.js\n/);
      assert.match(off.stderr, /^warning: the project uses firebase_auth on the web/);
      const refused = spawnSync(process.execPath, [CLI, 'flutter-web', '--url', 'http://127.0.0.1:9420', '--project', dir, '--products', 'auth'], { encoding: 'utf8' });
      assert.equal(refused.status, 1);
      assert.match(refused.stderr, /add storage to --products/);
    });
  });
});
