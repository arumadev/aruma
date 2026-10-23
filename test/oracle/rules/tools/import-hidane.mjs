// Imports hidane's Security Rules condition fixtures (github.com/hidane-dev/hidane,
// MIT OR Apache-2.0; the cel-spec cases inside come from google/cel-spec,
// Apache-2.0) into ../imported/hidane/ and the held-out set's directory.
//
//   node import-hidane.mjs <hidane checkout> <commit> <heldout dir>
//
// hidane recorded what the official Firestore emulator (v1.22.0) decided for
// each condition (results/rules-conditions-official-v1.22.0.json). The
// conditions named heldout/<n> (spikes/rules-eval/cases/rules-heldout.json)
// are held out from the S1 builders: their cases and their results go to
// <heldout dir>/hidane/, and the development copy of the results file is the
// original with those lines taken out. Nothing else is changed.
import { copyFileSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = dirname(fileURLToPath(import.meta.url));
const OUT = join(HERE, '..', 'imported', 'hidane');
const [repoArg, commit, heldArg] = process.argv.slice(2);
const repo = resolve(repoArg);
const HELD = join(resolve(heldArg), 'hidane');
const RESULTS = 'results/rules-conditions-official-v1.22.0.json';
const CASES = 'spikes/rules-eval/cases';
const VERSION = 'cloud-firestore-emulator v1.22.0 (recorded by hidane)';
// The date of hidane's spike results (spikes/rules-eval/README.md, "Results
// (2026-10-11)"); the results file carries no date of its own.
const RECORDED = '2026-10-11';

for (const d of ['original/results', 'original/cases', 'original/tools', 'normalised']) mkdirSync(join(OUT, d), { recursive: true });
for (const d of ['original/results', 'original/cases', 'normalised']) mkdirSync(join(HELD, d), { recursive: true });

// The results file has one result per line: split it by line, so both
// halves keep the original bytes.
const text = readFileSync(join(repo, RESULTS), 'utf8');
const lines = text.split('\n');
const isHeld = (l) => l.startsWith('  {"name": "heldout/');
const devLines = lines.filter((l) => !isHeld(l));
const heldLines = lines.filter(isHeld);
// Keep the JSON valid: the last result line before " ]" has no comma.
const fixCommas = (ls) => {
  const out = ls.map((l) => l);
  for (let i = 0; i < out.length; i++) {
    if (out[i].startsWith('  {"name": ')) {
      const last = !(out[i + 1] || '').startsWith('  {"name": ');
      out[i] = out[i].replace(/,$/, '') + (last ? '' : ',');
    }
  }
  return out;
};
writeFileSync(join(OUT, 'original/results/rules-conditions-official-v1.22.0.development.json'), fixCommas(devLines).join('\n'));
const header = lines.slice(0, lines.findIndex((l) => l.startsWith('  {"name": ')));
const footer = [' ]', '}', ''];
writeFileSync(
  join(HELD, 'original/results/rules-conditions-official-v1.22.0.heldout.json'),
  fixCommas([...header, ...heldLines, ...footer]).join('\n'),
);
const dev = JSON.parse(readFileSync(join(OUT, 'original/results/rules-conditions-official-v1.22.0.development.json'), 'utf8'));
const held = JSON.parse(readFileSync(join(HELD, 'original/results/rules-conditions-official-v1.22.0.heldout.json'), 'utf8'));
if (dev.results.length + held.results.length !== JSON.parse(text).results.length) throw new Error('split lost results');

copyFileSync(join(repo, CASES, 'rules.json'), join(OUT, 'original/cases/rules.json'));
copyFileSync(join(repo, CASES, 'cel-spec.json'), join(OUT, 'original/cases/cel-spec.json'));
copyFileSync(join(repo, CASES, 'rules-heldout.json'), join(HELD, 'original/cases/rules-heldout.json'));
copyFileSync(join(repo, 'spikes/rules-eval/conditions.py'), join(OUT, 'original/tools/conditions.py'));
copyFileSync(join(repo, 'tools/oracle/rules_conditions.py'), join(OUT, 'original/tools/rules_conditions.py'));

const rulesCases = JSON.parse(readFileSync(join(repo, CASES, 'rules.json'), 'utf8'));
const heldCases = JSON.parse(readFileSync(join(repo, CASES, 'rules-heldout.json'), 'utf8'));
const group = (name) => {
  const [kind, i] = name.split('/');
  if (kind === 'rules') return `rules: ${rulesCases[+i].group}`;
  if (kind === 'heldout') return `heldout: ${heldCases[+i].group}`;
  return `${kind}: ${name.split('/')[1]}`;
};

function expected(r) {
  if (r.outcome === 'allow') return { outcome: 'allow' };
  if (r.outcome === 'false') return { outcome: 'deny', reason: 'false', message: r.messages.join('\n') };
  if (r.outcome === 'error') return { outcome: 'error', errorKind: 'evaluation', message: r.messages.join('\n') };
  if (r.outcome === 'compile') return { outcome: 'error', errorKind: 'compile', message: r.message ?? `status ${r.status}` };
  return { outcome: 'error', errorKind: 'internal', message: `${r.status} ${r.message ?? ''}`.trim() };
}

function normalise(results, set) {
  return {
    schema: 'aruma.rules-oracle/v1',
    service: 'firestore',
    set,
    source: 'hidane',
    provenance: 'emulator',
    upstream: `https://github.com/hidane-dev/hidane/tree/${commit}`,
    licence: 'MIT OR Apache-2.0 (cel-spec cases: Apache-2.0)',
    recordedAt: RECORDED,
    version: VERSION,
    // hidane's template: the condition replaces {{expr}} (line 5, column 22).
    rulesets: { 'hidane-rooms-read': dev.rules.replace('%s', '{{expr}}') },
    // rooms/r1 as hidane wrote it, in Firestore REST value encoding.
    resources: { 'hidane-document': { data: dev.document } },
    cases: results.map((r) => ({
      id: `hidane/${r.name}`,
      service: 'firestore',
      source: 'hidane',
      provenance: 'emulator',
      recordedAt: RECORDED,
      version: VERSION,
      description: group(r.name),
      expression: r.condition,
      rulesetRef: 'hidane-rooms-read',
      // A REST GET of rooms/r1 without an Authorization header.
      request: { method: 'get', path: '/databases/(default)/documents/rooms/r1', auth: null },
      resourceRef: 'hidane-document',
      valueEncoding: 'firestore-rest',
      expected: expected(r),
    })),
  };
}

writeFileSync(join(OUT, 'normalised/rules-conditions-emulator.json'), JSON.stringify(normalise(dev.results, 'development'), null, 1) + '\n');
writeFileSync(join(HELD, 'normalised/rules-conditions-emulator.json'), JSON.stringify(normalise(held.results, 'heldout'), null, 1) + '\n');
console.log(`development ${dev.results.length}, heldout ${held.results.length}`);
