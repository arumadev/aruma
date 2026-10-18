// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

// The Admin API client: a service account access token from Aruma's token endpoint, then the
// Identity Toolkit calls firebase-admin makes, sent to Aruma instead of Google.

import { createPrivateKey, sign } from 'node:crypto';
import { AuthErrorCode } from 'firebase-admin/auth';
import { authError, fromResponse } from './errors.js';

const scope = 'https://www.googleapis.com/auth/cloud-platform';
const jwtBearer = 'urn:ietf:params:oauth:grant-type:jwt-bearer';

function segment(obj) {
  return Buffer.from(JSON.stringify(obj)).toString('base64url');
}

export class AdminClient {
  /**
   * @param {{url: string, projectId: string, key: ReturnType<import('./config.js').readKey>, fetch?: typeof fetch}} options
   */
  constructor({ url, projectId, key, fetch: fetchImpl = globalThis.fetch }) {
    this.tokenURL = url + '/oauth2/v1/token';
    this.base = `${url}/identitytoolkit.googleapis.com/v1/projects/${encodeURIComponent(projectId)}`;
    this.key = key;
    this.privateKey = createPrivateKey(key.privateKey);
    this.fetch = fetchImpl;
    this.token = null; // { value, refreshAt }
    this.pending = null;
  }

  // accessToken exchanges an RFC 7523 assertion signed with the key, as Google's libraries do,
  // and keeps the token until a minute before it expires.
  async accessToken() {
    if (this.token && Date.now() < this.token.refreshAt) {
      return this.token.value;
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
      throw authError(AuthErrorCode.INVALID_CREDENTIAL, `Cannot reach the Aruma token endpoint ${this.tokenURL}: ${err.message}`);
    }
    const body = await res.json().catch(() => ({}));
    if (!res.ok || typeof body.access_token !== 'string') {
      throw authError(AuthErrorCode.INVALID_CREDENTIAL,
        `Aruma refused the service account key: ${body.error_description ?? body.error ?? `HTTP ${res.status}`}`);
    }
    const lifetime = Number(body.expires_in) || 3600;
    this.token = { value: body.access_token, refreshAt: Date.now() + (lifetime - 60) * 1000 };
    return this.token.value;
  }

  /**
   * Calls the Admin API. path is relative to /v1/projects/{project}, e.g. "/accounts:lookup".
   * @param {'GET' | 'POST'} method
   * @param {string} path
   * @param {object} [body]
   */
  async call(method, path, body) {
    const token = await this.accessToken();
    let res;
    try {
      res = await this.fetch(this.base + path, {
        method,
        headers: { authorization: `Bearer ${token}`, ...(body ? { 'content-type': 'application/json' } : {}) },
        body: body ? JSON.stringify(body) : undefined,
        signal: AbortSignal.timeout(30_000),
        redirect: 'error',
      });
    } catch (err) {
      throw authError(AuthErrorCode.INTERNAL_ERROR, `Cannot reach Aruma Auth at ${this.base}: ${err.message}`);
    }
    const parsed = await res.json().catch(() => undefined);
    if (!res.ok) {
      throw fromResponse(res.status, parsed);
    }
    return parsed ?? {};
  }
}
