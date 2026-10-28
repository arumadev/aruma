# Aruma website

The landing page and documentation, built with [Astro Starlight](https://starlight.astro.build). The site is English only.

```bash
cd website
npm install
npm run dev      # http://localhost:4321/aruma/
npm run build    # static site in dist/
```

## How the docs get in

`../docs` stays the single source of truth. `scripts/sync-docs.mjs` runs before every dev and build. It copies the published sections (`compat`, `guides`, `security`, plus the configuration and console API references) into `src/content/docs`, adds the frontmatter Starlight needs, and rewrites relative links. Those copies are git-ignored. Release notes are not published; links to them point at GitHub.

Hand-written pages live in `src/content/docs`: the landing page (`index.mdx`) and the quick start (`docs/getting-started.md`).

## Address

The site is served from `https://arumadev.github.io/aruma/`. With a custom domain, set `SITE_URL` and an empty `SITE_BASE` in the workflow, add `public/CNAME`, and change the absolute `/aruma/...` links in the landing pages.
