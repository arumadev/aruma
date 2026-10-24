// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

import type { ArumaBase } from './index.js';

export interface ArumaViteOptions {
  /** The Aruma server, e.g. `https://aruma.example.com`. Missing or empty: the plugin does nothing. */
  url?: string;
  /** Accept `http` to a host that is not loopback. Development only. */
  allowInsecure?: boolean;
  /** mode 'fetch': log each rewrite in the browser console (method, host and path only). */
  log?: boolean;
  /**
   * 'fetch' (default): inject the fetch redirect before the app's scripts.
   * 'build': rewrite the host literals inside @firebase/auth at build time (Vite 8+), no runtime code.
   */
  mode?: 'fetch' | 'build';
  /**
   * Also rewrite Firebase Storage's host at build time (in `vite build`, SSR and the dev server), so
   * its requests and its download links are on Aruma. The URL must then be the root of Aruma's host.
   * `{ required: false }`: do not fail a browser build that has no @firebase/storage module.
   */
  storage?: boolean | { required?: boolean };
}

/** A Vite plugin (typed loosely so that this package does not depend on vite). */
export interface ArumaVitePlugin {
  name: string;
  [hook: string]: unknown;
}

/** One plugin, or with `storage` two (Auth's and Storage's); Vite flattens the array. */
export default function aruma(options?: ArumaViteOptions): ArumaVitePlugin | ArumaVitePlugin[];
export { aruma };

/** The build-time rewrite of one @firebase/auth module. */
export declare function rewriteAuthLiterals(code: string, base: ArumaBase): { code: string; hosts: number; scheme: number };
