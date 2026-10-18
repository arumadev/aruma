// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

// @aruma/admin-auth: the one line a developer's server changes (ADR-013). Import getAuth from
// here instead of firebase-admin/auth; everything else, including createCustomToken, the
// types and the error codes, stays firebase-admin's.

import { getApp } from 'firebase-admin/app';
import { ArumaAuth } from './lib/auth.js';

const instances = new WeakMap();

/**
 * Returns Aruma Auth for the app (the default app when omitted). Without options the instance
 * is shared per app, as firebase-admin's getAuth does.
 * @param {import('firebase-admin/app').App} [app]
 * @param {import('./index.js').ArumaAuthOptions} [options]
 */
export function getAuth(app, options) {
  const a = app ?? getApp();
  if (options) {
    return new ArumaAuth(a, options);
  }
  let auth = instances.get(a);
  if (!auth) {
    auth = new ArumaAuth(a, {});
    instances.set(a, auth);
  }
  return auth;
}

export { ArumaAuth };
