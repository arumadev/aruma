// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

// The build-time Storage rewrite for every bundler unplugin covers (ADR-022 §16.1), one line in the
// bundler's config:
//
//   import { arumaStorage } from '@aruma/web/unplugin';
//   arumaStorage.webpack({ url })   arumaStorage.esbuild({ url })   arumaStorage.rollup({ url })
//   arumaStorage.rspack({ url })    arumaStorage.rolldown({ url })  (Vite: '@aruma/web/vite')
//
// The hooks and the checks that fail the build are in build.js.
import { createUnplugin } from 'unplugin';
import { storageFactory } from './build.js';

export const arumaStorage = createUnplugin(storageFactory);
export default arumaStorage;
