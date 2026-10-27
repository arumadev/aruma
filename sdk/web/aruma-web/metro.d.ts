// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

/**
 * Returns the Metro config with the Storage rewrite in front of its Babel transformer.
 * Not configured (`url` missing or empty): the config unchanged. Refused: throws.
 */
export declare function withArumaStorage<T extends object>(config: T, options?: { url?: string | URL; allowInsecure?: boolean }): T;

/** The same for Cloud Firestore (SR3-10), alone or on the config withArumaStorage returned. */
export declare function withArumaFirestore<T extends object>(config: T, options?: { url?: string | URL; allowInsecure?: boolean }): T;

/** Metro's Babel transformer entry (set by withArumaStorage). */
export declare function transform(args: { filename: string; src: string; [key: string]: unknown }): unknown;

/** Part of Metro's transform cache key. */
export declare function getCacheKey(...args: unknown[]): string;
