// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

// Configuration: which Aruma server, which project and which service account key.

import { readFileSync } from 'node:fs';
import { argumentError } from './errors.js';

/**
 * The emulator variable switches the official firebase-admin in the same process to accept
 * unsigned tokens and to send "Bearer owner" to whatever host it names. Refuse to start with it set, unless the caller opts in
 * outside production.
 * @param {{allowEmulatorHost?: boolean}} options
 * @param {NodeJS.ProcessEnv} env
 */
export function emulatorGuard(options, env = process.env) {
  if (!env.FIREBASE_AUTH_EMULATOR_HOST) {
    return;
  }
  if (options.allowEmulatorHost === true && env.NODE_ENV !== 'production') {
    return;
  }
  throw argumentError(
    'FIREBASE_AUTH_EMULATOR_HOST is set: firebase-admin would accept unsigned ID tokens in this process. ' +
    'Unset it, or pass { allowEmulatorHost: true } outside production.');
}

/**
 * Reads a service account key file (path or parsed object) into the fields the library uses.
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
    'or pass { credential } to getAuth().');
}

const tokenPath = '/oauth2/v1/token';

function isLoopback(hostname) {
  return hostname === 'localhost' || hostname === '[::1]' || /^127\.\d+\.\d+\.\d+$/.test(hostname);
}

/**
 * Resolves the Aruma public URL: the url option, then ARUMA_URL, then the key file's token_uri
 * (Aruma writes <public_url>/oauth2/v1/token there). It must be https, except on loopback, and
 * never a Google host: that would mean a Google key was supplied.
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
  if (u.search || u.hash || u.username || u.password) {
    throw argumentError(`The Aruma URL must not carry a query, fragment or credentials: ${raw}`);
  }
  return (u.origin + u.pathname).replace(/\/+$/, '');
}
