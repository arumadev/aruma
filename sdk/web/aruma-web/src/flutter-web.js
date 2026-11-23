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
// Firebase Auth (`auth`, in the default set with `storage`) is the same step too: firebase-auth.js has
// the two Auth hosts compiled in (identitytoolkit.googleapis.com, securetoken.googleapis.com), and
// firebase_auth_web uses it for every Auth request (sign-up, sign-in, token refresh, profile...).
// Its `apiHost` and `tokenApiHost` defaults become paths on Aruma's host (auth.js, the rule of the
// Vite plugin's mode 'build'), and on http its `apiScheme` too, each found exactly once or the step
// fails. It is written to web/aruma/firebase-auth-<version>.js, ignored as 'auth' and set as
// window.firebase_auth, first, so no Auth request can leave for Google. Without it, sign-up and
// sign-in send the user's email and password to Google: that is why it is in the default set, and
// `--products storage` (a list without auth) turns it off, with a warning when the project uses
// firebase_auth. With Auth on Aruma, Storage and Firestore must be on Aruma too (their SDKs send
// the user's ID token with every request): the step fails when the project uses firebase_storage
// or cloud_firestore on the web and the product is left out.
//
// Re-running it is safe: it rewrites its own block, and removes the files of products (and versions)
// it no longer writes. After a firebase_core_web upgrade, run it again (the version is in the file
// name and the block). It fails when web/index.html also loads one of these SDKs from a CDN itself.
import { existsSync, mkdirSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { authBase, authLiteralProblem, rewriteAuthFile } from './auth.js';
import { firestoreBase, firestoreLiteralProblem, rewriteFirestoreLiterals } from './firestore.js';
import { literalProblem, rewriteStorageLiterals, storageBase } from './storage.js';

// The block's first line names its products; an earlier block is found by its start (BEGIN_AT),
// whatever products (or wording, before Auth was a product) it names.
const BEGIN_AT = '<!-- @aruma/web flutter-web:';
const begin = (names) => `${BEGIN_AT} Firebase ${names} on Aruma. Generated; run \`npx aruma-web flutter-web\` again to change it. -->`;
const END = '<!-- /@aruma/web flutter-web -->';

/** The products, in the order the block loads them: Auth first. */
export const PRODUCTS = Object.freeze(['auth', 'storage', 'firestore']);
/** The products of a run that names none: Auth (else it goes to Google with the user's password) and Storage. */
export const DEFAULT_PRODUCTS = Object.freeze(['auth', 'storage']);
// The FlutterFire web plugin of each product, as .dart_tool/package_config.json lists it, and its
// Dart package.
const PLUGINS = { auth: ['firebase_auth_web', 'firebase_auth'], storage: ['firebase_storage_web', 'firebase_storage'], firestore: ['cloud_firestore_web', 'cloud_firestore'] };
const LABEL = { auth: 'Auth', storage: 'Storage', firestore: 'Firestore' };
// A file this step writes into web/aruma/.
const WRITTEN = /^firebase-(?:auth|storage|firestore)-[0-9][0-9A-Za-z.-]*\.js$/;
// A script that loads one of the products' SDK from www.gstatic.com (any version, compat too).
const CDN_SCRIPT = /<script\b[^>]*\bsrc\s*=\s*["'](?:https?:)?\/\/www\.gstatic\.com\/firebasejs\/[^"']*\/firebase-(auth|storage|firestore)(?:-compat)?\.js["'][^>]*>/g;
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

/**
 * The products whose FlutterFire web plugin the project uses (firebase_auth_web, firebase_storage_web,
 * cloud_firestore_web in .dart_tool/package_config.json). Empty when that file is missing.
 */
export function webPlugins(project) {
  const configPath = join(project, '.dart_tool', 'package_config.json');
  if (!existsSync(configPath)) return new Set();
  const { packages = [] } = JSON.parse(readFileSync(configPath, 'utf8'));
  const names = new Set(packages.map((p) => p.name));
  return new Set(PRODUCTS.filter((p) => names.has(PLUGINS[p][0])));
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
  const labels = files.map(([name]) => LABEL[name] ?? name);
  return `${begin(labels.length > 1 ? `${labels.slice(0, -1).join(', ')} and ${labels.at(-1)}` : labels[0])}
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
  let out;
  const at = html.indexOf(BEGIN_AT);
  if (at !== -1) {
    const end = html.indexOf(END, at);
    if (end === -1) throw new Error('@aruma/web flutter-web: index.html has the start of the block but not its end');
    const old = html.slice(at, end + END.length);
    const async = /bootstrap\.async = (true|false)/.exec(old)?.[1] ?? 'true';
    out = html.slice(0, at) + indexBlock(files, { async }) + html.slice(end + END.length);
  } else {
    const tags = html.match(BOOTSTRAP) ?? [];
    if (tags.length !== 1) {
      throw new Error(`@aruma/web flutter-web: expected one <script src="flutter_bootstrap.js"> in web/index.html, found ${tags.length}`);
    }
    out = html.replace(BOOTSTRAP, () => indexBlock(files, { async: /\basync\b/.test(tags[0]) ? 'true' : 'false' }));
  }
  // The page must not load Google's copy of a product as well (an older FlutterFire setup put the
  // SDK's <script> tags in index.html): that copy keeps Google's host.
  const wanted = new Set(files.map(([name]) => name));
  const cdn = [...out.matchAll(CDN_SCRIPT)].filter((m) => wanted.has(m[1])).map((m) => m[0]);
  if (cdn.length) {
    throw new Error(`@aruma/web flutter-web: web/index.html loads the Firebase SDK from www.gstatic.com itself (${cdn.join(' ')}), which keeps Google's host; remove that script, the block loads the rewritten file instead`);
  }
  return out;
}

