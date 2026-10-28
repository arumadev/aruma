import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';
import lucode from 'lucode-starlight';

// Served from GitHub Pages at https://arumadev.github.io/aruma/ until a custom domain exists.
// With a domain: set SITE_URL to it and SITE_BASE to an empty string (and add public/CNAME).
const site = process.env.SITE_URL ?? 'https://arumadev.github.io';
const base = (process.env.SITE_BASE ?? '/aruma').replace(/\/$/, '');
const ogImage = `${site}${base}/social-preview.png`;

export default defineConfig({
  site,
  base: base || '/',
  trailingSlash: 'always',
  integrations: [
    starlight({
      title: 'Aruma',
      description: 'A self-hosted backend that speaks the official Firebase SDKs. One Go binary and PostgreSQL.',
      logo: { src: './src/assets/logo.svg', alt: 'Aruma' },
      favicon: '/favicon.svg',
      social: [{ icon: 'github', label: 'GitHub', href: 'https://github.com/arumadev/aruma' }],
      plugins: [
        lucode({
          navLinks: [
            { label: 'Docs', link: `${base}/docs/getting-started/` },
            { label: 'Guides', link: `${base}/docs/guides/auth-web/` },
            { label: 'Reference', link: `${base}/docs/reference/compatibility/` },
            { label: 'Changelog', link: `${base}/docs/changelog/` },
          ],
          footerText: 'Aruma is open source under Apache 2.0. Firebase is a trademark of Google LLC.',
        }),
      ],
      customCss: ['./src/styles/brand.css'],
      lastUpdated: false,
      head: [
        { tag: 'meta', attrs: { property: 'og:image', content: ogImage } },
        { tag: 'meta', attrs: { property: 'og:image:width', content: '1280' } },
        { tag: 'meta', attrs: { property: 'og:image:height', content: '640' } },
        { tag: 'meta', attrs: { name: 'twitter:card', content: 'summary_large_image' } },
        { tag: 'meta', attrs: { name: 'twitter:image', content: ogImage } },
      ],
      sidebar: [
        {
          label: 'Start',
          items: [{ slug: 'docs/getting-started' }, { slug: 'docs/sdks' }, { slug: 'docs/changelog' }],
        },
        { label: 'Guides', items: [{ autogenerate: { directory: 'docs/guides' } }] },
        { label: 'Reference', items: [{ autogenerate: { directory: 'docs/reference' } }] },
        {
          label: 'How it works',
          items: [
            { slug: 'docs/architecture' },
            { slug: 'docs/security' },
            { slug: 'docs/testing' },
            { slug: 'docs/benchmarks' },
          ],
        },
      ],
    }),
  ],
});
