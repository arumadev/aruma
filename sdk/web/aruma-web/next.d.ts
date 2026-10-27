// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

/**
 * The Next.js config with the Storage rewrite for webpack and Turbopack.
 * Not configured (`url` missing or empty): the config unchanged. Refused: throws.
 */
export declare function withArumaStorage<T extends object>(nextConfig?: T, options?: { url?: string | URL; allowInsecure?: boolean; required?: boolean }): T;

/** The same for Cloud Firestore (SR3-10), alone or after withArumaStorage. */
export declare function withArumaFirestore<T extends object>(nextConfig?: T, options?: { url?: string | URL; allowInsecure?: boolean; required?: boolean }): T;
export default withArumaStorage;