/**
 * The whole step. Returns what it wrote: `file` and `host` (Storage's when Storage is among the
 * products, as before Auth was one, else the first product's), `files` with every product's file,
 * `products` in the order the block loads them, `removed` with the files of an earlier run it took
 * out, and `warnings` for the caller to show.
 * @param {{ url: string, allowInsecure?: boolean, project?: string, firebase?: string, version?: string,
 *   products?: Array<'auth' | 'storage' | 'firestore'> }} options
 */
export function flutterWeb({ url, allowInsecure = false, project = '.', firebase, version, products = DEFAULT_PRODUCTS }) {
  const asked = new Set(products);
  if (asked.size === 0 || [...asked].some((p) => !PRODUCTS.includes(p))) {
    throw new Error("@aruma/web flutter-web: products are 'auth', 'storage' and 'firestore'");
  }
  const wanted = PRODUCTS.filter((p) => asked.has(p));
  const rules = {
    auth: { base: authBase, rewrite: rewriteAuthFile, accept: (r) => r.ok, problem: authLiteralProblem, what: (b) => `Auth hosts are ${b.host}${b.prefix}/identitytoolkit.googleapis.com and ${b.host}${b.prefix}/securetoken.googleapis.com` },
    storage: { base: storageBase, rewrite: rewriteStorageLiterals, accept: (r) => r.ok && r.hosts === 1, problem: literalProblem, what: (b) => `Storage host is ${b.host}` },
    firestore: { base: firestoreBase, rewrite: rewriteFirestoreLiterals, accept: (r) => r.ok && r.hosts === 1, problem: firestoreLiteralProblem, what: (b) => `Firestore host is ${b.host}` },
  };
  const bases = Object.fromEntries(wanted.map((p) => [p, rules[p].base(url, { allowInsecure })]));
  // With Auth on Aruma, the Storage and Firestore SDKs send the Aruma ID token with every request:
  // one left on Google's host would carry it there (SR3-06, SR3-10).
  const used = webPlugins(project);
  const warnings = [];
  if (wanted.includes('auth')) {
    for (const p of ['storage', 'firestore']) {
      if (used.has(p) && !wanted.includes(p)) {
        throw new Error(`@aruma/web flutter-web: the project uses ${PLUGINS[p][1]} on the web (${PLUGINS[p][0]}), and with Auth on Aruma its requests would carry the user's Aruma ID token to Google's ${LABEL[p]} host; add ${p} to --products`);
      }
    }
  } else if (used.has('auth')) {
    warnings.push(`warning: the project uses firebase_auth on the web (firebase_auth_web), but auth is not in --products: Firebase Auth stays on Google, and sign-up and sign-in send the user's email and password to identitytoolkit.googleapis.com. Add auth to --products (it is in the default set)`);
  }
  const v = version ?? pinnedVersion(project);
  const outDir = join(project, 'web', 'aruma');
  const staged = wanted.map((product) => {
    const base = bases[product];
    const src = sdkFile(project, v, firebase, product);
    const r = rules[product].rewrite(readFileSync(src, 'utf8'), base);
    // The CDN build holds each literal exactly once.
    if (!rules[product].accept(r)) throw new Error(rules[product].problem(src, r, base));
    const file = `firebase-${product}-${v}.js`;
    const notice = `/* firebase-${product}.js ${v} of the firebase npm package (Apache-2.0, Google LLC), modified by @aruma/web flutter-web: its ${rules[product].what(base)}${base.protocol === 'https' ? '' : ' over http'}. */\n`;
    return { product, file, code: notice + r.code };
  });
  const indexPath = join(project, 'web', 'index.html');
  const html = patchIndex(readFileSync(indexPath, 'utf8'), staged.map((f) => [f.product, f.file]));
  mkdirSync(outDir, { recursive: true });
  for (const f of staged) writeFileSync(join(outDir, f.file), f.code);
  // What an earlier run wrote for a product left out now, or for another SDK version.
  const keep = new Set(staged.map((f) => f.file));
  const removed = readdirSync(outDir).filter((name) => WRITTEN.test(name) && !keep.has(name));
  for (const name of removed) rmSync(join(outDir, name));
  writeFileSync(indexPath, html);
  const main = staged.find((f) => f.product === 'storage') ?? staged[0];
  return {
    version: v,
    file: join(outDir, main.file),
    files: staged.map((f) => join(outDir, f.file)),
    products: wanted,
    index: indexPath,
    host: bases[main.product].host,
    removed: removed.map((name) => join(outDir, name)),
    warnings,
  };
}

/** The Storage step: flutterWeb with `products: ['storage']`. */
export function flutterWebStorage(options) {
  return flutterWeb({ ...options, products: ['storage'] });
}
