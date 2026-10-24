// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

import type { App } from 'firebase-admin/app';
import { Storage, type Bucket, type BucketOptions, type File } from '@google-cloud/storage';

/** A service account key file as Aruma issues it, or its path. */
export type ServiceAccountKey = string | {
  project_id?: string;
  client_email: string;
  private_key: string;
  private_key_id?: string;
  token_uri?: string;
};

export interface ArumaStorageOptions {
  /**
   * The Aruma server's public URL, such as https://aruma.example.com: an origin, without a path.
   * Defaults to ARUMA_URL, then to the origin of the key file's token_uri.
   */
  url?: string;
  /** The service account key. Defaults to the app's cert() credential, then GOOGLE_APPLICATION_CREDENTIALS. */
  credential?: ServiceAccountKey;
  /** A fetch implementation for the token exchange, for proxies and tests. */
  fetch?: typeof fetch;
}

/** The official @google-cloud/storage client, pointed at Aruma. */
export declare class ArumaStorage extends Storage {
  private constructor();
  /** The firebase-admin app the instance belongs to. */
  readonly app: App;
  /** The Aruma server's public URL. */
  readonly url: string;
  /** The named bucket, or the app's storageBucket, as firebase-admin's Storage.bucket(). */
  bucket(name?: string, options?: BucketOptions): Bucket;
  /** What getDownloadURL returns for one of this instance's files. */
  downloadURL(file: File): Promise<string>;
}

/** Returns Aruma Storage for the app, or the default app. */
export declare function getStorage(app?: App, options?: ArumaStorageOptions): ArumaStorage;

/** Returns the file's download URL on Aruma: <aruma>/v0/b/<bucket>/o/<name>?alt=media&token=<token>. */
export declare function getDownloadURL(file: File): Promise<string>;
