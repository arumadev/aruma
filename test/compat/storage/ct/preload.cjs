// Preloaded (mocha --require) into the firebase-tools conformance run, after ts-node. The test
// files stay as upstream wrote them; this replaces what stands around them in firebase-tools'
// own run, where the emulators are started by the CLI and the page is a real site:
//
// - ../../integration-helpers/framework: EmulatorEndToEndTest starts nothing. The server is
//   already up (run.sh), and the tests' emulator config (firebase.json) names the adapter.
// - ../../../src/emulator/testing/fixtures: only IMAGE_FILE_BASE64 is imported. The real module
//   pulls in the Functions emulator; run.sh extracts the constant into fixtures.json.
// - googleapis: utils.ts imports it at the top for getProdAccessToken, which only production
//   mode calls. A stub that throws if it is ever used saves installing 100 MB.
// - puppeteer: launch() gets the Chromium headless shell from .cache/ms-playwright
//   (PUPPETEER_EXECUTABLE_PATH), a resolver that sends example.com and www.gstatic.com to the
//   local front and no other name anywhere, and trust in the front's certificate only (its
//   SPKI hash, ARUMA_FRONT_SPKI). So the page loads, and the firebase-*-compat.js it adds come
//   from the npm releases of the same versions, not from the network.
// - The parts of firebase-tools' .mocharc.yml bootstrap (src/test/helpers/mocha-bootstrap.js)
//   these tests rely on: chai-as-promised, NO_COLOR, and an unhandled rejection fails the run.
'use strict';
const Module = require('node:module');
const path = require('node:path');

process.env.NO_COLOR = 'true';
require('chai').use(require('chai-as-promised'));
process.on('unhandledRejection', (error) => {
  throw error;
});

const fixtures = require(process.env.ARUMA_CT_FIXTURES);

class EmulatorEndToEndTest {
  constructor(project, workdir, config) {
    this.project = project;
    this.workdir = workdir;
    this.authEmulatorPort = config?.emulators?.auth?.port;
    this.storageEmulatorPort = config?.emulators?.storage?.port;
  }
  startEmulators() {
    return Promise.resolve();
  }
  stopEmulators() {
    return Promise.resolve();
  }
}

const googleapis = {
  google: new Proxy(
    {},
    {
      get() {
        throw new Error('googleapis is stubbed in the Aruma run: production mode is not wired');
      },
    },
  ),
};

function browserArgs() {
  const port = process.env.ARUMA_FRONT_PORT;
  const rules = [
    `MAP example.com 127.0.0.1:${port}`,
    `MAP www.gstatic.com 127.0.0.1:${port}`,
    'MAP * ~NOTFOUND',
    'EXCLUDE localhost',
    'EXCLUDE 127.0.0.1',
  ].join(', ');
  return [
    `--host-resolver-rules=${rules}`,
    `--ignore-certificate-errors-spki-list=${process.env.ARUMA_FRONT_SPKI}`,
    '--no-first-run',
    '--no-default-browser-check',
    '--disable-background-networking',
    '--disable-component-update',
    '--disable-sync',
    '--disable-domain-reliability',
  ];
}

let puppeteerPatched = null;
function patchPuppeteer(mod) {
  if (puppeteerPatched) return puppeteerPatched;
  const launch = mod.launch.bind(mod);
  const patched = (options = {}) =>
    launch({
      ...options,
      executablePath: process.env.PUPPETEER_EXECUTABLE_PATH,
      args: [...(options.args || []), ...browserArgs()],
    });
  mod.launch = patched;
  if (mod.default) mod.default.launch = patched;
  puppeteerPatched = mod;
  return mod;
}

const realLoad = Module._load;
Module._load = function load(request, parent, isMain) {
  const from = parent?.filename ? path.dirname(parent.filename) : '';
  const abs = request.startsWith('.') ? path.resolve(from, request) : request;
  if (abs.endsWith(path.join('scripts', 'integration-helpers', 'framework'))) return { EmulatorEndToEndTest };
  if (abs.endsWith(path.join('src', 'emulator', 'testing', 'fixtures'))) return fixtures;
  if (request === 'googleapis') return googleapis;
  const mod = realLoad.apply(this, arguments);
  if (request === 'puppeteer') return patchPuppeteer(mod);
  return mod;
};
