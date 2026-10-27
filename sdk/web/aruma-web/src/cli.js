#!/usr/bin/env node
// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

// aruma-web flutter-web --url <aruma> [--project <dir>] [--firebase <dir>] [--allow-insecure]
//
// The Flutter web build step of flutter-web.js. Exit status 1 with the reason when it cannot do it.
import { parseArgs } from 'node:util';
import { flutterWeb } from './flutter-web.js';

const usage = 'usage: aruma-web flutter-web --url <aruma url> [--project <flutter project>] [--firebase <firebase npm package dir>] [--products storage,firestore] [--allow-insecure]';

const [command, ...rest] = process.argv.slice(2);
if (command !== 'flutter-web') {
  console.error(usage);
  process.exit(2);
}
let values;
try {
  ({ values } = parseArgs({
    args: rest,
    options: {
      url: { type: 'string', default: process.env.ARUMA_URL },
      project: { type: 'string', default: '.' },
      firebase: { type: 'string' },
      products: { type: 'string', default: 'storage' },
      'allow-insecure': { type: 'boolean', default: false },
    },
  }));
} catch (e) {
  console.error(`${e.message}\n${usage}`);
  process.exit(2);
}
if (!values.url) {
  console.error(`--url (or ARUMA_URL) is required\n${usage}`);
  process.exit(2);
}
try {
  const products = values.products.split(',').map((p) => p.trim()).filter(Boolean);
  const r = flutterWeb({ url: values.url, allowInsecure: values['allow-insecure'], project: values.project, firebase: values.firebase, products });
  console.log(`@aruma/web flutter-web: Firebase JS SDK ${r.version}, ${products.join(' and ')} on ${r.host}\n${r.files.map((f) => `  wrote ${f}\n`).join('')}  patched ${r.index}`);
} catch (e) {
  console.error(e.message);
  process.exit(1);
}
