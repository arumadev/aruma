// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

// ArumaStorage: the official @google-cloud/storage client, configured for Aruma as ADR-022 §14
// decides and docs/research/storage-production-path.md measured.
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

import { Storage } from '@google-cloud/storage';
import { OAuth2Client } from 'google-auth-library';
import { FirebaseStorageError } from 'firebase-admin/storage';
import { argumentError } from './config.js';
import { TokenSource } from './token.js';

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
    const authClient = new OAuth2Client({ universeDomain });
    authClient.refreshHandler = () => tokens.get();
    super({
      ...(projectId ? { projectId } : {}),
      apiEndpoint: url,
      universeDomain,
      authClient,
      useAuthWithCustomEndpoint: true,
    });
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
