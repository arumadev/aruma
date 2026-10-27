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
// Cloud Firestore (SR3-10) is the same step with `--products storage,firestore` (or `firestore`):
// firebase-firestore.js is rewritten the same way (its host literal, and its ssl default on http),
// written to web/aruma/firebase-firestore-<version>.js, ignored as 'firestore' and set as
// window.firebase_firestore. Without it, cloud_firestore on the web loads Google's copy.
//
// Re-running it is safe: it rewrites its own block. After a firebase_core_web upgrade, run it again
// (the version is in the file name and the block).
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { firestoreBase, firestoreLiteralProblem, rewriteFirestoreLiterals } from './firestore.js';
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

/** firebase-<product>.js of the `firebase` npm package at `dir` (or resolved from the project). */
export function sdkFile(project, version, dir, product = 'storage') {
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
  return join(pkgDir, `firebase-${product}.js`);
}

/** The block that takes the place of the flutter_bootstrap.js script. `files`: [[product, file]]. */
export function indexBlock(files, attrs) {
  const names = files.map(([name]) => `'${name}'`).join(', ');
  const imports = files.map(([name, file]) => `  window.firebase_${name} = await import('./aruma/${file}');`).join('\n');
  return `${BEGIN}
<script type="module">
  window.flutterfire_ignore_scripts = [...(window.flutterfire_ignore_scripts ?? []), ${names}];
${imports}
  const bootstrap = document.createElement('script');
  bootstrap.src = 'flutter_bootstrap.js';
  bootstrap.async = ${attrs.async};
  document.body.append(bootstrap);
</script>
${END}`;
}

/** web/index.html with the block in place of the bootstrap script (or of an earlier block). `file` is the Storage file, or the list of [product, file]. */
export function patchIndex(html, file) {
  const files = typeof file === 'string' ? [['storage', file]] : file;
  const begin = html.indexOf(BEGIN);
  if (begin !== -1) {
    const end = html.indexOf(END, begin);
    if (end === -1) throw new Error('@aruma/web flutter-web: index.html has the start of the block but not its end');
    const old = html.slice(begin, end + END.length);
    const async = /bootstrap\.async = (true|false)/.exec(old)?.[1] ?? 'true';
    return html.slice(0, begin) + indexBlock(files, { async }) + html.slice(end + END.length);
  }
  const tags = html.match(BOOTSTRAP) ?? [];
  if (tags.length !== 1) {
    throw new Error(`@aruma/web flutter-web: expected one <script src="flutter_bootstrap.js"> in web/index.html, found ${tags.length}`);
  }
  return html.replace(BOOTSTRAP, () => indexBlock(files, { async: /\basync\b/.test(tags[0]) ? 'true' : 'false' }));
}

/**
 * The whole step. Returns what it wrote: the first product's fields (`file`, `host`, which for the
 * default are Storage's, as before), and `files` with every product's file.
 * @param {{ url: string, allowInsecure?: boolean, project?: string, firebase?: string, version?: string,
 *   products?: Array<'storage' | 'firestore'> }} options
 */
export function flutterWeb({ url, allowInsecure = false, project = '.', firebase, version, products = ['storage'] }) {
  const wanted = [...new Set(products)];
  if (wanted.length === 0 || wanted.some((p) => p !== 'storage' && p !== 'firestore')) {
    throw new Error("@aruma/web flutter-web: products are 'storage' and 'firestore'");
  }
  const rules = {
    storage: { base: storageBase, rewrite: rewriteStorageLiterals, problem: literalProblem, what: 'Storage host' },
    firestore: { base: firestoreBase, rewrite: rewriteFirestoreLiterals, problem: firestoreLiteralProblem, what: 'Firestore host' },
  };
  const bases = Object.fromEntries(wanted.map((p) => [p, rules[p].base(url, { allowInsecure })]));
  const v = version ?? pinnedVersion(project);
  const outDir = join(project, 'web', 'aruma');
  const staged = wanted.map((product) => {
    const base = bases[product];
    const src = sdkFile(project, v, firebase, product);
    const r = rules[product].rewrite(readFileSync(src, 'utf8'), base);
    // The CDN build holds the host literal exactly once.
    if (!r.ok || r.hosts !== 1) throw new Error(rules[product].problem(src, r, base));
    const file = `firebase-${product}-${v}.js`;
    const notice = `/* firebase-${product}.js ${v} of the firebase npm package (Apache-2.0, Google LLC), modified by @aruma/web flutter-web: its ${rules[product].what} is ${base.host}${base.protocol === 'https' ? '' : ' over http'}. */\n`;
    return { product, file, code: notice + r.code };
  });
  mkdirSync(outDir, { recursive: true });
  for (const f of staged) writeFileSync(join(outDir, f.file), f.code);
  const indexPath = join(project, 'web', 'index.html');
  writeFileSync(indexPath, patchIndex(readFileSync(indexPath, 'utf8'), staged.map((f) => [f.product, f.file])));
  return {
    version: v,
    file: join(outDir, staged[0].file),
    files: staged.map((f) => join(outDir, f.file)),
    index: indexPath,
    host: bases[staged[0].product].host,
  };
}

/** The Storage step: flutterWeb with `products: ['storage']`. */
export function flutterWebStorage(options) {
  return flutterWeb({ ...options, products: ['storage'] });
}
