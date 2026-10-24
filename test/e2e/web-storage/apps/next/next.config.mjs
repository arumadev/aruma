import { withArumaStorage } from '@aruma/web/next';

// measure.sh's Next.js app: the one line, for `next build --webpack` and `next build` (Turbopack).
// ARUMA_E2E_DIST keeps the builds apart; /e2e.json comes from the page server.
// ARUMA_E2E_NO_TRANSPILE=1 takes back the transpilePackages the line adds, to show what it is for:
// the Pages Router's server then loads the SDK from node_modules, unrewritten.
const config = withArumaStorage(
  {
    distDir: process.env.ARUMA_E2E_DIST ?? '.next',
    async rewrites() {
      return [{ source: '/e2e.json', destination: `http://127.0.0.1:${process.env.ARUMA_E2E_PAGE_PORT ?? 9421}/e2e.json` }];
    },
  },
  { url: process.env.ARUMA_URL },
);
if (process.env.ARUMA_E2E_NO_TRANSPILE === '1') delete config.transpilePackages;
export default config;
