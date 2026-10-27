// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

// Cloud Firestore on the web (SR3-10). Aruma serves Firestore at the root of its host: REST (the
// lite SDK, with fetch) and WebChannel (the full SDK, /google.firestore.v1.Firestore/{Listen,Write}/channel).
// Both SDKs build every URL from DatabaseInfo's host and ssl, which come from the settings
// (FirestoreSettingsImpl: host = settings.host ?? DEFAULT_HOST, ssl = settings.ssl ?? DEFAULT_SSL).
// Unlike connectFirestoreEmulator, setting host and ssl keeps production mode (isUsingEmulator is
// only set by emulatorOptions), so the client behaves as it does against Google. Two ways in:
//
// - The build-time rewrite (the default, build.js, vite.js, next.js, metro.js): the one definition
//   of the default host in each @firebase/firestore dist file becomes Aruma's host, and the default
//   ssl `true` becomes `false` when Aruma is on http. getFirestore() then needs no change.
// - The run-time setting: arumaFirestoreSettings(url, settings) returns the settings to pass to
//   initializeFirestore(app, …), for a build that cannot be rewritten.
//
// What reaches Google is refused, not sent: index.js fails a request to Google's Firestore host
// while Aruma Auth is installed, because the ID token travels in it (in the WebChannel body).
import { ArumaConfigError, parseBase } from './index.js';

/** The host @firebase/firestore compiles in. Not written as the bare quoted literal (see storage.js). */
export const FIRESTORE_HOST = new URL('https://firestore.googleapis.com/').hostname;

/**
 * Validates the Aruma URL for Firestore: parseBase's rules, and no path (Firestore is served from the
 * root of the host, and the SDK builds `<scheme>://<host>/v1/…` and `/google.firestore.v1.Firestore/…`).
 *
 * @returns {{ origin: string, prefix: string, href: string, protocol: string, host: string }}
 * @throws {ArumaConfigError}
 */
export function firestoreBase(url, { allowInsecure = false } = {}) {
  const base = parseBase(url, { allowInsecure });
  if (base.prefix !== '') {
    throw new ArumaConfigError('the Aruma URL for Firestore must be the root of its host (no path): Firestore is served at https://<host>/v1/… and /google.firestore.v1.Firestore/…');
  }
  if (stripDot(new URL(base.origin).hostname).endsWith('googleapis.com')) {
    throw new ArumaConfigError('the Aruma URL cannot be a Google host');
  }
  // The host goes into a JS string literal of the SDK: see storage.js.
  if (!HOST_SHAPE.test(base.host)) {
    throw new ArumaConfigError('the Aruma URL for Firestore must have a plain host: letters, digits, dots and hyphens, or an IP address, and an optional port');
  }
  return base;
}

const HOST_SHAPE = /^(?:[a-z0-9.-]+|\[[0-9a-f:.]+\])(?::[0-9]{1,5})?$/;

const stripDot = (hostname) => (hostname.endsWith('.') ? hostname.slice(0, -1) : hostname);

// ---------------------------------------------------------------------------------------------
// The build-time rule.

// Every dist file of @firebase/firestore (the full and lite SDKs, ESM, CJS, React Native and Node
// builds), also when a package manager nests it. firestore-compat is another directory and holds
// no host: it wraps these.
const FIRESTORE_DIST = /[\\/]@firebase[\\/]firestore[\\/]dist[\\/](?:.*[\\/])?[^\\/]+\.(?:m?js|cjs)$/;

/** True for a module id (a path, maybe with a ?query) of one of @firebase/firestore's dist files. */
export function isFirestoreModule(id) {
  return FIRESTORE_DIST.test(String(id).split('?')[0]);
}

