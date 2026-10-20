// Turns the mocha reports run.sh collected into one result per test (results.json), prints the
// totals per file with each failure's first error line, and compares the run with an emulator
// baseline. Then the connections the network guard refused, which must be none.
//
//   node summarize.mjs <results dir> --classified classified.json
//                      (--baseline <file> | --write-baseline <file>)
//
// A test that passes in the baseline and fails or is missing here is a regression, unless
// classified.json lists it for this target and mode. Exits 1 when there are regressions, 3 when
// a report or the baseline is missing.
import { existsSync, readFileSync, writeFileSync } from 'node:fs';
import { basename, join } from 'node:path';

const [dir, ...rest] = process.argv.slice(2);
const opts = {};
for (let i = 0; i < rest.length; i += 2) opts[rest[i].replace(/^--/, '')] = rest[i + 1];

function readJSON(file) {
  if (!existsSync(file)) {
    console.error(`missing ${file}`);
    process.exit(3);
  }
  return JSON.parse(readFileSync(file, 'utf8'));
}

function firstLine(s = '') {
  return String(s).split('\n')[0].slice(0, 200);
}

// The file of a test, relative to the firebase-js-sdk root.
function relative(file = '') {
  const i = file.indexOf('/packages/');
  return i < 0 ? file : file.slice(i + 1);
}

// fromMocha lists the tests of one mocha JSON report. Its passes, failures and pending arrays
// hold every test once, in run order, plus the failing hooks ("before each" hook for ...),
// which count as failures.
function fromMocha(suite, report) {
  const tests = [];
  const add = (t, state) =>
    tests.push({
      suite,
      file: relative(t.file),
      title: t.fullTitle,
      state,
      ...(state === 'failed' ? { error: firstLine(t.err?.message) } : {}),
      retries: t.currentRetry || 0,
      durationMs: t.duration || 0
    });
  report.passes.forEach(t => add(t, 'passed'));
  report.failures.forEach(t => add(t, 'failed'));
  report.pending.forEach(t => add(t, 'skipped'));
  return tests;
}

const key = t => `${t.file} :: ${t.title}`;

const meta = readJSON(join(dir, 'meta.json'));
const handshake = existsSync(join(dir, 'handshake.json')) ? readJSON(join(dir, 'handshake.json')) : null;
const timing = existsSync(join(dir, 'timing.json')) ? readJSON(join(dir, 'timing.json')) : {};
const suites = meta.suites || ['compat', 'exp'];
const tests = suites.flatMap(s => fromMocha(s, readJSON(join(dir, `${s}.mocha.json`))));
const classified = opts.classified ? readJSON(opts.classified).tests : [];

// classOf returns the classification of a test for this run's target and mode, if any.
function classOf(t) {
  return classified.find(
    c =>
      c.file === t.file &&
      c.title === t.title &&
      (!c.targets || c.targets.includes(meta.target)) &&
      (!c.modes || c.modes.includes(meta.mode))
  );
}

const count = list => ({
  passed: list.filter(t => t.state === 'passed').length,
  failed: list.filter(t => t.state === 'failed').length,
  skipped: list.filter(t => t.state === 'skipped').length
});
const counts = count(tests);
writeFileSync(join(dir, 'results.json'), JSON.stringify({ meta, handshake, timing, counts, tests }, null, 2) + '\n');

console.log(`${meta.target} in ${meta.mode} mode, databaseURL ${meta.databaseURL}, ${meta.targetVersion}`);
if (handshake) console.log(`handshake: ${JSON.stringify(handshake.hello)}`);
const files = [...new Set(tests.map(t => t.file))].sort();
for (const f of files) {
  const list = tests.filter(t => t.file === f);
  const c = count(list);
  const retried = list.filter(t => t.retries > 0).length;
  console.log(`${f}: ${c.passed} passed, ${c.failed} failed, ${c.skipped} skipped${retried ? `, ${retried} passed or failed after retries` : ''}`);
  for (const t of list.filter(t => t.state === 'failed')) {
    const c = classOf(t);
    console.log(`  ✖ ${t.title} :: ${t.error}${c ? `  [${c.class}]` : ''}`);
  }
  for (const t of list.filter(t => t.state === 'skipped')) console.log(`  - ${t.title}`);
}
for (const f of meta.skippedFiles || []) console.log(`${f}: not run (browser-only)`);
console.log(`total: ${counts.passed} passed, ${counts.failed} failed, ${counts.skipped} skipped`);
if (timing.suitesSeconds !== undefined) console.log(`time: suites ${timing.suitesSeconds}s, whole run ${timing.runSeconds}s`);

const guard = join(dir, 'netguard.log');
const blocked = existsSync(guard) ? readFileSync(guard, 'utf8').split('\n').filter(l => l.startsWith('BLOCK')) : [];
console.log(`network guard: ${blocked.length} connection(s) off this machine refused`);
for (const l of [...new Set(blocked.map(l => l.replace(/ pid=\d+$/, '')))]) console.log(`  ${l}`);

if (opts['write-baseline']) {
  const baseline = {
    note: 'Written by run.sh --update-baseline. Durations and retries are left out, so that a new recording only differs where a result does.',
    // The hello's version and host; its time and session id change with every run.
    meta: { ...meta, handshake: handshake && { v: handshake.hello.v, h: handshake.hello.h } },
    counts,
    tests: tests.map(({ suite, file, title, state, error }) => ({ suite, file, title, state, ...(error ? { error } : {}) }))
  };
  writeFileSync(opts['write-baseline'], JSON.stringify(baseline, null, 2) + '\n');
  console.log(`baseline written to ${opts['write-baseline']}`);
  process.exit(0);
}

if (!opts.baseline) process.exit(0);
const baseline = readJSON(opts.baseline);
const now = new Map(tests.map(t => [key(t), t]));
const regressions = [];
const excused = [];
const ran = b => suites.includes(b.suite);
for (const b of baseline.tests.filter(b => ran(b) && b.state === 'passed')) {
  const t = now.get(key(b)) || { ...b, state: 'missing', error: 'not run' };
  if (t.state === 'passed' || t.state === 'skipped') continue;
  const c = classOf(t);
  (c ? excused : regressions).push({ t, c });
}
const fixed = baseline.tests.filter(b => ran(b) && b.state === 'failed' && now.get(key(b))?.state === 'passed');
const passedThere = baseline.tests.filter(b => ran(b) && b.state === 'passed').length;
console.log(`\nagainst ${basename(opts.baseline)} (${passedThere} of these suites' tests pass there):`);
console.log(`  ${regressions.length} regression(s): pass on the emulator, fail here, not classified`);
for (const { t } of regressions) console.log(`  ✖ ${key(t)} :: ${t.error}`);
console.log(`  ${excused.length} classified difference(s)`);
for (const { t, c } of excused) console.log(`  ~ ${key(t)}  [${c.class}]`);
if (fixed.length) console.log(`  ${fixed.length} test(s) fail on the emulator and pass here`);
for (const b of fixed) console.log(`  + ${key(b)}`);
process.exit(regressions.length ? 1 : 0);
