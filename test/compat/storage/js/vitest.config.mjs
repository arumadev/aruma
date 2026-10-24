// The vitest config the Storage compat run (../run.sh) hands to vitest for packages/storage: the
// package's own (vitest.config.mjs, over config/vitest.base.mjs, unchanged), with what the run
// adds to its browser project only. ARUMA_STORAGE_SDK names the extracted firebase-js-sdk.
//
// - The Chromium that Playwright launches resolves Firebase's API hosts to the local TLS front
//   (ARUMA_FRONT_PORT) and every other name but localhost to nothing, and trusts the front's
//   certificate by its SPKI hash (ARUMA_FRONT_SPKI) and nothing else.
// - The browser server listens on ARUMA_BROWSER_PORT, inside the task's port range.
//
// In both projects @firebase/component resolves to its source (below). Otherwise the node
// project is left as it is: there, ../hosts.cjs (NODE_OPTIONS) does the resolving.
import path from 'node:path';
import { pathToFileURL } from 'node:url';
import { playwright } from '@vitest/browser-playwright';

const sdk = process.env.ARUMA_STORAGE_SDK;
const { default: config } = await import(pathToFileURL(path.join(sdk, 'packages/storage/vitest.config.mjs')).href);

const port = process.env.ARUMA_FRONT_PORT;
const rules = [
  ...(process.env.ARUMA_TEST_HOSTS || '').split(',').filter(Boolean).map((h) => `MAP ${h} 127.0.0.1:${port}`),
  'MAP * ~NOTFOUND',
  'EXCLUDE localhost',
  'EXCLUDE 127.0.0.1',
].join(', ');

// The storage sources import @firebase/component from its source at the same commit, as the
// monorepo's workspace resolves it: they read ComponentType and InstantiationMode, const enums
// that the npm release (0.7.5, the same version) inlines at build time and does not export. The
// npm packages (app, auth) keep the release, as they do in the node project, where vitest leaves
// node_modules to Node.
const componentSource = {
  name: 'aruma-component-source',
  enforce: 'pre',
  resolveId(id, importer) {
    if (id === '@firebase/component' && importer && !importer.includes(`${path.sep}node_modules${path.sep}`)) {
      return path.join(sdk, 'packages/component/aruma-index.ts');
    }
    return null;
  },
};

for (const project of config.test.projects) {
  project.plugins = [...(project.plugins ?? []), componentSource];
  if (project.test.name !== 'browser') continue;
  project.server = { ...project.server, port: Number(process.env.ARUMA_BROWSER_PORT), strictPort: true };
  project.test.browser = {
    ...project.test.browser,
    api: { port: Number(process.env.ARUMA_BROWSER_PORT), strictPort: true },
    provider: playwright({
      launchOptions: {
        args: [
          `--host-resolver-rules=${rules}`,
          `--ignore-certificate-errors-spki-list=${process.env.ARUMA_FRONT_SPKI}`,
          '--disable-background-networking',
          '--disable-component-update',
          '--disable-domain-reliability',
          '--disable-sync',
        ],
      },
    }),
  };
}

export default config;
