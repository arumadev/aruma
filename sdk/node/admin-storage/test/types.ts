// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

// Compiled by `npm run typecheck`, never run: the declarations must give the official types.

import { initializeApp, cert } from 'firebase-admin/app';
import { Storage, type Bucket, type File } from '@google-cloud/storage';
import { getStorage, getDownloadURL, type ArumaStorage } from '../index.js';

export async function usage(): Promise<void> {
  const app = initializeApp({ credential: cert('service-account.json'), storageBucket: 'demo-app.aruma.example.com' });
  const storage: ArumaStorage = getStorage(app, { url: 'https://aruma.example.com' });
  const official: Storage = storage;
  const bucket: Bucket = storage.bucket();
  const other: Bucket = official.bucket('other');
  const file: File = bucket.file('photos/cat.png');
  await file.save(Buffer.from('meow'), { contentType: 'image/png', resumable: true });
  const [data] = await other.file('x').download();
  const url: string = await getDownloadURL(file);
  const sameApp: string | undefined = storage.app.options.projectId;
  void [data, url, sameApp, storage.url];
  // @ts-expect-error getDownloadURL takes a File
  await getDownloadURL('photos/cat.png');
}
