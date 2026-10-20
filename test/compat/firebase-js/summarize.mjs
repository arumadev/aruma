// Prints the totals, failures and skipped tests of the suites run.sh ran: mocha's JSON reports
// for the Node suites, and karma-mocha-reporter's log for the browser one. Then the connections
// the network guard refused, which must be none.
//
//   node summarize.mjs <results dir> <suite>...
import { existsSync, readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';

const [dir, ...suites] = process.argv.slice(2);

// fromMochaJSON reads <suite>.json, or the <suite>.<part>.json files of a suite run in parts.
function fromMochaJSON(suite) {
  const files = readdirSync(dir).filter(f => f === `${suite}.json` || (f.startsWith(`${suite}.`) && f.endsWith('.json')));
  if (files.length === 0) return null;
  const out = { passed: 0, failed: 0, skipped: 0, failures: [], skips: [] };
  for (const f of files.sort()) {
    const r = JSON.parse(readFileSync(join(dir, f), 'utf8'));
    out.passed += r.stats.passes;
    out.failed += r.stats.failures;
    out.skipped += r.stats.pending;
    out.failures.push(...r.failures.map(f => `${f.fullTitle} :: ${firstLine(f.err.message)}`));
    out.skips.push(...r.pending.map(p => p.fullTitle));
  }
  return out;
}

// karma-mocha-reporter prints the tree first, suite titles indented by depth and a skipped test
// as "✖ title (skipped)"; then "SUMMARY:", the counts, and "FAILED TESTS:" with, per failure, a
// "✖ test" line, the browser, the error and its stack up to a blank line.
function fromKarmaLog(file) {
  const text = readFileSync(file, 'utf8').replace(/\x1b\[[0-9;]*m/g, '');
  const count = re => Number((text.match(re) || [])[1] || 0);
  const [tree, summary = ''] = text.split('SUMMARY:');
  const out = {
    passed: count(/✔ (\d+) tests? completed/),
    failed: count(/✖ (\d+) tests? failed/),
    skipped: count(/(\d+) tests? skipped/),
    failures: [],
    skips: []
  };
  walk(tree.split('\n'), (title, lines, i) => {
    if (title.endsWith(' (skipped)')) out.skips.push(title.slice(0, -' (skipped)'.length));
    return i;
  });
  walk((summary.split('FAILED TESTS:')[1] || '').split('\n'), (title, lines, i) => {
    out.failures.push(`${title} :: ${firstLine((lines[i + 2] || '').trim())}`);
    while (i < lines.length && lines[i].trim() !== '') i++; // the error block
    return i;
  });
  return out;
}

// walk follows the indented tree and calls onTest(full title, lines, index) for each "✖" line.
function walk(lines, onTest) {
  const stack = [];
  for (let i = 0; i < lines.length; i++) {
    const m = lines[i].match(/^(\s+)(✖ |✔ )?(\S.*)$/);
    if (!m || /^(LOG|WARN|ERROR):/.test(m[3]) || /^Chrome /.test(m[3])) continue;
    const depth = Math.floor(m[1].length / 2); // a title may start with a space
    while (stack.length && stack[stack.length - 1].depth >= depth) stack.pop();
    if (!m[2]) {
      stack.push({ depth, title: m[3] });
      continue;
    }
    if (m[2] === '✖ ') i = onTest([...stack.map(s => s.title), m[3]].join(' '), lines, i);
  }
}

function firstLine(s = '') {
  return s.split('\n')[0].slice(0, 200);
}

const total = { passed: 0, failed: 0, skipped: 0 };
for (const s of suites) {
  const r = s === 'js-browser' ? fromKarmaLog(join(dir, `${s}.log`)) : fromMochaJSON(s);
  if (!r) {
    console.log(`${s}: no report (see ${join(dir, `${s}.log`)})`);
    continue;
  }
  console.log(`${s}: ${r.passed} passed, ${r.failed} failed, ${r.skipped} skipped`);
  for (const f of r.failures) console.log(`  ✖ ${f}`);
  for (const t of r.skips) console.log(`  - ${t}`);
  for (const k of Object.keys(total)) total[k] += r[k];
}
console.log(`total: ${total.passed} passed, ${total.failed} failed, ${total.skipped} skipped`);

const guard = join(dir, 'netguard.log');
const blocked = existsSync(guard) ? readFileSync(guard, 'utf8').split('\n').filter(l => l.startsWith('BLOCK')) : [];
console.log(`network guard: ${blocked.length} connection(s) off this machine refused`);
for (const l of [...new Set(blocked.map(l => l.replace(/ pid=\d+$/, '')))]) console.log(`  ${l}`);
