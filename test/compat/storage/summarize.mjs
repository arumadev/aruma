// Prints the Storage compat run's results (run.sh calls it last) and writes them, one entry per
// test, to <results>/results.json:
//
//   node summarize.mjs <results> <classified.json> <suite>...
//
// A test that does not pass is classified in classified.json under "<suite> | <full title>",
// with one of the three classes of docs/compat/storage-v0.3.md:
//   a  a bug in Aruma (fixed with a test, or listed)
//   b  a difference listed in ADR-023 §3 (item names the number)
//   c  a bug in the emulator, with the evidence that production differs
// The conformance tests firebase-tools marks emulatorOnly run as well, and are reported apart:
// they are outside the count (ARUMA_CT_SOURCES names their sources). It exits 1 when a test
// fails without a class, or a classified test is missing from the run.
import fs from 'node:fs';
import path from 'node:path';

const [dir, classifiedFile, ...suites] = process.argv.slice(2);
const classified = JSON.parse(fs.readFileSync(classifiedFile, 'utf8')).tests ?? {};

function emulatorOnlyTitles() {
  const src = process.env.ARUMA_CT_SOURCES;
  const out = new Set();
  if (!src || !fs.existsSync(src)) return out;
  for (const f of fs.readdirSync(src).filter((f) => f.endsWith('.test.ts'))) {
    const s = fs.readFileSync(path.join(src, f), 'utf8');
    for (const m of s.matchAll(/emulatorOnly\.it\(\s*(["'`])((?:(?!\1).)+)\1/gs)) out.add(`${f}|${m[2].replace(/\s+/g, ' ')}`);
  }
  return out;
}
const emulatorOnly = emulatorOnlyTitles();

function firstLine(s) {
  return String(s ?? '').split('\n').find((l) => l.trim()) ?? '';
}

// read turns a vitest or mocha JSON report into entries.
function read(suite) {
  const file = path.join(dir, `${suite}.json`);
  if (!fs.existsSync(file)) return null;
  const r = JSON.parse(fs.readFileSync(file, 'utf8'));
  const out = [];
  if (r.testResults) {
    for (const f of r.testResults) {
      for (const t of f.assertionResults) {
        const state = t.status === 'passed' ? 'passed' : t.status === 'failed' ? 'failed' : 'skipped';
        out.push({ suite, file: path.basename(f.name), title: [...t.ancestorTitles, t.title].join(' '), state, error: firstLine(t.failureMessages?.[0]) });
      }
      if (f.status === 'failed' && f.assertionResults.length === 0) out.push({ suite, file: path.basename(f.name), title: '(file)', state: 'failed', error: firstLine(f.message) });
    }
  } else {
    const failed = new Map(r.failures.map((t) => [t.fullTitle, t]));
    const pending = new Set(r.pending.map((t) => t.fullTitle));
    for (const t of r.tests) {
      const state = failed.has(t.fullTitle) ? 'failed' : pending.has(t.fullTitle) ? 'skipped' : 'passed';
      out.push({ suite, file: path.basename(t.file ?? ''), title: t.fullTitle, state, error: firstLine(failed.get(t.fullTitle)?.err?.message) });
    }
    // A hook that fails is a failure without a test of its own.
    for (const t of r.failures) if (!r.tests.some((x) => x.fullTitle === t.fullTitle)) out.push({ suite, file: path.basename(t.file ?? ''), title: t.fullTitle, state: 'failed', error: firstLine(t.err?.message) });
  }
  for (const e of out) {
    e.emulatorOnly = [...emulatorOnly].some((k) => {
      const [f, t] = k.split('|');
      return f === e.file && e.title.endsWith(t);
    });
    const c = classified[`${suite} | ${e.title}`];
    if (c) e.class = c;
  }
  return out;
}

const all = [];
let bad = 0;
for (const suite of suites.filter((s) => s !== 'captures')) {
  const entries = read(suite);
  if (!entries) {
    console.log(`\n${suite}: no report`);
    bad++;
    continue;
  }
  all.push(...entries);
  const counted = entries.filter((e) => !e.emulatorOnly);
  const n = (st) => counted.filter((e) => e.state === st).length;
  const byClass = {};
  for (const e of counted.filter((e) => e.state === 'failed')) byClass[e.class?.class ?? '?'] = (byClass[e.class?.class ?? '?'] ?? 0) + 1;
  console.log(`\n${suite}: ${counted.length} tests, ${n('passed')} passed, ${n('failed')} failed (${Object.entries(byClass).map(([k, v]) => `${k}: ${v}`).join(', ') || 'none'}), ${n('skipped')} skipped`);
  for (const e of counted.filter((e) => e.state === 'failed')) {
    const c = e.class ? `[${e.class.class}${e.class.item ? ' ' + e.class.item : ''}]` : '[UNCLASSIFIED]';
    if (!e.class) bad++;
    console.log(`  ${c} ${e.file}: ${e.title}\n      ${e.error}`);
  }
  for (const e of counted.filter((e) => e.state === 'passed' && e.class)) console.log(`  [passes now, still classified ${e.class.class}] ${e.title}`);
  const eo = entries.filter((e) => e.emulatorOnly);
  if (eo.length) {
    console.log(`  emulatorOnly (run, not counted): ${eo.map((e) => `${e.state} "${e.title}"`).join('; ')}`);
  }
}
for (const key of Object.keys(classified)) {
  const [suite, title] = key.split(' | ');
  if (suites.includes(suite) && !all.some((e) => e.suite === suite && e.title === title)) {
    console.log(`  [missing from the run] ${key}`);
    bad++;
  }
}
const guard = path.join(dir, 'netguard.log');
const blocked = fs.existsSync(guard) ? fs.readFileSync(guard, 'utf8').split('\n').filter((l) => l.startsWith('BLOCK')) : [];
console.log(`\nnetwork: ${blocked.length} connections refused off this machine${blocked.length ? '\n  ' + [...new Set(blocked.map((l) => l.replace(/ pid=\d+$/, '')))].join('\n  ') : ''}`);
if (suites.includes('captures') && fs.existsSync(path.join(dir, 'captures-diff.txt'))) {
  console.log('\n' + fs.readFileSync(path.join(dir, 'captures-diff.txt'), 'utf8'));
}
fs.writeFileSync(path.join(dir, 'results.json'), JSON.stringify(all, null, 2) + '\n');
process.exit(bad ? 1 : 0);
