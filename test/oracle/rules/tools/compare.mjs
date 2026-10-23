// Lists every case where the emulator decided differently from the Rules
// Test API, as Markdown tables for parity-exceptions.md.
//
//   node compare.mjs <emulator dir> <production dir>
//
// Our topics are compared through <emulator dir>/<topic>.json (written by
// record-emulator.mjs). hidane's conditions are compared through
// <production dir>/hidane-conditions.json: each case carries the outcome
// hidane recorded from the emulator. Outcome classes are compared (allow,
// deny, error); messages are not, as the emulators word them differently.
import { existsSync, readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';

const [emuDir, prodDir] = process.argv.slice(2);
const cell = (s) => String(s ?? '').replace(/\|/g, '\\|').replace(/\n/g, ' ').slice(0, 160);
const fmt = (o) => (o.skip ? `skip` : `${o.outcome}${o.errorKind && o.errorKind !== 'evaluation' ? ` (${o.errorKind})` : ''}`);

let total = { same: 0, differ: 0, skip: 0 };
const rows = [];
for (const f of readdirSync(emuDir).filter((n) => n.endsWith('.json')).sort()) {
  const e = JSON.parse(readFileSync(join(emuDir, f), 'utf8'));
  const prod = JSON.parse(readFileSync(join(prodDir, f), 'utf8'));
  const byId = new Map(prod.cases.map((c) => [c.id, c]));
  for (const r of e.cases) {
    if (r.emulator.skip) {
      total.skip++;
      continue;
    }
    if (r.emulator.outcome === r.production.outcome) {
      total.same++;
      continue;
    }
    total.differ++;
    const c = byId.get(r.id);
    rows.push(
      `| \`${r.id}\` | ${cell(c.expression ?? c.description)} | ${fmt(r.production)} | ${fmt(r.emulator)} | ${cell(r.emulator.message ?? r.production.message)} |`,
    );
  }
}
console.log(`Our topics: ${total.same} same, ${total.differ} different, ${total.skip} not run on the emulator.\n`);
console.log('| case | condition | Test API | emulator | message |');
console.log('|---|---|---|---|---|');
console.log(rows.join('\n'));

const hc = join(prodDir, 'hidane-conditions.json');
if (existsSync(hc)) {
  const map = { allow: 'allow', false: 'deny', error: 'error', compile: 'error', internal: 'error' };
  const p = JSON.parse(readFileSync(hc, 'utf8'));
  const out = [];
  const t = { same: 0, differ: 0, uncomparable: 0 };
  for (const c of p.cases) {
    const emu = map[c.hidaneEmulatorOutcome];
    const prod = c.expected.outcome;
    if (c.note && c.note.startsWith('comparable: false')) {
      t.uncomparable++;
      continue;
    }
    // A compile error on one side and an evaluation error on the other is
    // still a difference.
    const emuKind = c.hidaneEmulatorOutcome === 'compile' ? 'compile' : undefined;
    const same = emu === prod && (emuKind === 'compile') === (c.expected.errorKind === 'compile');
    if (same) {
      t.same++;
      continue;
    }
    t.differ++;
    out.push(
      `| \`${c.id}\` | \`${cell(c.expression)}\` | ${fmt(c.expected)} | ${c.hidaneEmulatorOutcome === 'false' ? 'deny' : c.hidaneEmulatorOutcome} | ${cell(c.expected.message)} |`,
    );
  }
  console.log(`\nhidane's conditions: ${t.same} same, ${t.differ} different, ${t.uncomparable} not comparable.\n`);
  console.log('| case | condition | Test API | emulator (hidane) | Test API message |');
  console.log('|---|---|---|---|---|');
  console.log(out.join('\n'));
}
