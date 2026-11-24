// Creates, through the Admin API, the composite indexes of firebase-js-sdk's Firestore integration
// tests: those of packages/firestore/firestore_composite_index_config.tf (which Firebase applies to
// its production test project, in both scopes), plus the one index Aruma's rule asks for where
// that file holds one Aruma refuses (below). The file's two indexes that name a field twice are
// refused, as docs/reference/known-differences.md states; any other refusal fails (exit 1).
//
//   node indexes.mjs <config.tf> <base url> <project id>
import { readFileSync } from 'node:fs';

const [tf, base, project] = process.argv.slice(2);
const text = readFileSync(tf, 'utf8');
const body = text.slice(text.indexOf('indexes = {'));

// parse reads each `indexN = [ { field_path, order | array_config }, ... ]` of the HCL file.
function parse() {
  const out = [];
  for (const m of body.matchAll(/\w*index\w*\s*=\s*\[/g)) {
    let i = m.index + m[0].length;
    let depth = 1;
    let j = i;
    while (depth > 0) {
      depth += (body[j] === '[') - (body[j] === ']');
      j++;
    }
    const fields = [...body.slice(i, j - 1).matchAll(/\{([^{}]*)\}/g)].map((f) => {
      const path = /field_path\s*=\s*"((?:[^"\\]|\\.)*)"/.exec(f[1])[1].replace(/\\\\/g, '\\');
      const order = /order\s*=\s*"(\w+)"/.exec(f[1]);
      const array = /array_config\s*=\s*"(\w+)"/.exec(f[1]);
      return order ? { fieldPath: path, order: order[1] } : { fieldPath: path, arrayConfig: array[1] };
    });
    out.push(fields);
  }
  return out;
}

// The last query of "OR Queries can use or queries" (testId == x && (a == 2 || b == 1), order by a,
// limitToLast, so a descending on the wire) has the disjunct testId == x && a == 2 ordered by a
// descending. Production's rule, as recorded (test/oracle/documents/corpus/indexes-105.json,
// eq_eq_ord_eqfield and ltl_eq), asks for [testId, a DESC, __name__ DESC] for that shape, and
// Aruma's error names that index. The Firebase file serves it with [a, testId, a DESC] instead, a
// field twice, which Aruma refuses.
const extra = [[{ fieldPath: 'testId', order: 'ASCENDING' }, { fieldPath: 'a', order: 'DESCENDING' }]];

// The refusals expected: each index of the file that names a field twice, in both scopes.
const twice = new Set(['a,testId,a', 'rating,testId,pages,rating']);

let made = 0;
let unexpected = 0;
for (const queryScope of ['COLLECTION', 'COLLECTION_GROUP']) {
  for (const fields of [...parse(), ...extra]) {
    const r = await fetch(`${base}/v1/projects/${project}/databases/(default)/collectionGroups/composite-index-test-collection/indexes`, {
      method: 'POST',
      headers: { 'content-type': 'application/json', authorization: 'Bearer owner' },
      body: JSON.stringify({ queryScope, fields }),
    });
    if (r.ok) {
      made++;
      continue;
    }
    const paths = fields.map((f) => f.fieldPath);
    const text = await r.text();
    const known = twice.has(paths.join(',')) && text.includes('appears more than once in the index');
    if (!known) unexpected++;
    console.log(`index refused${known ? ' (a field twice, expected)' : ''} (${r.status}): ${text.slice(0, 120)} ${queryScope} ${JSON.stringify(paths)}`);
  }
}
console.log(`${made} composite indexes created`);
if (unexpected) {
  console.log(`${unexpected} index(es) refused unexpectedly`);
  process.exit(1);
}
