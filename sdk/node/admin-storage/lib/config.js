// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

// Configuration: which Aruma server and which service account key.

import { readFileSync } from 'node:fs';
import { FirebaseStorageError } from 'firebase-admin/storage';

/** Returns a FirebaseStorageError with code storage/invalid-argument. */
export function argumentError(message) {
  return new FirebaseStorageError({ code: 'invalid-argument', message });
}

/**
 * With STORAGE_EMULATOR_HOST set, @google-cloud/storage sends its JSON requests to that host
 * whatever apiEndpoint says (storage.js:474 in 8.4.0), and without credentials; firebase-admin
 * turns FIREBASE_STORAGE_EMULATOR_HOST into it. Aruma accepts no unauthenticated GCS request
 * (ADR-022 §14), so the library refuses to start, with no opt-in.
 * @param {NodeJS.ProcessEnv} env
 */
export function emulatorGuard(env = process.env) {
  for (const name of ['STORAGE_EMULATOR_HOST', 'FIREBASE_STORAGE_EMULATOR_HOST']) {
    if (env[name]) {
      throw argumentError(`${name} is set: @google-cloud/storage would talk to that host without credentials. ` +
        'Unset it to reach Aruma.');
    }
  }
}

/**
 * Reads a service account key file (path or parsed object) into the fields the library uses.
 * universe_domain is not one of them: the token comes from token_uri whatever it says.
 * @param {string | Record<string, any>} source
 */
export function readKey(source) {
  let raw = source;
  if (typeof source === 'string') {
    try {
      raw = JSON.parse(readFileSync(source, 'utf8'));
    } catch (err) {
      throw argumentError(`Cannot read the service account key file ${source}: ${err.message}`);
    }
  }
  if (!raw || typeof raw !== 'object') {
    throw argumentError('The service account credential must be a key file path or object.');
  }
  const key = {
    projectId: raw.project_id ?? raw.projectId,
    clientEmail: raw.client_email ?? raw.clientEmail,
    privateKey: raw.private_key ?? raw.privateKey,
    privateKeyId: raw.private_key_id ?? raw.privateKeyId,
    tokenUri: raw.token_uri ?? raw.tokenUri,
  };
  if (typeof key.clientEmail !== 'string' || typeof key.privateKey !== 'string') {
    throw argumentError('The service account key needs client_email and private_key.');
  }
  return key;
}

/**
 * Finds the service account key: the explicit option, then the key behind the app's cert()
 * credential, then GOOGLE_APPLICATION_CREDENTIALS. firebase-admin keeps the source it was
 * given on its ServiceAccountCredential; that field is not public API, so its absence is
 * reported plainly instead of guessed around.
 */
export function findKey(app, options, env = process.env) {
  if (options.credential) {
    return readKey(options.credential);
  }
  const cred = app?.options?.credential;
  if (cred?.serviceAccountPathOrObject) {
    return readKey(cred.serviceAccountPathOrObject);
  }
  if (typeof cred?.clientEmail === 'string' && typeof cred?.privateKey === 'string') {
    return readKey({ client_email: cred.clientEmail, private_key: cred.privateKey, project_id: cred.projectId });
  }
  if (env.GOOGLE_APPLICATION_CREDENTIALS) {
    return readKey(env.GOOGLE_APPLICATION_CREDENTIALS);
  }
  throw argumentError(
    'No service account key: initialise the app with cert(<Aruma key file>), set GOOGLE_APPLICATION_CREDENTIALS, ' +
    'or pass { credential } to getStorage().');
}

const tokenPath = '/oauth2/v1/token';

function isLoopback(hostname) {
  return hostname === 'localhost' || hostname === '[::1]' || /^127\.\d+\.\d+\.\d+$/.test(hostname);
}

/**
 * Resolves the Aruma public URL: the url option, then ARUMA_URL, then the key file's token_uri
 * (Aruma writes <public_url>/oauth2/v1/token there). It must be https, except on loopback, never
 * a Google host (that would mean a Google key was supplied), and an origin alone: Aruma serves
 * Storage at the root of its domain, and the download URL is <origin>/v0/…. The result is
 * URL.origin: lower case, without the default port, the form of every platform's download URL
 * (ADR-022 §16).
 */
export function resolveURL(options, key, env = process.env) {
  let raw = options.url ?? env.ARUMA_URL;
  if (!raw && typeof key.tokenUri === 'string' && key.tokenUri.endsWith(tokenPath)) {
    raw = key.tokenUri.slice(0, -tokenPath.length);
  }
  if (!raw) {
    throw argumentError('Set ARUMA_URL (or the url option) to your Aruma server, for example https://aruma.example.com.');
  }
  let u;
  try {
    u = new URL(raw);
  } catch {
    throw argumentError(`Invalid Aruma URL: ${raw}`);
  }
  if (/(^|\.)(googleapis|google)\.com$/i.test(u.hostname)) {
    throw argumentError(`${raw} is a Google address, not an Aruma server: check the key file and ARUMA_URL.`);
  }
  if (u.protocol !== 'https:' && !(u.protocol === 'http:' && isLoopback(u.hostname))) {
    throw argumentError(`The Aruma URL must use https (plain http is allowed on loopback only): ${raw}`);
  }
  if (u.search || u.hash || u.username || u.password || raw.includes('?') || raw.includes('#')) {
    throw argumentError(`The Aruma URL must not carry a query, fragment or credentials: ${raw}`);
  }
  if (u.pathname.replace(/\/+$/, '') !== '') {
    throw argumentError(`The Aruma URL must be the server's origin, without a path: ${raw}`);
  }
  return u.origin;
}
