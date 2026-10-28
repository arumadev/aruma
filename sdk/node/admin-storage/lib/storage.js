// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

// ArumaStorage: the official @google-cloud/storage client, configured for Aruma as ADR-022 §14
// decides.
//
// - apiEndpoint is the Aruma origin, and useAuthWithCustomEndpoint keeps the authorization a
//   custom endpoint otherwise drops (nodejs-common/util.js:495-500).
// - universeDomain is the Aruma host. The resumable upload, which save() and
//   createWriteStream() use by default, is not given useAuthWithCustomEndpoint (file.js:3531-3560
//   in 8.4.0), so it would start the session and send its chunks without credentials; it keeps
//   the authorized client only for a host equal to the universe or under it
//   (resumable-upload.js:124-147).
// - authClient is a google-auth-library OAuth2Client whose tokens come from Aruma's token
//   endpoint (token.js), not from gtoken, which asks Google.
// - The authorization fails closed outside the Aruma origin (OriginGuard). The resumable upload
//   picks its authorized client once and then sends to whatever URL the start's Location names
//   (resumable-upload.js:123-147, :805, :838), and gaxios follows redirects with the headers it
//   was given, so a Location or a redirect to another origin, port or scheme would carry the
//   Bearer there.

import { Storage } from '@google-cloud/storage';
import { GoogleAuth, OAuth2Client } from 'google-auth-library';
import { FirebaseStorageError } from 'firebase-admin/storage';
import { argumentError } from './config.js';
import { TokenSource } from './token.js';

/** Throws unless url is on origin. */
function requireOrigin(origin, url) {
  let got = null;
  try {
    got = new URL(String(url)).origin;
  } catch {
    // not an absolute URL: refused below
  }
  if (got !== origin) {
    throw argumentError(`Refusing to send the Aruma access token to ${got ?? url}: this client talks to ${origin} only. ` +
      'An upload session on another origin means the url is not the server\'s public_url.');
  }
}

/**
 * ArumaAuthClient takes its tokens from Aruma, sends only to the Aruma origin, and follows no
 * redirect: maxRedirects 0 on each request (gaxios 7 reads it from the request's own options,
 * not from its defaults), so node-fetch rejects a 3xx with a Location instead of following it.
 * The 308 of a resumable chunk has none. The resumable upload sends through request().
 */
class ArumaAuthClient extends OAuth2Client {
  #origin;

  constructor(origin, tokens) {
    super({ universeDomain: new URL(origin).hostname });
    this.#origin = origin;
    this.refreshHandler = () => tokens.get();
  }

  request(opts, callback) {
    try {
      requireOrigin(this.#origin, opts?.url);
    } catch (err) {
      if (callback) {
        callback(err);
        return undefined;
      }
      return Promise.reject(err);
    }
    return super.request({ ...opts, maxRedirects: 0 }, callback);
  }

  async getRequestHeaders(url) {
    if (url !== undefined) requireOrigin(this.#origin, url);
    return super.getRequestHeaders(url);
  }
}

/**
 * OriginGuard is the GoogleAuth @google-cloud/storage uses as it is (nodejs-common/util.js:388).
 * Its JSON requests are authorized with authorizeRequest and sent by teeny-request, to URLs made
 * from apiEndpoint; the resumable upload calls request(). Signing (getSignedUrl,
 * generateSignedPostPolicyV2/V4) goes through getCredentials and sign, which would ask the GCE
 * metadata server and iamcredentials.googleapis.com: signed URLs are out of scope in v0.3
 * (ADR-023), so both refuse.
 */
class OriginGuard extends GoogleAuth {
  #origin;

  constructor(origin, client, projectId) {
    super({ authClient: client, universeDomain: client.universeDomain, ...(projectId ? { projectId } : {}) });
    this.#origin = origin;
  }

  async authorizeRequest(opts = {}) {
    requireOrigin(this.#origin, opts.uri ?? opts.url);
    return super.authorizeRequest(opts);
  }

  async request(opts) {
    requireOrigin(this.#origin, opts?.url);
    return super.request(opts);
  }

  async getCredentials() {
    throw signedURLsUnsupported();
  }

  async sign() {
    throw signedURLsUnsupported();
  }
}

function signedURLsUnsupported() {
  return new FirebaseStorageError({
    code: 'invalid-argument',
    message: 'Signed URLs are not supported in Aruma v0.3: use getDownloadURL() for a link, or read and write through the client.',
  });
}

export class ArumaStorage extends Storage {
  #app;
  #url;
  #defaultBucket;

  /**
   * @param {import('firebase-admin/app').App | null} app
   * @param {{url: string, projectId?: string, key: ReturnType<import('./config.js').readKey>, fetch?: typeof fetch}} options
   */
  constructor(app, { url, projectId, key, fetch }) {
    const universeDomain = new URL(url).hostname;
    const tokens = new TokenSource({ url, key, fetch });
    const authClient = new OriginGuard(url, new ArumaAuthClient(url, tokens), projectId);
    super({
      ...(projectId ? { projectId } : {}),
      apiEndpoint: url,
      universeDomain,
      authClient,
      useAuthWithCustomEndpoint: true,
    });
    if (this.authClient !== authClient) {
      // A second copy of google-auth-library would wrap the guard instead of using it.
      throw argumentError('@google-cloud/storage did not take the Aruma auth client: install one google-auth-library ^11 ' +
        '(npm dedupe).');
    }
    this.#app = app;
    this.#url = url;
    this.#defaultBucket = app?.options?.storageBucket;
  }

  /** The firebase-admin app the instance belongs to, as firebase-admin's Storage.app. */
  get app() {
    return this.#app;
  }

  /** The Aruma server's public URL. */
  get url() {
    return this.#url;
  }

  /**
   * Returns the named bucket, or without a name the app's storageBucket, as firebase-admin's
   * Storage.bucket does.
   * @param {string} [name]
   * @param {import('@google-cloud/storage').BucketOptions} [options]
   */
  bucket(name, options) {
    const bucketName = name ?? this.#defaultBucket;
    if (typeof bucketName !== 'string' || bucketName === '') {
      throw argumentError('Bucket name not specified or invalid. Specify a valid bucket name via the storageBucket ' +
        'option when initializing the app, or specify the bucket name explicitly when calling the bucket() method.');
    }
    return super.bucket(bucketName, options);
  }

  /**
   * The download URL of a file, in the one form every platform resolves (ADR-022 §16):
   * <aruma>/v0/b/<bucket>/o/<encodeURIComponent(name)>?alt=media&token=<token>. Like
   * firebase-admin's getDownloadURL it reads the Firebase metadata with the client's own
   * authorization (makeAuthenticatedRequest), but from Aruma, which gives a file without a
   * token its first one.
   * @param {import('@google-cloud/storage').File} file
   */
  async downloadURL(file) {
    const uri = `${this.#url}/v0/b/${file.bucket.name}/o/${encodeURIComponent(file.name)}`;
    const metadata = await new Promise((resolve, reject) => {
      this.makeAuthenticatedRequest({ method: 'GET', uri }, (err, body) => (err ? reject(err) : resolve(body)));
    });
    const [token] = String(metadata?.downloadTokens ?? '').split(',');
    if (!token) {
      throw new FirebaseStorageError({
        code: 'no-download-token',
        message: 'No download token available. Please create one in the Firebase Console.',
      });
    }
    return `${uri}?alt=media&token=${token}`;
  }
}
