// Compares a vitest JSON report with the emulator's baseline and classified.json, and prints the
// totals and every test that passes on the emulator and does not pass here.
//
//   node summarize.mjs <report.json> <baseline.json> <classified.json> <suite> [--update-baseline]
//
// A test of the baseline that failed there too is not compared. A test that fails here and is
// listed in classified.json (by suite, file and full name, for this target) is printed with its
// class and not counted as unexplained. Exit status 1: an unexplained failure, or a classified
// test that no longer fails.
import { readFileSync, writeFileSync, existsSync } from 'node:fs';

const [reportFile, baselineFile, classifiedFile, suite, flag] = process.argv.slice(2);
const report = JSON.parse(readFileSync(reportFile, 'utf8'));

// A test of database.test.ts is named after the current time ("set/update should reject: Mon Nov
// 09 2026 16:14:41 GMT+0000 (Coordinated Universal Time)"); the time is cut out of the name.
const stamp = (name) => name.replace(/\w{3} \w{3} \d{2} \d{4} \d{2}:\d{2}:\d{2} GMT[+-]\d{4} \([^)]*\)/g, '<date>');

const results = new Map();
for (const f of report.testResults) {
  const file = f.name.replace(/^.*\/packages\/firestore\//, '');
  for (const t of f.assertionResults) results.set(`${suite} :: ${file} :: ${stamp(t.fullName)}`, { status: t.status, message: (t.failureMessages || []).join(' ') });
}
const count = (m) => {
  const c = { passed: 0, failed: 0, skipped: 0 };
  for (const v of m.values()) c[v.status === 'passed' ? 'passed' : v.status === 'failed' ? 'failed' : 'skipped']++;
  return c;
};
const total = count(results);
console.log(`${suite}: ${total.passed} passed, ${total.failed} failed, ${total.skipped} skipped`);

if (flag === '--update-baseline') {
  const old = existsSync(baselineFile) ? JSON.parse(readFileSync(baselineFile, 'utf8')) : { tests: {} };
  for (const [k, v] of results) old.tests[k] = v.status;
  old.note = 'Status of every test of the integration and lite suites on the Firestore emulator (cloud-firestore-emulator), by "suite :: file :: full name".';
  writeFileSync(baselineFile, JSON.stringify({ note: old.note, tests: Object.fromEntries(Object.entries(old.tests).sort()) }, null, 1) + '\n');
  console.log(`baseline written: ${Object.keys(old.tests).length} tests`);
  process.exit(0);
}

const baseline = JSON.parse(readFileSync(baselineFile, 'utf8')).tests;
const classified = new Map(JSON.parse(readFileSync(classifiedFile, 'utf8')).tests.map((t) => [`${t.suite} :: ${t.file} :: ${t.title}`, t]));
let bad = 0;
const seen = new Set();
for (const [k, v] of results) {
  if (v.status !== 'failed' || baseline[k] === 'failed') continue;
  const c = classified.get(k);
  if (c) {
    seen.add(k);
    console.log(`  classified [${c.class}] ${k}`);
  } else {
    bad++;
    console.log(`  UNEXPLAINED ${k}\n      ${v.message.replace(/\s+/g, ' ').slice(0, 300)}`);
  }
}
for (const [k, c] of classified) {
  if (k.startsWith(`${suite} :: `) && !seen.has(k)) {
    bad++;
    console.log(`  STALE classification (${results.has(k) ? results.get(k).status : 'not run'}): ${k}`);
  }
}
for (const [k, s] of Object.entries(baseline)) {
  if (k.startsWith(`${suite} :: `) && s === 'passed' && !results.has(k)) {
    bad++;
    console.log(`  MISSING from the run: ${k}`);
  }
}
console.log(bad ? `${bad} unexplained difference(s)` : 'no unexplained difference');
process.exit(bad ? 1 : 0);
