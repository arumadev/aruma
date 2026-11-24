// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

// The build-time Auth rule: the two host literals of @firebase/auth become paths on Aruma's host.
// Aruma serves Google's exact Auth paths under its own host (ADR-014), so
//
//   "identitytoolkit.googleapis.com" → "<aruma host><prefix>/identitytoolkit.googleapis.com"
//   "securetoken.googleapis.com"     → "<aruma host><prefix>/securetoken.googleapis.com"
//
// and `apiScheme: "https"` becomes "http" when Aruma is on http. The SDK builds every Auth URL as
// `${apiScheme}://${apiHost}${path}` (identitytoolkit) or `${apiScheme}://${tokenApiHost}${path}`
// (securetoken, token refresh), so nothing else needs to change. EID_FROM_APIHOST keys on the same
// literal and stays consistent.
//
// Used by the Vite plugin's mode 'build' (vite.js) and by the Flutter web step (flutter-web.js), which
// rewrites the CDN build of firebase-auth.js that firebase_core_web would load from www.gstatic.com.
import { ArumaConfigError, parseBase } from './index.js';

/**
 * The rewrite of one @firebase/auth file.
 * @returns {{ code: string, hosts: number, scheme: number }}
 */
export function rewriteAuthLiterals(code, base) {
  let hosts = 0;
  let scheme = 0;
  let out = code.replace(/(["'])(identitytoolkit|securetoken)\.googleapis\.com\1/g, (_, q, name) => {
    hosts++;
    return `${q}${base.host}${base.prefix}/${name}.googleapis.com${q}`;
  });
  if (base.protocol !== 'https') {
    out = out.replace(/(apiScheme\s*:\s*)(["'])https\2/g, (_, key, q) => {
      scheme++;
      return `${key}${q}${base.protocol}${q}`;
    });
  }
  return { code: out, hosts, scheme };
}

// The defaults the SDK builds its config from (_getAuthConfig / the CDN's `apiHost:"…"`), and a
// Google Auth host still written as a host anywhere in a string: after the rewrite none is left.
const API_HOST = /(?<![A-Za-z0-9_$])apiHost\s*:\s*(["'])identitytoolkit\.googleapis\.com\1/g;
const TOKEN_API_HOST = /(?<![A-Za-z0-9_$])tokenApiHost\s*:\s*(["'])securetoken\.googleapis\.com\1/g;
const GOOGLE_AUTH_HOST = /(?:["'`]|\/\/)(?:identitytoolkit|securetoken)\.googleapis\.com/;
const SCHEME = /(?<![A-Za-z0-9_$])apiScheme\s*:\s*(["'])https\1/g;

const count = (re, s) => (s.match(re) ?? []).length;

/**
 * The Aruma URL for the Auth rewrite: parseBase's rules, and a host and path that can go into a JS
 * string literal as they are (the WHATWG parser lets quotes and backslashes through in some places).
 */
export function authBase(url, { allowInsecure = false } = {}) {
  const base = parseBase(url, { allowInsecure });
  if (!/^(?:[a-z0-9.-]+|\[[0-9a-f:.]+\])(?::[0-9]{1,5})?$/.test(base.host) || !/^(?:\/[A-Za-z0-9._~%-]+)*$/.test(base.prefix)) {
    throw new ArumaConfigError('the Aruma URL for Auth must have a plain host (letters, digits, dots and hyphens, or an IP address, and an optional port) and a plain path');
  }
  return base;
}

/**
 * The rewrite of a whole, single-file build of @firebase/auth (the CDN build, firebase-auth.js of the
 * firebase npm package), checked: its defaults `apiHost` and `tokenApiHost` are found exactly once
 * each, on http its `apiScheme` default exactly once, and afterwards no Google Auth host is left in
 * a string. `ok` false means a release this rule does not know: the caller fails.
 * @returns {{ code: string, hosts: number, scheme: number, apiHost: number, tokenApiHost: number, schemes: number, left: boolean, ok: boolean }}
 */
export function rewriteAuthFile(code, base) {
  const apiHost = count(API_HOST, code);
  const tokenApiHost = count(TOKEN_API_HOST, code);
  const schemes = count(SCHEME, code);
  const r = rewriteAuthLiterals(code, base);
  const left = GOOGLE_AUTH_HOST.test(r.code);
  const ok = apiHost === 1 && tokenApiHost === 1 && !left && (base.protocol === 'https' || (schemes === 1 && r.scheme === 1));
  return { ...r, apiHost, tokenApiHost, schemes, left, ok };
}

/** Why rewriteAuthFile refused a file. */
export function authLiteralProblem(id, r, base) {
  const want = base.protocol === 'https' ? 'the apiHost and tokenApiHost defaults once each' : 'the apiHost, tokenApiHost and apiScheme defaults once each';
  const left = r.left ? ', and a Google Auth host is still in the code after the rewrite' : '';
  return `@aruma/web: expected ${want} in ${id}, found ${r.apiHost} apiHost, ${r.tokenApiHost} tokenApiHost and ${r.schemes} apiScheme defaults${left}; this Firebase Auth release is not supported by the build-time rewrite. Report the version`;
}
