import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';

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
      defaultLocale: 'root',
      locales: {
        root: { label: 'العربية', lang: 'ar', dir: 'rtl' },
        en: { label: 'English', lang: 'en' },
      },
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
          label: 'ابدأ',
          translations: { en: 'Start' },
          items: [{ slug: 'docs/getting-started' }],
        },
        {
          label: 'الأدلة',
          translations: { en: 'Guides' },
          items: [{ autogenerate: { directory: 'docs/guides' } }],
        },
        {
          label: 'المرجع',
          translations: { en: 'Reference' },
          items: [
            { label: 'الإعداد (بالإنجليزية)', translations: { en: 'Configuration' }, link: `${site}${base}/en/docs/reference/configuration/` },
            { label: 'واجهة اللوحة API (بالإنجليزية)', translations: { en: 'Console API' }, link: `${site}${base}/en/docs/reference/console-api/` },
          ],
        },
        {
          label: 'تقارير التوافق',
          translations: { en: 'Compatibility reports' },
          items: [{ autogenerate: { directory: 'docs/compat' } }],
        },
        {
          label: 'الأمان',
          translations: { en: 'Security' },
          items: [{ autogenerate: { directory: 'docs/security' } }],
        },
        {
          label: 'القرارات المعمارية',
          translations: { en: 'Architecture decisions' },
          items: [{ autogenerate: { directory: 'docs/adr', collapsed: true } }],
        },
      ],
    }),
  ],
});
