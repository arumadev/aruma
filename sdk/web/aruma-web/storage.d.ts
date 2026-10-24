// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

import type { ArumaBase } from './index.js';

/** `firebasestorage.googleapis.com`: the host @firebase/storage compiles in. */
export declare const STORAGE_HOST: string;

/** The Firebase JS SDK on www.gstatic.com, which no build can rewrite. */
export declare const GSTATIC_FIREBASE: RegExp;

/** Validates the Aruma URL for Storage: the root of an https host (http only for loopback, or allowInsecure). */
export declare function storageBase(url: string | URL, options?: { allowInsecure?: boolean }): ArumaBase;

/** True for a module id of one of @firebase/storage's dist files. */
export declare function isStorageModule(id: string): boolean;

/** The build-time rewrite of one @firebase/storage module; `ok` when each literal was found exactly once. */
export declare function rewriteStorageLiterals(code: string, base: ArumaBase): { code: string; hosts: number; schemes: number; ok: boolean };

/** Whether a @firebase/storage module's code, as bundled, names Aruma and not Google. */
export declare function storageModuleRewritten(code: string, base: ArumaBase): boolean;

/** `https://firebasestorage.googleapis.com[:443]/<path>?<query>` → `{ path, search }`, else null. */
export declare function matchStorage(url: string | URL): { path: string; search: string } | null;

/** A Google Storage URL → the same path and query on Aruma's root, or null. */
export declare function rewriteStorageURL(url: string | URL, base: string | ArumaBase): string | null;

/** A Storage link on Google's host → the unified link on Aruma; anything else unchanged. */
export declare function toArumaUrl(url: string | URL, base?: ArumaBase): string;

export interface StorageInstallOptions {
  /** The Aruma server, e.g. `https://aruma.example.com` (its root). Missing or empty: nothing is installed. */
  url?: string | URL;
  /** Accept `http` to a host that is not loopback. Development only. */
  allowInsecure?: boolean;
  /** Log each redirected request (path only). */
  log?: boolean;
}

/**
 * The fallback's request redirect: XMLHttpRequest and fetch to Google's Storage host go to Aruma.
 * @returns true when active, false when not configured.
 * @throws ArumaConfigError when `url` is refused; Storage requests then fail instead of reaching Google.
 */
export declare function installStorage(options?: StorageInstallOptions): boolean;

/** `{ url }` when active, `{ blocked }` when configured but refused, null when not installed. */
export declare function storageStatus(): { url: string } | { blocked: string } | null;

/** Turns the Storage redirect off. */
export declare function uninstallStorage(): void;

/**
 * The fallback for links: points one FirebaseStorage instance at Aruma (its `host` and, on http,
 * `_protocol`), so getDownloadURL returns the unified link. Call it before the instance's first
 * operation. Without `url`, uses installStorage's.
 */
export declare function useArumaStorage<T extends object>(storage: T, options?: { url?: string | URL; allowInsecure?: boolean }): T;
