// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

// Verifies Aruma ID tokens and session cookies (ADR-004) against the keys their issuer
// publishes through OpenID discovery. Only RS256 with a kid is accepted, so unsigned tokens,
// "alg: none" and HMAC confusion fail before any key is looked at.

import { createPublicKey, verify as verifySignature } from 'node:crypto';
import { AuthErrorCode } from 'firebase-admin/auth';
import { authError, argumentError } from './errors.js';

const customTokenAudience = 'https://identitytoolkit.googleapis.com/google.identity.identitytoolkit.v1.IdentityToolkit';

// Clock skew allowed for iat and auth_time in the future. exp has none, as in firebase-admin
// for Node, where a token is expired at now >= exp.
const skewSeconds = 300;

// After an unknown kid, the keys are fetched again at most this often.
const refetchCooldownMs = 30_000;

/** The kinds of token, with the names and codes firebase-admin uses for them. */
export const ID_TOKEN = { name: 'ID token', prefix: '/issuers/', expired: AuthErrorCode.ID_TOKEN_EXPIRED };
export const SESSION_COOKIE = { name: 'session cookie', prefix: '/session-issuers/', expired: AuthErrorCode.SESSION_COOKIE_EXPIRED };

// Decodes base64url strictly: only the canonical form of each value is accepted, so one token has
// one spelling.
function decodeBase64url(segment) {
  if (!/^[A-Za-z0-9_-]*$/.test(segment)) {
    throw new Error('not base64url');
  }
  const bytes = Buffer.from(segment, 'base64url');
  if (bytes.toString('base64url') !== segment) {
    throw new Error('not canonical base64url');
  }
  return bytes;
}

function decodeSegment(segment) {
  return JSON.parse(decodeBase64url(segment).toString('utf8'));
}

function maxAgeMs(res) {
  const m = /max-age=(\d+)/i.exec(res.headers.get('cache-control') ?? '');
  return m ? Number(m[1]) * 1000 : 0;
}

export class Verifier {
  /**
   * @param {{url: string, projectId: string, kind: typeof ID_TOKEN, fetch?: typeof fetch}} options
   */
  constructor({ url, projectId, kind, fetch: fetchImpl = globalThis.fetch }) {
    this.kind = kind;
    this.projectId = projectId;
    this.origin = new URL(url).origin;
    this.issuer = url + kind.prefix + projectId;
    this.fetch = fetchImpl;
    this.keys = null; // { byKid: Map, expiresAt, fetchedAt }
    this.pending = null;
  }

  /**
   * Checks the token and returns its payload with uid set, as DecodedIdToken.
   * Expiry gives the kind's -expired code; every other failure argument-error, as in
   * firebase-admin.
   * @param {string} token
   */
  async verify(token) {
    const { name } = this.kind;
    if (typeof token !== 'string' || token === '') {
      throw argumentError(`First argument must be a non-empty string ${name}.`);
    }
    const parts = token.split('.');
    let header, payload;
    try {
      if (parts.length !== 3) {
        throw new Error('not a JWT');
      }
      header = decodeSegment(parts[0]);
      payload = decodeSegment(parts[1]);
    } catch {
      throw argumentError(`Decoding the ${name} failed: it is not a JWT.`);
    }
    if (!payload || typeof payload !== 'object') {
      throw argumentError(`The ${name} has no claims.`);
    }
    if (!header?.kid) {
      if (payload.aud === customTokenAudience) {
        throw argumentError(`Expected an ${name}, but was given a custom token.`);
      }
      throw argumentError(`The ${name} has no "kid" claim.`);
    }
    if (header.alg !== 'RS256') {
      throw argumentError(`The ${name} has algorithm "${header.alg}", but RS256 is required.`);
    }
    if (payload.aud !== this.projectId) {
      throw argumentError(`The ${name} has audience "${payload.aud}", but "${this.projectId}" was expected.`);
    }
    if (payload.iss !== this.issuer) {
      throw argumentError(`The ${name} has issuer "${payload.iss}", but "${this.issuer}" was expected.`);
    }
    if (typeof payload.sub !== 'string' || payload.sub === '' || payload.sub.length > 128) {
      throw argumentError(`The ${name} has an invalid "sub" claim.`);
    }
    const now = Math.floor(Date.now() / 1000);
    for (const claim of ['iat', 'exp', 'auth_time']) {
      if (typeof payload[claim] !== 'number') {
        throw argumentError(`The ${name} has no numeric "${claim}" claim.`);
      }
    }
    if (payload.iat > now + skewSeconds || payload.auth_time > now + skewSeconds) {
      throw argumentError(`The ${name} was issued in the future.`);
    }
    const key = await this.key(header.kid);
    const signed = Buffer.from(parts[0] + '.' + parts[1]);
    let signature;
    try {
      signature = decodeBase64url(parts[2]);
    } catch {
      throw argumentError(`The ${name} has an invalid signature.`);
    }
    if (!verifySignature('RSA-SHA256', signed, key, signature)) {
      throw argumentError(`The ${name} has an invalid signature.`);
    }
    // Expiry is reported only for a token that is otherwise genuine.
    if (now >= payload.exp) {
      throw authError(this.kind.expired, `The ${name} has expired.`);
    }
    return { ...payload, uid: payload.sub };
  }

  async key(kid) {
    let keys = await this.currentKeys();
    if (!keys.byKid.has(kid) && Date.now() - keys.fetchedAt > refetchCooldownMs) {
      keys = await this.currentKeys(true);
    }
    const key = keys.byKid.get(kid);
    if (!key) {
      throw argumentError(`The ${this.kind.name} has "kid" ${JSON.stringify(kid)}, which is not a published key of ${this.issuer}.`);
    }
    return key;
  }

  async currentKeys(force = false) {
    if (!force && this.keys && Date.now() < this.keys.expiresAt) {
      return this.keys;
    }
    this.pending ??= this.load().finally(() => { this.pending = null; });
    return this.pending;
  }

  // load reads the issuer's discovery document, then its JWKS. Both must come from the Aruma
  // server the library was configured with.
  async load() {
    try {
      const disc = await this.getJSON(this.issuer + '/.well-known/openid-configuration');
      if (disc.body.issuer !== this.issuer || typeof disc.body.jwks_uri !== 'string' || new URL(disc.body.jwks_uri).origin !== this.origin) {
        throw new Error('the discovery document does not describe this issuer');
      }
      const jwks = await this.getJSON(disc.body.jwks_uri);
      const byKid = new Map();
      for (const jwk of jwks.body.keys ?? []) {
        if (jwk.kty === 'RSA' && (jwk.alg ?? 'RS256') === 'RS256' && (jwk.use ?? 'sig') === 'sig' && jwk.kid) {
          byKid.set(jwk.kid, createPublicKey({ key: { kty: jwk.kty, n: jwk.n, e: jwk.e }, format: 'jwk' }));
        }
      }
      const now = Date.now();
      this.keys = { byKid, fetchedAt: now, expiresAt: now + jwks.maxAge };
      return this.keys;
    } catch (err) {
      throw argumentError(`Cannot fetch the public keys of ${this.issuer}: ${err.message}`);
    }
  }

  async getJSON(url) {
    const res = await this.fetch(url, { signal: AbortSignal.timeout(10_000), redirect: 'error' });
    if (!res.ok) {
      throw new Error(`HTTP ${res.status} from ${url}`);
    }
    return { body: await res.json(), maxAge: maxAgeMs(res) };
  }
}
