// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

export interface FlutterWebOptions {
  /** The Aruma server's root. */
  url: string;
  /** Accept `http` to a host that is not loopback. Development only. */
  allowInsecure?: boolean;
  /** The Flutter project (default `.`). */
  project?: string;
  /** The `firebase` npm package's directory (default: resolved from the project). */
  firebase?: string;
  /** The Firebase JS SDK version (default: the one the project's firebase_core_web pins). */
  version?: string;
  /** The services to rewrite (default `['storage']`). `firestore` is SR3-10. */
  products?: Array<'storage' | 'firestore'>;
}

/** Writes web/aruma/firebase-storage-<version>.js rewritten for Aruma and patches web/index.html. */
export declare function flutterWebStorage(options: FlutterWebOptions): { version: string; file: string; index: string; host: string };

/** The same with `products`: web/aruma/firebase-<product>-<version>.js for each, and `files` lists them. */
export declare function flutterWeb(options: FlutterWebOptions): { version: string; file: string; files: string[]; index: string; host: string };

/** The Firebase JS SDK version the project's firebase_core_web pins. */
export declare function pinnedVersion(project: string): string;

/** web/index.html with the loader block in place of the flutter_bootstrap.js script. */
export declare function patchIndex(html: string, file: string | Array<[string, string]>): string;
