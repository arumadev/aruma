// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

/** The hosts that are rewritten: `identitytoolkit.googleapis.com` and `securetoken.googleapis.com`. */
export declare const AUTH_HOSTS: readonly string[];

/** An Aruma URL that was refused. The message never contains the URL itself. */
export declare class ArumaConfigError extends Error {
  name: 'ArumaConfigError';
}

export interface InstallOptions {
  /** The Aruma server, e.g. `https://aruma.example.com`. Missing or empty: install does nothing. */
  url?: string | URL;
  /** Accept `http` to a host that is not loopback. Development only. */
  allowInsecure?: boolean;
  /** Log each rewrite (method, host and path; never the query, headers or body). */
  log?: boolean;
}

/**
 * Rewrites Firebase Auth requests to the Aruma server. Wraps `globalThis.fetch` once; a second call
 * only updates the settings.
 *
 * @returns true when active, false when `url` is missing or empty (nothing is installed).
 * @throws ArumaConfigError when `url` is refused. Auth requests then fail instead of reaching Google.
 */
export declare function install(options?: InstallOptions): boolean;

/** Like install, but a refused URL is logged and returns false instead of throwing. */
export declare function installFromConfig(config: InstallOptions | string | URL | null | undefined): boolean;

/** Removes the redirect, restoring the original fetch when nothing has wrapped it since. */
export declare function uninstall(): void;

/** `{ url }` when active, `{ blocked }` when configured but refused, null when not installed. */
export declare function status(): { url: string } | { blocked: string } | null;

export interface ArumaBase {
  origin: string;
  prefix: string;
  href: string;
  protocol: string;
  host: string;
}

/** Validates an Aruma URL. @throws ArumaConfigError */
export declare function parseBase(url: string | URL, options?: { allowInsecure?: boolean }): ArumaBase;

/** True for `localhost`, `127.0.0.0/8` and `::1`. */
export declare function isLoopback(hostname: string): boolean;

/** The Auth endpoint a URL names (https, default port, one of AUTH_HOSTS), or null. */
export declare function matchAuth(url: string | URL): { host: string; path: string; search: string } | null;

/** `https://<auth host><path>?<query>` → `<base>/<auth host><path>?<query>`, or null when not an Auth URL. */
export declare function rewriteURL(url: string | URL, base: string | ArumaBase): string | null;

declare global {
  /** Read by `@aruma/web/auto`. */
  var __ARUMA__: InstallOptions | string | undefined;
}
