// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

// The build step for Flutter web (ADR-022 §16.1). firebase_core_web loads the Firebase JS SDK from
// www.gstatic.com at run time (firebase-<service>.js of supportedFirebaseJsSdkVersion), so no bundler
// ever sees it. But it skips the services named in window.flutterfire_ignore_scripts and reads each
// service from window.firebase_<service>. So, once per firebase_core_web version:
//
//   npx aruma-web flutter-web --url https://aruma.example.com   (in the Flutter project)
//
// 1. reads the pinned version from the project's firebase_core_web (.dart_tool/package_config.json,
//    then lib/src/firebase_sdk_version.dart);
// 2. takes firebase-storage.js of that exact version from the `firebase` npm package (the same CDN
//    build, published to npm: no request to Google), rewrites its host literal (and, for an http
//    Aruma, its protocol literal) exactly once each, or fails, and writes it to
//    web/aruma/firebase-storage-<version>.js with a notice that it was modified;
// 3. replaces the flutter_bootstrap.js script of web/index.html with a module that sets
//    flutterfire_ignore_scripts = [..., 'storage'], imports that file as window.firebase_storage and
//    only then loads flutter_bootstrap.js, so Dart never runs before the rewritten SDK is in place.
//    The file still imports firebase-app.js from www.gstatic.com at the same version, the URL
//    firebase_core_web loads, so both share one Firebase app.
//
// Re-running it is safe: it rewrites its own block. After a firebase_core_web upgrade, run it again
// (the version is in the file name and the block).
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { literalProblem, rewriteStorageLiterals, storageBase } from './storage.js';

const BEGIN = '<!-- @aruma/web flutter-web: Firebase Storage on Aruma. Generated; run `npx aruma-web flutter-web` again to change it. -->';
const END = '<!-- /@aruma/web flutter-web -->';
const BOOTSTRAP = /<script\b[^>]*\bsrc\s*=\s*["']flutter_bootstrap\.js["'][^>]*>\s*<\/script>/g;

/** The Firebase JS SDK version the project's firebase_core_web pins. */
export function pinnedVersion(project) {
  const configPath = join(project, '.dart_tool', 'package_config.json');
  if (!existsSync(configPath)) throw new Error(`@aruma/web flutter-web: ${configPath} is missing; run flutter pub get first`);
  const { packages = [] } = JSON.parse(readFileSync(configPath, 'utf8'));
  const core = packages.find((p) => p.name === 'firebase_core_web');
  if (!core) throw new Error('@aruma/web flutter-web: the project does not use firebase_core_web (firebase_core on the web)');
  const root = fileURLToPath(new URL(core.rootUri.endsWith('/') ? core.rootUri : `${core.rootUri}/`, pathToFileURL(configPath)));
  const dart = readFileSync(join(root, 'lib', 'src', 'firebase_sdk_version.dart'), 'utf8');
  const m = /supportedFirebaseJsSdkVersion\s*=\s*'([^']+)'/.exec(dart);
  if (!m) throw new Error(`@aruma/web flutter-web: supportedFirebaseJsSdkVersion not found in ${root}`);
  return m[1];
}

/** firebase-storage.js of the `firebase` npm package at `dir` (or resolved from the project). */
export function sdkFile(project, version, dir) {
  let pkgDir = dir;
  if (!pkgDir) {
    try {
      pkgDir = dirname(createRequire(join(resolve(project), 'package.json')).resolve('firebase/package.json'));
    } catch {
      throw new Error(`@aruma/web flutter-web: the firebase npm package is not installed; run npm install --no-save firebase@${version} in the project, or pass --firebase <dir>`);
    }
  }
  const pkg = JSON.parse(readFileSync(join(pkgDir, 'package.json'), 'utf8'));
  if (pkg.name !== 'firebase' || pkg.version !== version) {
    throw new Error(`@aruma/web flutter-web: firebase_core_web loads the Firebase JS SDK ${version}, but ${pkgDir} is ${pkg.name}@${pkg.version}; install firebase@${version}`);
  }
  return join(pkgDir, 'firebase-storage.js');
}

/** The block that takes the place of the flutter_bootstrap.js script. */
export function indexBlock(file, attrs) {
  return `${BEGIN}
<script type="module">
  window.flutterfire_ignore_scripts = [...(window.flutterfire_ignore_scripts ?? []), 'storage'];
  window.firebase_storage = await import('./aruma/${file}');
  const bootstrap = document.createElement('script');
  bootstrap.src = 'flutter_bootstrap.js';
  bootstrap.async = ${attrs.async};
  document.body.append(bootstrap);
</script>
${END}`;
}

/** web/index.html with the block in place of the bootstrap script (or of an earlier block). */
export function patchIndex(html, file) {
  const begin = html.indexOf(BEGIN);
  if (begin !== -1) {
    const end = html.indexOf(END, begin);
    if (end === -1) throw new Error('@aruma/web flutter-web: index.html has the start of the block but not its end');
    const old = html.slice(begin, end + END.length);
    const async = /bootstrap\.async = (true|false)/.exec(old)?.[1] ?? 'true';
    return html.slice(0, begin) + indexBlock(file, { async }) + html.slice(end + END.length);
  }
  const tags = html.match(BOOTSTRAP) ?? [];
  if (tags.length !== 1) {
    throw new Error(`@aruma/web flutter-web: expected one <script src="flutter_bootstrap.js"> in web/index.html, found ${tags.length}`);
  }
  return html.replace(BOOTSTRAP, () => indexBlock(file, { async: /\basync\b/.test(tags[0]) ? 'true' : 'false' }));
}

/**
 * The whole step. Returns what it wrote.
 * @param {{ url: string, allowInsecure?: boolean, project?: string, firebase?: string, version?: string }} options
 */
export function flutterWebStorage({ url, allowInsecure = false, project = '.', firebase, version }) {
  const base = storageBase(url, { allowInsecure });
  const v = version ?? pinnedVersion(project);
  const src = sdkFile(project, v, firebase);
  const r = rewriteStorageLiterals(readFileSync(src, 'utf8'), base);
  if (!r.ok) throw new Error(literalProblem(src, r, base));
  const file = `firebase-storage-${v}.js`;
  const outDir = join(project, 'web', 'aruma');
  mkdirSync(outDir, { recursive: true });
  const notice = `/* firebase-storage.js ${v} of the firebase npm package (Apache-2.0, Google LLC), modified by @aruma/web flutter-web: its Storage host is ${base.host}${base.protocol === 'https' ? '' : ' over http'}. */\n`;
  writeFileSync(join(outDir, file), notice + r.code);
  const indexPath = join(project, 'web', 'index.html');
  writeFileSync(indexPath, patchIndex(readFileSync(indexPath, 'utf8'), file));
  return { version: v, file: join(outDir, file), index: indexPath, host: base.host };
}
