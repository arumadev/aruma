// Prints the totals and failures of the suites run.sh ran: mocha's JSON reports for the Node
// suites, and karma-mocha-reporter's log for the browser one.
//
//   node summarize.mjs <results dir> <suite>...
import { existsSync, readFileSync } from 'node:fs';
import { join } from 'node:path';

const [dir, ...suites] = process.argv.slice(2);

function fromMochaJSON(file) {
  const r = JSON.parse(readFileSync(file, 'utf8'));
  return {
    passed: r.stats.passes,
    failed: r.stats.failures,
    skipped: r.stats.pending,
    failures: r.failures.map(f => `${f.fullTitle} :: ${firstLine(f.err.message)}`)
  };
}

// karma-mocha-reporter ends with "SUMMARY:", the counts, then "FAILED TESTS:": suite titles
// indented by depth, and per failure a "✖ test" line, the browser, the error and its stack up
// to a blank line.
function fromKarmaLog(file) {
  const text = readFileSync(file, 'utf8').replace(/\x1b\[[0-9;]*m/g, '');
  const count = re => Number((text.match(re) || [])[1] || 0);
  const out = {
    passed: count(/✔ (\d+) tests? completed/),
    failed: count(/✖ (\d+) tests? failed/),
    skipped: count(/(\d+) tests? skipped/),
    failures: []
  };
  const lines = (text.split('FAILED TESTS:')[1] || '').split('\n');
  const suites = [];
  for (let i = 0; i < lines.length; i++) {
    const m = lines[i].match(/^(\s*)(✖ )?(\S.*)$/);
    if (!m) continue;
    const depth = m[1].length;
    while (suites.length && suites[suites.length - 1].depth >= depth) suites.pop();
    if (!m[2]) {
      suites.push({ depth, title: m[3] });
      continue;
    }
    const title = [...suites.map(s => s.title), m[3]].join(' ');
    out.failures.push(`${title} :: ${firstLine((lines[i + 2] || '').trim())}`);
    while (i < lines.length && lines[i].trim() !== '') i++; // the error block
  }
  return out;
}

function firstLine(s = '') {
  return s.split('\n')[0].slice(0, 200);
}

const total = { passed: 0, failed: 0, skipped: 0 };
for (const s of suites) {
  const json = join(dir, `${s}.json`);
  const r = s === 'js-browser' ? fromKarmaLog(join(dir, `${s}.log`)) : existsSync(json) ? fromMochaJSON(json) : null;
  if (!r) {
    console.log(`${s}: no report (see ${join(dir, `${s}.log`)})`);
    continue;
  }
  console.log(`${s}: ${r.passed} passed, ${r.failed} failed, ${r.skipped} skipped`);
  for (const f of r.failures) console.log(`  ✖ ${f}`);
  for (const k of Object.keys(total)) total[k] += r[k];
}
console.log(`total: ${total.passed} passed, ${total.failed} failed, ${total.skipped} skipped`);
