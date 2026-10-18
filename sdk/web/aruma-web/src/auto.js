// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

// Side-effect entry: `import '@aruma/web/auto'` first in the app installs the redirect from
//
//   1. globalThis.__ARUMA__  ({ url, allowInsecure?, log? } or a URL string), else
//   2. import.meta.env.VITE_ARUMA_URL (with VITE_ARUMA_ALLOW_INSECURE and VITE_ARUMA_LOG), when the
//      bundler replaces import.meta.env.
//
// Not configured: nothing happens. Configured but refused: the reason is logged once and Firebase
// Auth requests fail instead of reaching Google; the import itself never throws.
import { installFromConfig } from './index.js';

function fromEnv() {
  let env;
  try {
    env = import.meta.env;
  } catch {
    return undefined;
  }
  if (!env || !env.VITE_ARUMA_URL) return undefined;
  return { url: env.VITE_ARUMA_URL, allowInsecure: env.VITE_ARUMA_ALLOW_INSECURE, log: env.VITE_ARUMA_LOG };
}

installFromConfig(globalThis.__ARUMA__ ?? fromEnv());
