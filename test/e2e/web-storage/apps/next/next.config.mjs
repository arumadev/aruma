import { withArumaStorage } from '@aruma/web/next';

// measure.sh's Next.js app: the one line, for `next build --webpack` and `next build` (Turbopack).
// ARUMA_E2E_DIST keeps the two builds apart; /e2e.json comes from the page server.
export default withArumaStorage(
  {
    distDir: process.env.ARUMA_E2E_DIST ?? '.next',
    async rewrites() {
      return [{ source: '/e2e.json', destination: `http://127.0.0.1:${process.env.ARUMA_E2E_PAGE_PORT ?? 9421}/e2e.json` }];
    },
  },
  { url: process.env.ARUMA_URL },
);
