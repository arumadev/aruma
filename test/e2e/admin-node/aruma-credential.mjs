// arumaCredential: a firebase-admin Credential that exchanges an Aruma service account key
// for an access token at the key file's own token_uri.
//
// Why it exists: firebase-admin's cert() hands token fetching to google-auth-library/gtoken,
// which posts to a hardcoded https://oauth2.googleapis.com/token and ignores token_uri. That
// sends a signed assertion for an Aruma account to Google and never yields a token Aruma
// accepts. This helper does the same RFC 7523 JWT-bearer exchange, but against token_uri.
//
//   import { initializeApp } from 'firebase-admin/app';
//   import { arumaCredential } from './aruma-credential.mjs';
//
//   const app = initializeApp({ credential: arumaCredential('service-account.json'), projectId: 'my-app' });
//   const { access_token } = await app.options.credential.getAccessToken();
//
// It depends only on node:crypto, node:fs and the global fetch (Node 18+). firebase-admin
// caches the token it gets from a credential and asks again shortly before expiry; direct
// callers get a fresh exchange per call unless they cache it themselves.
import { createPrivateKey, sign } from 'node:crypto';
import { readFileSync } from 'node:fs';

/** The scopes firebase-admin itself requests. Aruma accepts and records them. */
export const DEFAULT_SCOPES = [
  'https://www.googleapis.com/auth/cloud-platform',
  'https://www.googleapis.com/auth/firebase.database',
  'https://www.googleapis.com/auth/firebase.messaging',
  'https://www.googleapis.com/auth/identitytoolkit',
  'https://www.googleapis.com/auth/userinfo.email',
];

const JWT_BEARER = 'urn:ietf:params:oauth:grant-type:jwt-bearer';
const LIFETIME_SECONDS = 3600; // Aruma rejects assertions with exp - iat above one hour.

/**
 * Builds a credential from a key file path or its parsed JSON.
 *
 * @param {string | object} keyFileOrJson - path to the key file Aruma issued, or its contents.
 * @param {{ scopes?: string[], fetch?: typeof fetch }} [options]
 * @returns {{ getAccessToken(): Promise<{ access_token: string, expires_in: number }> }}
 */
export function arumaCredential(keyFileOrJson, { scopes = DEFAULT_SCOPES, fetch: fetchImpl = globalThis.fetch } = {}) {
  const key = typeof keyFileOrJson === 'string' ? JSON.parse(readFileSync(keyFileOrJson, 'utf8')) : keyFileOrJson;
  for (const field of ['client_email', 'private_key', 'private_key_id', 'token_uri']) {
    if (typeof key?.[field] !== 'string' || key[field] === '') {
      throw new Error(`arumaCredential: the key file has no ${field}`);
    }
  }
  if (key.type !== 'service_account') throw new Error('arumaCredential: the key file is not a service_account key');
  const tokenURL = new URL(key.token_uri);
  if (tokenURL.protocol !== 'https:' && tokenURL.protocol !== 'http:') {
    throw new Error(`arumaCredential: unsupported token_uri ${key.token_uri}`);
  }
  const privateKey = createPrivateKey(key.private_key); // Fails early on a damaged key.

  return {
    async getAccessToken() {
      const assertion = signAssertion({
        kid: key.private_key_id,
        iss: key.client_email,
        aud: key.token_uri,
        scope: scopes.join(' '),
        privateKey,
      });
      const res = await fetchImpl(key.token_uri, {
        method: 'POST',
        headers: { 'content-type': 'application/x-www-form-urlencoded', accept: 'application/json' },
        body: new URLSearchParams({ grant_type: JWT_BEARER, assertion }).toString(),
      });
      const text = await res.text();
      let body;
      try {
        body = JSON.parse(text);
      } catch {
        body = {};
      }
      if (!res.ok || typeof body.access_token !== 'string') {
        const reason = body.error ? `${body.error}: ${body.error_description ?? ''}`.trim() : text.slice(0, 200);
        throw new Error(`arumaCredential: token exchange at ${key.token_uri} failed with HTTP ${res.status} (${reason})`);
      }
      return { access_token: body.access_token, expires_in: Number(body.expires_in) || LIFETIME_SECONDS };
    },
  };
}

/** Signs the RFC 7523 assertion: header {alg: RS256, typ: JWT, kid}, claims iss/scope/aud/iat/exp. */
function signAssertion({ kid, iss, aud, scope, privateKey }) {
  // Backdate iat a little, as golang.org/x/oauth2 does, to absorb clock skew.
  const iat = Math.floor(Date.now() / 1000) - 10;
  const header = { alg: 'RS256', typ: 'JWT', kid };
  const claims = { iss, scope, aud, iat, exp: iat + LIFETIME_SECONDS };
  const input = `${b64url(JSON.stringify(header))}.${b64url(JSON.stringify(claims))}`;
  const signature = sign('RSA-SHA256', Buffer.from(input), privateKey);
  return `${input}.${b64url(signature)}`;
}

function b64url(data) {
  return Buffer.from(data).toString('base64url');
}
