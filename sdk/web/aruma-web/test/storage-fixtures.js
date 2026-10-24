// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

// The lines of @firebase/storage 0.13 and 0.14 that the rewrite touches, as the ESM dist has them
// (index.esm.js:27, :2810, :3204 and :3246 in 0.14.6), and as the CDN build has them minified.
export const ESM_SNIPPET = `
const DEFAULT_HOST = 'firebasestorage.googleapis.com';
/**
 *         http[s]://firebasestorage.googleapis.com/
 */
function connectStorageEmulator$1(storage, host, port, options = {}) {
    storage.host = \`\${host}:\${port}\`;
    storage._protocol = useSsl ? 'https' : 'http';
}
class FirebaseStorageImpl {
    constructor(app) {
        this._host = DEFAULT_HOST;
        this._protocol = 'https';
    }
}
`;
export const CDN_SNIPPET =
  'const l="firebasestorage.googleapis.com",c="storageBucket";e._isUsingEmulator=!0,e._protocol=o?"https":"http";' +
  'this._isUsingEmulator=s,this._bucket=null,this._host=l,this._protocol="https",this._appId=null';
