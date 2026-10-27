// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

import type { ArumaBase } from './index.js';

/** `firestore.googleapis.com`: the host @firebase/firestore compiles in. */
export declare const FIRESTORE_HOST: string;

/** Validates the Aruma URL for Firestore: the root of an https host (http only for loopback, or allowInsecure). */
export declare function firestoreBase(url: string | URL, options?: { allowInsecure?: boolean }): ArumaBase;

/** True for a module id of one of @firebase/firestore's dist files. */
export declare function isFirestoreModule(id: string): boolean;

/** The build-time rewrite of one @firebase/firestore module: its host literal (and ssl default on http). `hosts` is 0 for a file without the definition. */
export declare function rewriteFirestoreLiterals(code: string, base: ArumaBase): { code: string; hosts: number; schemes: number; ok: boolean };

/** Whether a built file still holds Google's Firestore host as a string literal. */
export declare function namesGoogleFirestore(code: string): boolean;

/**
 * The settings for `initializeFirestore(app, settings)` that point the SDK (full or lite) at Aruma in
 * production mode: `host` and `ssl`, not the emulator. Call it before the first `getFirestore()`.
 */
export declare function arumaFirestoreSettings<T extends object>(
  url: string | URL,
  settings?: T,
  options?: { allowInsecure?: boolean },
): T & { host: string; ssl: boolean };
