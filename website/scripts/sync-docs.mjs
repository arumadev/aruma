// Copies the public documentation from ../docs into the Starlight content tree.
// ../docs stays the single source of truth; the copies are generated and git-ignored.
//
//  - adds frontmatter (title from the first H1, description, edit link)
//  - rewrites relative links so they work on the site, or point at GitHub
//  - copies the images the landing pages use
import { promises as fs, existsSync, statSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const siteDir = path.resolve(here, '..');
const repoDir = path.resolve(siteDir, '..');
const docsDir = path.join(repoDir, 'docs');
const outDir = path.join(siteDir, 'src/content/docs');
const BASE = (process.env.SITE_BASE ?? '/aruma').replace(/\/$/, '');
const GH = 'https://github.com/arumadev/aruma';

// Published sections and single pages. docs/README.md is the index on GitHub and is not published.
const SECTIONS = ['guides', 'reference'];
const SINGLES = {
  'getting-started.md': 'getting-started',
  'architecture.md': 'architecture',
  'sdks.md': 'sdks',
  'benchmarks.md': 'benchmarks',
  'testing.md': 'testing',
  'security.md': 'security',
  'CHANGELOG.md': 'changelog',
};
const ORDER = {
  'getting-started.md': 1, 'sdks.md': 2, 'architecture.md': 3, 'security.md': 4, 'testing.md': 5,
  'benchmarks.md': 6, 'CHANGELOG.md': 7,
  'guides/auth-web.md': 1, 'guides/auth-admin.md': 2, 'guides/realtime-database.md': 3,
  'guides/storage.md': 4, 'guides/security-rules.md': 5, 'guides/firestore.md': 6,
  'guides/firestore-migration.md': 7, 'guides/mobile-apps.md': 8, 'guides/operations.md': 9,
  'reference/configuration.md': 1, 'reference/compatibility.md': 2, 'reference/known-differences.md': 3,
  'reference/console-api.md': 4,
};

// Short sidebar labels; the page keeps its full title.
const LABEL = {
  'guides/auth-web.md': 'Auth on the web', 'guides/auth-admin.md': 'Auth on your server',
  'guides/realtime-database.md': 'Realtime Database', 'guides/storage.md': 'Cloud Storage',
  'guides/security-rules.md': 'Security rules', 'guides/firestore.md': 'Firestore',
  'guides/firestore-migration.md': 'Moving from Firestore', 'guides/mobile-apps.md': 'Mobile apps',
  'guides/operations.md': 'Operations', 'reference/console-api.md': 'Console API',
  'reference/known-differences.md': 'Known differences', 'getting-started.md': 'Getting started',
  'sdks.md': 'SDK packages',
};

const posix = (p) => p.split(path.sep).join('/');

// 1. Collect the files.
const files = []; // { rel, abs, slug }
async function walk(dir) {
  for (const e of await fs.readdir(dir, { withFileTypes: true })) {
    const abs = path.join(dir, e.name);
    if (e.isDirectory()) await walk(abs);
    else if (e.name.endsWith('.md')) files.push(abs);
  }
}
const picked = [];
for (const s of SECTIONS) await walk(path.join(docsDir, s));
for (const abs of files) picked.push(posix(path.relative(docsDir, abs)));
for (const rel of Object.keys(SINGLES)) picked.push(rel);

const entries = new Map();
for (const rel of picked) {
  const text = await fs.readFile(path.join(docsDir, rel), 'utf8');
  let slug = SINGLES[rel] ? SINGLES[rel] : rel.replace(/\.md$/, '').toLowerCase();
  slug = slug.replace(/(^|\/)readme$/, '$1index').replace(/\./g, ''); // Astro's slugger drops dots: review-v0.1 -> review-v01
  slug = 'docs/' + slug;
  entries.set(rel, { rel, text, slug });
}

const urlFor = (e) => {
  const slug = e.slug.replace(/(^|\/)index$/, '$1').replace(/\/$/, '');
  return `${BASE}/${slug}/`;
};

// 2. Rewrite links outside fenced code blocks.
function rewriteLinks(text, fromRel) {
  const fromDir = path.posix.dirname(fromRel);
  let fenced = false;
  return text.split('\n').map((line) => {
    if (/^\s*(```|~~~)/.test(line)) { fenced = !fenced; return line; }
    if (fenced) return line;
    return line.replace(/\]\(([^)\s]+)\)/g, (m, target) => {
      if (/^([a-z][a-z0-9+.-]*:|#|\/)/i.test(target)) return m;
      const [filePart, hash = ''] = target.split('#');
      if (!filePart) return m;
      const resolved = path.posix.normalize(path.posix.join(fromDir, decodeURIComponent(filePart)));
      const anchor = hash ? '#' + hash : '';
      const hit = entries.get(resolved);
      if (hit) return `](${urlFor(hit)}${anchor})`;
      const abs = path.join(docsDir, resolved);
      if (!resolved.startsWith('..')) {
        if (existsSync(abs)) {
          const kind = statSync(abs).isDirectory() ? 'tree' : 'blob';
          return `](${GH}/${kind}/main/docs/${resolved}${anchor})`;
        }
      } else {
        const repoRel = path.posix.normalize(path.posix.join('docs', resolved));
        const absRepo = path.join(repoDir, repoRel);
        if (existsSync(absRepo)) {
          const kind = statSync(absRepo).isDirectory() ? 'tree' : 'blob';
          return `](${GH}/${kind}/main/${repoRel}${anchor})`;
        }
      }
      return m;
    });
  }).join('\n');
}

const plain = (s) => s
  .replace(/`([^`]*)`/g, '$1').replace(/\[([^\]]*)\]\([^)]*\)/g, '$1')
  .replace(/[*_>#]/g, '').replace(/\s+/g, ' ').trim();

// The page's first block, when it is a plain paragraph: its text and where it ends.
function leadParagraph(body) {
  const m = body.match(/^\s*([^\n][\s\S]*?)(\n\s*\n|$)/);
  if (!m) return undefined;
  const t = m[1].trim();
  if (/^(#|\||```|-|\*|\d+\.|>|<|:::|import )/.test(t)) return undefined;
  const text = plain(t);
  if (text.length < 20) return undefined;
  return { text, end: m[0].length };
}


// 3. Write the generated files (start clean so renamed or removed docs do not linger).
for (const d of ['docs/compat', 'docs/guides', 'docs/security', 'docs/reference']) {
  await fs.rm(path.join(outDir, d), { recursive: true, force: true });
}
for (const slug of Object.values(SINGLES)) {
  await fs.rm(path.join(outDir, 'docs', slug + '.md'), { force: true });
}
const generated = [];
for (const e of entries.values()) {
  let body = e.text.replace(/^﻿/, '');
  const h1 = body.match(/^#\s+(.+)\s*$/m);
  const title = h1 ? plain(h1[1]) : path.posix.basename(e.rel, '.md');
  if (h1) body = body.replace(h1[0], '').replace(/^\s+/, '');
  body = rewriteLinks(body, e.rel);
  const fm = [
    '---',
    `title: ${JSON.stringify(title)}`,
  ];
  // The theme shows the description as the page's lead under the title. When the page opens with
  // a paragraph, that paragraph becomes the lead and leaves the body, so it is not shown twice.
  const lead = leadParagraph(body);
  if (lead) {
    body = body.slice(lead.end).replace(/^\s+/, '');
    fm.push(`description: ${JSON.stringify(lead.text)}`);
  }
  fm.push(`editUrl: ${GH}/edit/main/docs/${e.rel}`);
  if (ORDER[e.rel] || LABEL[e.rel]) {
    fm.push('sidebar:');
    if (LABEL[e.rel]) fm.push(`  label: ${JSON.stringify(LABEL[e.rel])}`);
    if (ORDER[e.rel]) fm.push(`  order: ${ORDER[e.rel]}`);
  }
  fm.push('---', '');
  const dest = path.join(outDir, e.slug + '.md');
  await fs.mkdir(path.dirname(dest), { recursive: true });
  await fs.writeFile(dest, fm.join('\n') + body.replace(/\s*$/, '\n'));
  generated.push(posix(path.relative(siteDir, dest)));
}

// 4. Images used by the landing pages.
const assetsOut = path.join(siteDir, 'src/assets');
await fs.mkdir(assetsOut, { recursive: true });
for (const f of ['logo.svg', 'console-auth.png', 'console-rtdb.png', 'console-storage.png']) {
  await fs.copyFile(path.join(docsDir, 'assets', f), path.join(assetsOut, f));
}

// Favicon and social preview.
const publicOut = path.join(siteDir, 'public');
await fs.mkdir(publicOut, { recursive: true });
await fs.copyFile(path.join(docsDir, 'assets', 'logo.svg'), path.join(publicOut, 'favicon.svg'));
await fs.copyFile(path.join(docsDir, 'assets', 'social-preview.png'), path.join(publicOut, 'social-preview.png'));

console.log(`synced ${generated.length} docs`);
