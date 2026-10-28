// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

// The service account's access token, from Aruma's token endpoint. firebase-admin's cert() and
// @google-cloud/storage's keyFilename both go through gtoken, which posts to a fixed
// https://oauth2.googleapis.com/token whatever token_uri says, so the library makes the RFC 7523 exchange itself, at the key file's
// server.

import { createPrivateKey, sign } from 'node:crypto';
import { FirebaseStorageError } from 'firebase-admin/storage';

const scope = 'https://www.googleapis.com/auth/devstorage.full_control https://www.googleapis.com/auth/cloud-platform';
const jwtBearer = 'urn:ietf:params:oauth:grant-type:jwt-bearer';
// google-auth-library refreshes a token five minutes before it expires; keeping ours until then
// gives it one exchange per token.
const refreshMargin = 5 * 60 * 1000;

function segment(obj) {
  return Buffer.from(JSON.stringify(obj)).toString('base64url');
}

function credentialError(message) {
  return new FirebaseStorageError({ code: 'invalid-credential', message });
}

export class TokenSource {
  /**
   * @param {{url: string, key: ReturnType<import('./config.js').readKey>, fetch?: typeof fetch}} options
   */
  constructor({ url, key, fetch: fetchImpl = globalThis.fetch }) {
    this.tokenURL = url + '/oauth2/v1/token';
    this.key = key;
    this.privateKey = createPrivateKey(key.privateKey); // fails early on a damaged key
    this.fetch = fetchImpl;
    this.token = null; // { access_token, expiry_date }
    this.pending = null;
  }

  /** Returns { access_token, expiry_date }, the shape google-auth-library's refreshHandler returns. */
  async get() {
    if (this.token && Date.now() < this.token.expiry_date - refreshMargin) {
      return this.token;
    }
    this.pending ??= this.exchange().finally(() => { this.pending = null; });
    return this.pending;
  }

  async exchange() {
    const iat = Math.floor(Date.now() / 1000);
    const header = { alg: 'RS256', typ: 'JWT', ...(this.key.privateKeyId ? { kid: this.key.privateKeyId } : {}) };
    const claims = { iss: this.key.clientEmail, scope, aud: this.tokenURL, iat, exp: iat + 3600 };
    const signing = segment(header) + '.' + segment(claims);
    const assertion = signing + '.' + sign('RSA-SHA256', Buffer.from(signing), this.privateKey).toString('base64url');
    let res;
    try {
      res = await this.fetch(this.tokenURL, {
        method: 'POST',
        headers: { 'content-type': 'application/x-www-form-urlencoded' },
        body: new URLSearchParams({ grant_type: jwtBearer, assertion }),
        signal: AbortSignal.timeout(10_000),
        redirect: 'error',
      });
    } catch (err) {
      throw credentialError(`Cannot reach the Aruma token endpoint ${this.tokenURL}: ${err.message}`);
    }
    const body = await res.json().catch(() => ({}));
    if (!res.ok || typeof body.access_token !== 'string') {
      throw credentialError(`Aruma refused the service account key: ${body.error_description ?? body.error ?? `HTTP ${res.status}`}`);
    }
    const lifetime = Number(body.expires_in) || 3600;
    this.token = { access_token: body.access_token, expiry_date: Date.now() + lifetime * 1000 };
    return this.token;
  }
}
