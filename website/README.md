# Aruma website

The landing page and documentation, built with [Astro Starlight](https://starlight.astro.build). The site is English only.

```bash
cd website
npm install
npm run dev      # http://localhost:4321/aruma/
npm run build    # static site in dist/
```

## How the docs get in

`../docs` stays the single source of truth. `scripts/sync-docs.mjs` runs before every dev and build. It copies the published pages (`guides/`, `reference/`, and getting started, SDK packages, architecture, security, testing, benchmarks and the changelog) into `src/content/docs`, adds the frontmatter Starlight needs, and rewrites relative links. Those copies are git-ignored.  Links to files that are not published (for example `docs/README.md`) point at GitHub.

The only hand-written page is the landing page, `src/content/docs/index.mdx`.

## Address

The site is served from `https://arumadev.github.io/aruma/`. With a custom domain, set `SITE_URL` and an empty `SITE_BASE` in the workflow, add `public/CNAME`, and change the absolute `/aruma/...` links in the landing pages.
