// A topic module for record-production.mjs: hidane's conditions (see
// import-hidane.mjs), so each one the emulator decided is also decided by the
// Rules Test API. The development conditions come from
// ../imported/hidane/original/results; the held-out ones from the results
// file named by HIDANE_HELDOUT_RESULTS, when it is set (they are then
// recorded into the held-out set only).
//
// The Test API takes the document as JSON, not as typed Firestore values, so
// the document is the closest JSON: ts as an RFC 3339 string (the API reads
// such strings as timestamps), big written as the JSON number
// 9223372036854775807, and geo, ref and by as plain maps and strings. A
// condition that reads one_f, geo, ref or by is marked comparable: false.
import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = dirname(fileURLToPath(import.meta.url));
const devFile = join(HERE, '../imported/hidane/original/results/rules-conditions-official-v1.22.0.development.json');

export const topic = 'hidane-conditions';
export const service = 'firestore';
export const wildcard = 'room';
const BIG = 777009;
export const resource = {
  n: 1,
  f: 1.5,
  one_f: 1.0,
  s: 'abc',
  b: true,
  nul: null,
  arr: [1, 2, 3],
  tags: ['a', 'b'],
  m: { x: 1, y: 'z' },
  ts: '2020-01-02T03:04:05.123Z',
  geo: { latitude: 10.0, longitude: 20.0 },
  ref: '/databases/(default)/documents/rooms/r2',
  by: 'AQI=',
  big: BIG,
};

const UNCOMPARABLE = /\b(one_f|geo|ref|by)\b/;

function load(file, heldout) {
  return JSON.parse(readFileSync(file, 'utf8')).results.map((r, i) => {
    const seg = `h${heldout ? 'h' : 'd'}${String(i).padStart(4, '0')}`;
    const c = {
      id: r.name,
      seg,
      desc: `hidane ${r.name}; emulator: ${r.outcome}`,
      expr: r.condition,
      allow: 'read',
      path: `${seg}/r1`,
      auth: null,
      raw: [[BIG, '9223372036854775807']],
      emulatorOutcome: r.outcome,
    };
    if (UNCOMPARABLE.test(r.condition)) c.note = 'comparable: false (reads a field the Test API cannot type)';
    if (heldout) c.heldout = true;
    else c.pin = true;
    return c;
  });
}

export const cases = [
  ...load(devFile, false),
  ...(process.env.HIDANE_HELDOUT_RESULTS ? load(process.env.HIDANE_HELDOUT_RESULTS, true) : []),
];