// The Node builds also hold the host in the gRPC descriptor metadata ("(google.api.default_host)":
// "firestore.googleapis.com"), which names the service and connects nowhere: it stays.
const HOST_LITERAL = /(?<!default_host\)["']\s*:\s*)(["'])firestore\.googleapis\.com\1/g;
// The default ssl, as the browser and React Native builds minify it next to the host
// (`rn = "firestore.googleapis.com", sn = true;`) and as the Node builds spell it.
const SSL_AFTER_HOST = /(["']firestore\.googleapis\.com["']\s*,\s*[\w$]+\s*=\s*)(?:true|!0)(?![\w$])/g;
const SSL_NAMED = /(\bDEFAULT_SSL\s*=\s*)true(?![\w$])/g;

/**
 * The build-time rewrite of one @firebase/firestore module: the quoted host literal (the definition
 * of DEFAULT_HOST) becomes Aruma's host, and, when Aruma is on http, the default ssl becomes false.
 *
 * Most dist files import the shared chunk and hold no literal (`hosts` is 0, the code unchanged).
 * `ok` is false when the literal appears twice, or the ssl default is not found exactly once next
 * to a host literal on http: a release this rule does not know.
 *
 * @param {string} code
 * @param {{ host: string, protocol: string }} base from firestoreBase
 * @returns {{ code: string, hosts: number, schemes: number, ok: boolean }}
 */
export function rewriteFirestoreLiterals(code, base) {
  let hosts = 0;
  let schemes = 0;
  let out = code;
  if (base.protocol !== 'https') {
    out = out.replace(SSL_AFTER_HOST, (_, key) => {
      schemes++;
      return `${key}false`;
    });
    out = out.replace(SSL_NAMED, (_, key) => {
      schemes++;
      return `${key}false`;
    });
  }
  out = out.replace(HOST_LITERAL, () => {
    hosts++;
    return JSON.stringify(base.host);
  });
  const ok = hosts <= 1 && schemes === (hosts === 1 && base.protocol !== 'https' ? 1 : 0);
  return { code: out, hosts, schemes, ok };
}

/** Why a module was not rewritten, for the build error. */
export function firestoreLiteralProblem(id, r, base) {
  const want = base.protocol === 'https' ? 'the host literal at most once' : 'the host literal and the ssl default once each';
  return `@aruma/web: expected ${want} in ${id}, found ${r.hosts} host and ${r.schemes} ssl literals; this @firebase/firestore release is not supported by the build-time rewrite. Use the run-time setting (arumaFirestoreSettings with initializeFirestore) and report the version`;
}

/** Whether the code of a @firebase/firestore module, as it ends up in the bundle, no longer names Google. */
export function firestoreModuleRewritten(code) {
  return !namesGoogleFirestore(code);
}

/** Whether the module carries the (rewritten) definition: the build needs one such module. */
export function firestoreModuleCarries(code, base) {
  return code.includes(`"${base.host}"`) || code.includes(`'${base.host}'`);
}

/** True when a built file still holds Google's Firestore host as a string literal. */
export function namesGoogleFirestore(code) {
  HOST_LITERAL.lastIndex = 0;
  return HOST_LITERAL.test(code);
}

// ---------------------------------------------------------------------------------------------
// The run-time setting.

/**
 * The settings for initializeFirestore(app, settings) that point the SDK, full or lite, at Aruma in
 * production mode (host and ssl; not connectFirestoreEmulator, which switches the client to
 * emulator behaviour). Call initializeFirestore before the app's first getFirestore().
 *
 * @template {object} T
 * @param {string | URL} url
 * @param {T} [settings] other Firestore settings, kept as they are
 * @param {{ allowInsecure?: boolean }} [options]
 * @returns {T & { host: string, ssl: boolean }}
 */
export function arumaFirestoreSettings(url, settings = {}, { allowInsecure = false } = {}) {
  const base = firestoreBase(url, { allowInsecure });
  if (settings.emulatorOptions !== undefined) {
    throw new ArumaConfigError('arumaFirestoreSettings: emulatorOptions switch the SDK to emulator behaviour; leave it out');
  }
  return { ...settings, host: base.host, ssl: base.protocol === 'https' };
}
