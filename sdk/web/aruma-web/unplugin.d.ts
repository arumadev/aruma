// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

import type { UnpluginInstance } from 'unplugin';

export interface ArumaStorageOptions {
  /** The Aruma server's root, e.g. `https://aruma.example.com`. Missing or empty: the plugin does nothing. */
  url?: string | URL;
  /** Accept `http` to a host that is not loopback. Development only. */
  allowInsecure?: boolean;
  /**
   * Fail a browser build that has no @firebase/storage module (default true). Server builds and
   * watch mode never fail for this.
   */
  required?: boolean;
  /** Log each rewritten module. */
  log?: boolean;
}

/** The build-time Storage rewrite: `.webpack()`, `.rspack()`, `.esbuild()`, `.rollup()`, `.rolldown()`. */
export declare const arumaStorage: UnpluginInstance<ArumaStorageOptions | undefined, false>;
export default arumaStorage;
