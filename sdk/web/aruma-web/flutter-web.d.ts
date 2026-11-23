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
  /**
   * The services to rewrite (default `['auth', 'storage']`, see DEFAULT_PRODUCTS). `firestore` is
   * SR3-10. A list without `auth` leaves Firebase Auth on Google (a warning when the project uses
   * firebase_auth); with `auth`, a product the project uses on the web must be in the list too.
   */
  products?: Array<'auth' | 'storage' | 'firestore'>;
}

export type FlutterWebProduct = 'auth' | 'storage' | 'firestore';

/** Every product, in the order the block loads them (Auth first). */
export declare const PRODUCTS: readonly FlutterWebProduct[];

/** The products of a run that names none: `['auth', 'storage']`. */
export declare const DEFAULT_PRODUCTS: readonly FlutterWebProduct[];

/** Writes web/aruma/firebase-storage-<version>.js rewritten for Aruma and patches web/index.html. */
export declare function flutterWebStorage(options: FlutterWebOptions): { version: string; file: string; index: string; host: string };

/**
 * The same with `products`: web/aruma/firebase-<product>-<version>.js for each, and `files` lists
 * them; `removed` lists the files of an earlier run it took out, `warnings` what to show the user.
 */
export declare function flutterWeb(options: FlutterWebOptions): {
  version: string;
  file: string;
  files: string[];
  products: FlutterWebProduct[];
  index: string;
  host: string;
  removed: string[];
  warnings: string[];
};

/** The products whose FlutterFire web plugin the project uses (from .dart_tool/package_config.json). */
export declare function webPlugins(project: string): Set<FlutterWebProduct>;

/** The Firebase JS SDK version the project's firebase_core_web pins. */
export declare function pinnedVersion(project: string): string;

/** web/index.html with the loader block in place of the flutter_bootstrap.js script. */
export declare function patchIndex(html: string, file: string | Array<[string, string]>): string;
