// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

// @aruma/admin-storage: the one line a developer's server changes (ADR-022 §14). Import
// getStorage and getDownloadURL from here instead of firebase-admin/storage; the buckets and
// files are @google-cloud/storage's own, as with firebase-admin.

import { getApp } from 'firebase-admin/app';
import { argumentError, emulatorGuard, findKey, resolveURL } from './lib/config.js';
import { ArumaStorage } from './lib/storage.js';

const instances = new WeakMap();

function create(app, options) {
  emulatorGuard();
  const key = findKey(app, options);
  const url = resolveURL(options, key);
  const projectId = app?.options?.projectId ?? key.projectId;
  return new ArumaStorage(app, { url, projectId, key, fetch: options.fetch });
}

/**
 * Returns Aruma Storage for the app (the default app when omitted): an @google-cloud/storage
 * Storage pointed at Aruma, whose bucket() defaults to the app's storageBucket. Without options
 * the instance is shared per app, as firebase-admin's getStorage does.
 * @param {import('firebase-admin/app').App} [app]
 * @param {import('./index.js').ArumaStorageOptions} [options]
 */
export function getStorage(app, options) {
  const a = app ?? getApp();
  if (options) {
    return create(a, options);
  }
  let storage = instances.get(a);
  if (!storage) {
    storage = create(a, {});
    instances.set(a, storage);
  }
  return storage;
}

/**
 * Returns the file's download URL on Aruma, in the unified form
 * <aruma>/v0/b/<bucket>/o/<name>?alt=media&token=<token>. firebase-admin's getDownloadURL would
 * send the Aruma token to firebasestorage.googleapis.com instead.
 * @param {import('@google-cloud/storage').File} file a file of a bucket from getStorage()
 */
export async function getDownloadURL(file) {
  if (!(file?.storage instanceof ArumaStorage)) {
    throw argumentError('getDownloadURL needs a file of a bucket from getStorage() of @aruma/admin-storage.');
  }
  return file.storage.downloadURL(file);
}

export { ArumaStorage };
