// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

import { test, describe } from 'node:test';
import assert from 'node:assert/strict';
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { pathToFileURL } from 'node:url';
import { flutterWebStorage, patchIndex, pinnedVersion } from '../src/flutter-web.js';
import { getCacheKey, transform, withArumaStorage } from '../src/metro.js';
import { CDN_SNIPPET, ESM_SNIPPET } from './storage-fixtures.js';

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

// A Flutter project with firebase_core_web pinning `version`, and a firebase npm package.
function project(version = '13.1.0', npmVersion = version) {
  const dir = mkdtempSync(join(tmpdir(), 'aruma-flutter-'));
  const core = join(dir, 'pub', 'firebase_core_web-4.1.0');
  mkdirSync(join(core, 'lib', 'src'), { recursive: true });
  writeFileSync(join(core, 'lib', 'src', 'firebase_sdk_version.dart'), `part of '../firebase_core_web.dart';\n\nconst String supportedFirebaseJsSdkVersion = '${version}';\n`);
  mkdirSync(join(dir, '.dart_tool'));
  writeFileSync(join(dir, '.dart_tool', 'package_config.json'), JSON.stringify({ packages: [{ name: 'firebase_core_web', rootUri: pathToFileURL(core).href, packageUri: 'lib/' }] }));
  mkdirSync(join(dir, 'web'));
  writeFileSync(join(dir, 'web', 'index.html'), INDEX);
  const fb = join(dir, 'node_modules', 'firebase');
  mkdirSync(fb, { recursive: true });
  writeFileSync(join(fb, 'package.json'), JSON.stringify({ name: 'firebase', version: npmVersion }));
  writeFileSync(join(fb, 'firebase-storage.js'), `import{x}from"https://www.gstatic.com/firebasejs/${npmVersion}/firebase-app.js";${CDN_SNIPPET}`);
  return dir;
}

describe('flutter-web', () => {
  test('the pinned version, from the project', () => {
    const dir = project('13.1.0');
    try {
      assert.equal(pinnedVersion(dir), '13.1.0');
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
  });

  test('writes the rewritten SDK and loads it before flutter_bootstrap.js, once', () => {
    const dir = project();
    try {
      const r = flutterWebStorage({ url: 'http://127.0.0.1:9420', project: dir });
      assert.equal(r.version, '13.1.0');
      const js = readFileSync(r.file, 'utf8');
      assert.match(js, /^\/\* firebase-storage\.js 13\.1\.0 of the firebase npm package \(Apache-2\.0, Google LLC\), modified by @aruma\/web/);
      assert.match(js, /const l="127\.0\.0\.1:9420"/);
      assert.match(js, /this\._protocol="http"/);
      assert.match(js, /from"https:\/\/www\.gstatic\.com\/firebasejs\/13\.1\.0\/firebase-app\.js"/);
      const html = readFileSync(r.index, 'utf8');
      assert.doesNotMatch(html, /<script src="flutter_bootstrap\.js"/);
      assert.match(html, /flutterfire_ignore_scripts = \[\.\.\.\(window\.flutterfire_ignore_scripts \?\? \[\]\), 'storage'\]/);
      assert.match(html, /window\.firebase_storage = await import\('\.\/aruma\/firebase-storage-13\.1\.0\.js'\);/);
      assert.match(html, /bootstrap\.async = true;/);
      // Again: the block is replaced, not added.
      flutterWebStorage({ url: 'https://aruma.example.com', project: dir });
      const again = readFileSync(r.index, 'utf8');
      assert.equal(again.split('<!-- @aruma/web flutter-web').length - 1, 1);
      assert.match(readFileSync(r.file, 'utf8'), /const l="aruma\.example\.com"/);
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
  });

  test('fails on another SDK version, on a file it does not know and on an index.html without the script', () => {
    const dir = project('13.1.0', '13.0.0');
    try {
      assert.throws(() => flutterWebStorage({ url: 'https://aruma.example.com', project: dir }), /loads the Firebase JS SDK 13\.1\.0, but .* is firebase@13\.0\.0/);
      writeFileSync(join(dir, 'node_modules', 'firebase', 'package.json'), JSON.stringify({ name: 'firebase', version: '13.1.0' }));
      writeFileSync(join(dir, 'node_modules', 'firebase', 'firebase-storage.js'), 'const l="x";');
      assert.throws(() => flutterWebStorage({ url: 'https://aruma.example.com', project: dir }), /not supported by the build-time rewrite/);
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
    assert.throws(() => patchIndex('<html></html>', 'f.js'), /expected one <script src="flutter_bootstrap\.js">/);
  });
});

describe('metro', () => {
  test('withArumaStorage puts the transformer in front of the current one', () => {
    const config = { transformer: { babelTransformerPath: '/upstream.js', other: 1 }, resolver: {} };
    assert.equal(withArumaStorage(config, {}), config);
    const c = withArumaStorage(config, { url: 'https://aruma.example.com' });
    assert.match(c.transformer.babelTransformerPath, /src[\\/]metro\.js$/);
    assert.equal(c.transformer.other, 1);
    assert.deepEqual(JSON.parse(process.env.ARUMA_WEB_METRO), { url: 'https://aruma.example.com', allowInsecure: false, upstream: '/upstream.js' });
    assert.throws(() => withArumaStorage(config, { url: 'http://aruma.example.com' }), /not loopback/);
  });

  test('transform rewrites @firebase/storage and hands every file to the upstream transformer', () => {
    const dir = mkdtempSync(join(tmpdir(), 'aruma-metro-'));
    try {
      const upstream = join(dir, 'upstream.cjs');
      writeFileSync(upstream, 'exports.transform = (a) => ({ src: a.src, filename: a.filename }); exports.getCacheKey = () => "up";');
      process.env.ARUMA_WEB_METRO = JSON.stringify({ url: 'http://127.0.0.1:9420', allowInsecure: false, upstream });
      const id = '/app/node_modules/@firebase/storage/dist/index.node.cjs.js';
      assert.match(transform({ filename: id, src: ESM_SNIPPET }).src, /const DEFAULT_HOST = "127\.0\.0\.1:9420";/);
      assert.equal(transform({ filename: '/app/App.js', src: 'x' }).src, 'x');
      assert.throws(() => transform({ filename: id, src: 'const x = 1;' }), /not supported by the build-time rewrite/);
      assert.equal(getCacheKey(), ['up', 'aruma-web-storage-1', 'http://127.0.0.1:9420', 'false'].join('\0'));
    } finally {
      delete process.env.ARUMA_WEB_METRO;
      rmSync(dir, { recursive: true, force: true });
    }
  });
});
