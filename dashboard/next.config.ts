import type { NextConfig } from 'next';

// The dashboard is a static export embedded in the Aruma binary and served from the same
// origin as /console/v1/. Rewrites do not exist in a static export, so the API proxy is only
// added to `next dev`, where it forwards to a locally running `aruma serve`.
const isDev = process.env.NODE_ENV === 'development';
const devApi = process.env.ARUMA_DEV_API ?? 'http://127.0.0.1:18081';

const config: NextConfig = {
  output: 'export',
  trailingSlash: true,
  reactStrictMode: true,
  poweredByHeader: false,
  productionBrowserSourceMaps: false,
  images: { unoptimized: true },
  ...(isDev && {
    rewrites: async () => [{ source: '/console/v1/:path*', destination: `${devApi}/console/v1/:path*` }],
  }),
};

export default config;
