// Composite-index recorder: runs 105 runQuery / runAggregationQuery shapes on
// a project with no composite index and records, per shape, whether the query
// is served or refused with FAILED_PRECONDITION, the exact message, its
// template and the decoded "create it here" link.
//
// It is the composite-index probe of .cache/briefs/research/artifacts/
// (composite-index-probe.mjs, 2026-10-26) made re-runnable: the same shapes,
// the same data, and the same output, normalised into the corpus.
//
//   node record-indexes.mjs                     production (chatapp-8ae21)
//   node record-indexes.mjs --emulator HOST:PORT the Firestore emulator
//   node record-indexes.mjs --import FILE        convert a recorded probe JSON
//
// Production writes 11 documents, only under the top-level collection
// aruma-ref-idx (and its subcollections aruma-ref-idxcg), deletes them by one
// commit at the end, and verifies the deletion. It creates, deletes or
// changes no index, field override, rule or setting.
import { readFileSync } from 'node:fs';
import { basename } from 'node:path';
import { PROJECT, rest, v, fields, versions, writeCorpus, assertScoped } from './lib.mjs';

const args = process.argv.slice(2);
const opt = (name) => {
  const i = args.indexOf(name);
  return i >= 0 ? args[i + 1] : undefined;
};

// ---- the shapes ----
const F = (f, op, x) => ({ fieldFilter: { field: { fieldPath: f }, op, value: v(x) } });
const U = (f, op) => ({ unaryFilter: { field: { fieldPath: f }, op } });
const AND = (...filters) => ({ compositeFilter: { op: 'AND', filters } });
const OR = (...filters) => ({ compositeFilter: { op: 'OR', filters } });
const O = (f, d = 'ASCENDING') => ({ field: { fieldPath: f }, direction: d });
const DESC = 'DESCENDING';

const COLL = 'aruma-ref-idx';
const CG = 'aruma-ref-idxcg';
const EMPTY = 'aruma-ref-idx-empty';
const EMPTY_CG = 'aruma-ref-idx-emptycg';
const docs = {
  d1: { a: 1, b: 1, c: 'x', d: 10, tags: ['red', 'blue'], m: { x: 1, y: 'p' } },
  d2: { a: 1, b: 2, c: 'y', d: 20, tags: ['red'], m: { x: 2, y: 'q' } },
  d3: { a: 2, b: 1, c: 'x', d: 30, tags: ['green'], m: { x: 1, y: 'q' } },
  d4: { a: 2, b: 2, c: 'z', d: 40, tags: [], m: { x: 3 } },
  d5: { a: 3, b: 3, c: 'y', d: 50, tags: ['blue', 'green'], m: { x: 2, y: 'p' } },
  d6: { a: 1, b: 1, c: 'z', d: 60 },
};

// The cursor references name {root}; runCase puts the root of the target in.
const REF = (id) => ({ $ref: `{root}/${COLL}/${id}` });

// [id, description, kind, structuredQuery without from, aggregations?]
const cases = [
  ['eq1', 'a == 1', 'q', { where: F('a', 'EQUAL', 1) }],
  ['eq2', 'a == 1 && b == 1', 'q', { where: AND(F('a', 'EQUAL', 1), F('b', 'EQUAL', 1)) }],
  ['eq3', "a == 1 && b == 1 && c == 'x'", 'q', { where: AND(F('a', 'EQUAL', 1), F('b', 'EQUAL', 1), F('c', 'EQUAL', 'x')) }],
  ['eq1_ord_same', 'a == 1 orderBy a', 'q', { where: F('a', 'EQUAL', 1), orderBy: [O('a')] }],
  ['eq1_ord_other', 'a == 1 orderBy d', 'q', { where: F('a', 'EQUAL', 1), orderBy: [O('d')] }],
  ['eq1_ord_other_desc', 'a == 1 orderBy d desc', 'q', { where: F('a', 'EQUAL', 1), orderBy: [O('d', DESC)] }],
  ['eq1_ord_name_desc', 'a == 1 orderBy __name__ desc', 'q', { where: F('a', 'EQUAL', 1), orderBy: [O('__name__', DESC)] }],
  ['eq2_ord_name_desc', 'a == 1 && b == 1 orderBy __name__ desc', 'q', { where: AND(F('a', 'EQUAL', 1), F('b', 'EQUAL', 1)), orderBy: [O('__name__', DESC)] }],
  ['range1', 'd > 15', 'q', { where: F('d', 'GREATER_THAN', 15) }],
  ['range1_both', 'd > 15 && d < 50', 'q', { where: AND(F('d', 'GREATER_THAN', 15), F('d', 'LESS_THAN', 50)) }],
  ['range1_ord_desc', 'd > 15 orderBy d desc', 'q', { where: F('d', 'GREATER_THAN', 15), orderBy: [O('d', DESC)] }],
  ['range1_ord_other', 'd > 15 orderBy d, a', 'q', { where: F('d', 'GREATER_THAN', 15), orderBy: [O('d'), O('a')] }],
  ['range1_ord_other_first', 'd > 15 orderBy a (raw REST, a first)', 'q', { where: F('d', 'GREATER_THAN', 15), orderBy: [O('a')] }],
  ['range1_name_desc', 'd > 15 orderBy d asc, __name__ desc', 'q', { where: F('d', 'GREATER_THAN', 15), orderBy: [O('d'), O('__name__', DESC)] }],
  ['eq_range', 'a == 1 && d > 15', 'q', { where: AND(F('a', 'EQUAL', 1), F('d', 'GREATER_THAN', 15)) }],
  ['range2', 'd > 15 && b > 1', 'q', { where: AND(F('d', 'GREATER_THAN', 15), F('b', 'GREATER_THAN', 1)) }],
  ['ac', "tags array-contains 'red'", 'q', { where: F('tags', 'ARRAY_CONTAINS', 'red') }],
  ['ac_eq', "tags array-contains 'red' && a == 1", 'q', { where: AND(F('tags', 'ARRAY_CONTAINS', 'red'), F('a', 'EQUAL', 1)) }],
  ['ac_ord', "tags array-contains 'red' orderBy d", 'q', { where: F('tags', 'ARRAY_CONTAINS', 'red'), orderBy: [O('d')] }],
  ['ac_range', "tags array-contains 'red' && d > 15", 'q', { where: AND(F('tags', 'ARRAY_CONTAINS', 'red'), F('d', 'GREATER_THAN', 15)) }],
  ['aca', "tags array-contains-any ['red','green']", 'q', { where: F('tags', 'ARRAY_CONTAINS_ANY', ['red', 'green']) }],
  ['aca_eq', "tags array-contains-any ['red','green'] && a == 1", 'q', { where: AND(F('tags', 'ARRAY_CONTAINS_ANY', ['red', 'green']), F('a', 'EQUAL', 1)) }],
  ['in', 'a in [1,2]', 'q', { where: F('a', 'IN', [1, 2]) }],
  ['in_eq', 'a in [1,2] && b == 1', 'q', { where: AND(F('a', 'IN', [1, 2]), F('b', 'EQUAL', 1)) }],
  ['in_ord_other', 'a in [1,2] orderBy d', 'q', { where: F('a', 'IN', [1, 2]), orderBy: [O('d')] }],
  ['notin', 'a not-in [1]', 'q', { where: F('a', 'NOT_IN', [1]) }],
  ['notin_eq', 'a not-in [1] && b == 1', 'q', { where: AND(F('a', 'NOT_IN', [1]), F('b', 'EQUAL', 1)) }],
  ['neq', 'a != 1', 'q', { where: F('a', 'NOT_EQUAL', 1) }],
  ['neq_ord_other', 'a != 1 orderBy a, d', 'q', { where: F('a', 'NOT_EQUAL', 1), orderBy: [O('a'), O('d')] }],
  ['neq_eq', 'a != 1 && b == 1', 'q', { where: AND(F('a', 'NOT_EQUAL', 1), F('b', 'EQUAL', 1)) }],
  ['is_null', 'a IS_NULL', 'q', { where: U('a', 'IS_NULL') }],
  ['or2', 'a == 1 || b == 2', 'q', { where: OR(F('a', 'EQUAL', 1), F('b', 'EQUAL', 2)) }],
  ['or_and', "(a == 1 && b == 1) || c == 'z'", 'q', { where: OR(AND(F('a', 'EQUAL', 1), F('b', 'EQUAL', 1)), F('c', 'EQUAL', 'z')) }],
  ['or_ord', '(a == 1 || b == 2) orderBy d', 'q', { where: OR(F('a', 'EQUAL', 1), F('b', 'EQUAL', 2)), orderBy: [O('d')] }],
  ['or_range', 'a == 1 || d > 45', 'q', { where: OR(F('a', 'EQUAL', 1), F('d', 'GREATER_THAN', 45)) }],
  ['ord_a', 'orderBy a', 'q', { orderBy: [O('a')] }],
  ['ord_a_desc', 'orderBy a desc', 'q', { orderBy: [O('a', DESC)] }],
  ['ord_a_d', 'orderBy a, d', 'q', { orderBy: [O('a'), O('d')] }],
  ['ord_a_d_desc', 'orderBy a desc, d desc', 'q', { orderBy: [O('a', DESC), O('d', DESC)] }],
  ['ord_a_desc_d', 'orderBy a desc, d asc', 'q', { orderBy: [O('a', DESC), O('d')] }],
  ['ltl_single', 'limitToLast(2) on orderBy d (wire: orderBy d desc limit 2)', 'q', { orderBy: [O('d', DESC)], limit: 2 }],
  ['ltl_two', 'limitToLast(2) on orderBy a, d (wire: a desc, d desc limit 2)', 'q', { orderBy: [O('a', DESC), O('d', DESC)], limit: 2 }],
  ['ltl_eq', 'a == 1 limitToLast(2) on orderBy d (wire: d desc)', 'q', { where: F('a', 'EQUAL', 1), orderBy: [O('d', DESC)], limit: 2 }],
  ['cursor_single', 'orderBy d startAt [20]', 'q', { orderBy: [O('d')], startAt: { values: [v(20)], before: true } }],
  ['cursor_eq_name', 'a == 1 orderBy __name__ startAfter d1', 'q', { where: F('a', 'EQUAL', 1), orderBy: [O('__name__')], startAt: { values: [v(REF('d1'))], before: false } }],
  ['cursor_eq_other', 'a == 1 orderBy d startAt [20]', 'q', { where: F('a', 'EQUAL', 1), orderBy: [O('d')], startAt: { values: [v(20)], before: true } }],
  ['name_asc', 'orderBy __name__', 'q', { orderBy: [O('__name__')] }],
  ['name_desc', 'orderBy __name__ desc', 'q', { orderBy: [O('__name__', DESC)] }],
  ['name_range', '__name__ > d2', 'q', { where: F('__name__', 'GREATER_THAN', REF('d2')) }],
  ['name_range_eq', '__name__ > d2 && a == 1', 'q', { where: AND(F('__name__', 'GREATER_THAN', REF('d2')), F('a', 'EQUAL', 1)) }],
  ['map_eq_whole', "m == {x:1,y:'p'}", 'q', { where: F('m', 'EQUAL', { x: 1, y: 'p' }) }],
  ['map_sub_eq', 'm.x == 1', 'q', { where: F('m.x', 'EQUAL', 1) }],
  ['map_sub_eq_eq', 'm.x == 1 && a == 1', 'q', { where: AND(F('m.x', 'EQUAL', 1), F('a', 'EQUAL', 1)) }],
  ['map_ord', 'orderBy m', 'q', { orderBy: [O('m')] }],
  ['map_sub_eq_ord', 'm.x == 1 orderBy d', 'q', { where: F('m.x', 'EQUAL', 1), orderBy: [O('d')] }],
  ['select_eq', 'select(a) where a == 1', 'q', { select: { fields: [{ fieldPath: 'a' }] }, where: F('a', 'EQUAL', 1) }],
  ['offset_eq', 'a == 1 offset 1', 'q', { where: F('a', 'EQUAL', 1), offset: 1 }],
  ['eq_eq_ord_eqfield', 'a == 1 && d == 10 orderBy d', 'q', { where: AND(F('a', 'EQUAL', 1), F('d', 'EQUAL', 10)), orderBy: [O('d')] }],
  ['eq1_ord_a_d', 'a == 1 orderBy a, d', 'q', { where: F('a', 'EQUAL', 1), orderBy: [O('a'), O('d')] }],
  ['in_in', 'a in [1,2] && b in [1,2]', 'q', { where: AND(F('a', 'IN', [1, 2]), F('b', 'IN', [1, 2])) }],
  ['ac_in', "tags array-contains 'red' && a in [1,2]", 'q', { where: AND(F('tags', 'ARRAY_CONTAINS', 'red'), F('a', 'IN', [1, 2])) }],
  ['in_ord_same_desc', 'a in [1,2] orderBy a desc', 'q', { where: F('a', 'IN', [1, 2]), orderBy: [O('a', DESC)] }],
  ['notin_ord_other', 'a not-in [1] orderBy a, d', 'q', { where: F('a', 'NOT_IN', [1]), orderBy: [O('a'), O('d')] }],
  ['and_or', "a == 1 && (b == 1 || c == 'z')", 'q', { where: AND(F('a', 'EQUAL', 1), OR(F('b', 'EQUAL', 1), F('c', 'EQUAL', 'z'))) }],
  ['or_same_field_range', 'd < 15 || d > 45', 'q', { where: OR(F('d', 'LESS_THAN', 15), F('d', 'GREATER_THAN', 45)) }],
  ['ord_d_desc_name_asc', 'orderBy d desc, __name__ asc', 'q', { orderBy: [O('d', DESC), O('__name__')] }],
  ['ord_d_name_asc', 'orderBy d asc, __name__ asc (explicit)', 'q', { orderBy: [O('d'), O('__name__')] }],
  ['is_not_null', 'a IS_NOT_NULL', 'q', { where: U('a', 'IS_NOT_NULL') }],
  ['range_ord_same_limit', 'd > 15 orderBy d desc limit 1', 'q', { where: F('d', 'GREATER_THAN', 15), orderBy: [O('d', DESC)], limit: 1 }],
  ['eq_ord_name_cursor', 'a == 1 && b == 1 orderBy __name__ desc startAt [d6]', 'q', { where: AND(F('a', 'EQUAL', 1), F('b', 'EQUAL', 1)), orderBy: [O('__name__', DESC)], startAt: { values: [v(REF('d6'))], before: true } }],
  ['map_sub_range', 'm.x > 1', 'q', { where: F('m.x', 'GREATER_THAN', 1) }],
  ['cg_name_asc', 'CG: orderBy __name__', 'cg', { orderBy: [O('__name__')] }],
  ['cg_ord_desc', 'CG: orderBy d desc', 'cg', { orderBy: [O('d', DESC)] }],
  ['cg_or', 'CG: a == 1 || b == 2', 'cg', { where: OR(F('a', 'EQUAL', 1), F('b', 'EQUAL', 2)) }],
  ['count_ac', "count() where tags array-contains 'red'", 'agg', { where: F('tags', 'ARRAY_CONTAINS', 'red') }, [{ alias: 'n', count: {} }]],
  ['count_or', 'count() where a == 1 || b == 2', 'agg', { where: OR(F('a', 'EQUAL', 1), F('b', 'EQUAL', 2)) }, [{ alias: 'n', count: {} }]],
  ['sum2_all', 'sum(d), sum(b)', 'agg', {}, [{ alias: 's', sum: { field: { fieldPath: 'd' } } }, { alias: 't', sum: { field: { fieldPath: 'b' } } }]],
  ['sum2_same', 'sum(d), avg(d)', 'agg', {}, [{ alias: 's', sum: { field: { fieldPath: 'd' } } }, { alias: 't', avg: { field: { fieldPath: 'd' } } }]],
  ['sum_eq2', 'sum(d) where a == 1 && b == 1', 'agg', { where: AND(F('a', 'EQUAL', 1), F('b', 'EQUAL', 1)) }, [{ alias: 's', sum: { field: { fieldPath: 'd' } } }]],
  ['sum_ord_same', 'sum(d) orderBy d', 'agg', { orderBy: [O('d')] }, [{ alias: 's', sum: { field: { fieldPath: 'd' } } }]],
  ['sum_ord_other', 'sum(d) orderBy a', 'agg', { orderBy: [O('a')] }, [{ alias: 's', sum: { field: { fieldPath: 'd' } } }]],
  ['cg_all', 'CG: no filter', 'cg', {}],
  ['cg_eq', 'CG: a == 1', 'cg', { where: F('a', 'EQUAL', 1) }],
  ['cg_ord', 'CG: orderBy d', 'cg', { orderBy: [O('d')] }],
  ['cg_name_desc', 'CG: orderBy __name__ desc', 'cg', { orderBy: [O('__name__', DESC)] }],
  ['cg_eq_ord', 'CG: a == 1 orderBy d', 'cg', { where: F('a', 'EQUAL', 1), orderBy: [O('d')] }],
  ['cg_ac', "CG: tags array-contains 'red'", 'cg', { where: F('tags', 'ARRAY_CONTAINS', 'red') }],
  ['cg_range', 'CG: d > 15', 'cg', { where: F('d', 'GREATER_THAN', 15) }],
  ['count_all', 'count()', 'agg', {}, [{ alias: 'n', count: {} }]],
  ['count_eq', 'count() where a == 1', 'agg', { where: F('a', 'EQUAL', 1) }, [{ alias: 'n', count: {} }]],
  ['count_eq2', 'count() where a == 1 && b == 1', 'agg', { where: AND(F('a', 'EQUAL', 1), F('b', 'EQUAL', 1)) }, [{ alias: 'n', count: {} }]],
  ['count_range', 'count() where d > 15', 'agg', { where: F('d', 'GREATER_THAN', 15) }, [{ alias: 'n', count: {} }]],
  ['count_eq_range', 'count() where a == 1 && d > 15', 'agg', { where: AND(F('a', 'EQUAL', 1), F('d', 'GREATER_THAN', 15)) }, [{ alias: 'n', count: {} }]],
  ['count_up_to', 'count(upTo 2) where a == 1', 'agg', { where: F('a', 'EQUAL', 1) }, [{ alias: 'n', count: { upTo: '2' } }]],
  ['sum_all', 'sum(d)', 'agg', {}, [{ alias: 's', sum: { field: { fieldPath: 'd' } } }]],
  ['sum_eq', 'sum(d) where a == 1', 'agg', { where: F('a', 'EQUAL', 1) }, [{ alias: 's', sum: { field: { fieldPath: 'd' } } }]],
  ['sum_eq_same', 'sum(a) where a == 1', 'agg', { where: F('a', 'EQUAL', 1) }, [{ alias: 's', sum: { field: { fieldPath: 'a' } } }]],
  ['sum_range_same', 'sum(d) where d > 15', 'agg', { where: F('d', 'GREATER_THAN', 15) }, [{ alias: 's', sum: { field: { fieldPath: 'd' } } }]],
  ['avg_all', 'avg(d)', 'agg', {}, [{ alias: 'v', avg: { field: { fieldPath: 'd' } } }]],
  ['avg_eq', 'avg(d) where a == 1', 'agg', { where: F('a', 'EQUAL', 1) }, [{ alias: 'v', avg: { field: { fieldPath: 'd' } } }]],
  ['avg_range_other', 'avg(d) where b > 1', 'agg', { where: F('b', 'GREATER_THAN', 1) }, [{ alias: 'v', avg: { field: { fieldPath: 'd' } } }]],
  ['multi_agg', 'count(), sum(d), avg(b)', 'agg', {}, [{ alias: 'n', count: {} }, { alias: 's', sum: { field: { fieldPath: 'd' } } }, { alias: 'v', avg: { field: { fieldPath: 'b' } } }]],
  ['count_ord_other', 'count() where a == 1 orderBy d', 'agg', { where: F('a', 'EQUAL', 1), orderBy: [O('d')] }, [{ alias: 'n', count: {} }]],
  ['cg_count', 'CG: count()', 'cgagg', {}, [{ alias: 'n', count: {} }]],
  ['cg_count_eq', 'CG: count() where a == 1', 'cgagg', { where: F('a', 'EQUAL', 1) }, [{ alias: 'n', count: {} }]],
];

// ---- running (the raw form is the probe's own) ----

async function runProbe(mode, emuHost) {
  const project = mode === 'prod' ? PROJECT : 'demo-aruma';
  const origin = mode === 'prod' ? undefined : `http://${emuHost}`;
  const db = `projects/${project}/databases/(default)`;
  const root = `${db}/documents`;
  const call = (method, path, body) => rest(method, path, body, origin ? { origin } : {});
  const written = [];
  for (const [id, data] of Object.entries(docs)) written.push([`${root}/${COLL}/${id}`, data]);
  for (const [id, data] of Object.entries(docs).slice(0, 4)) written.push([`${root}/${COLL}/d1/${CG}/${id}`, data]);
  written.push([`${root}/${COLL}/d2/${CG}/e1`, docs.d5]);
  for (const [name] of written) assertScoped(name.replace(root, `projects/${PROJECT}/databases/(default)/documents`));

  const runCase = async ([id, desc, kind, sq, aggs], coll, cg) => {
    const isCg = kind === 'cg' || kind === 'cgagg';
    const from = isCg ? [{ collectionId: cg, allDescendants: true }] : [{ collectionId: coll }];
    const sqj = JSON.parse(JSON.stringify({ from, ...sq }).replaceAll(`{root}/${COLL}/d`, `${root}/${coll}/d`));
    const agg = kind === 'agg' || kind === 'cgagg';
    const path = `${root}:${agg ? 'runAggregationQuery' : 'runQuery'}`;
    const body = agg ? { structuredAggregationQuery: { structuredQuery: sqj, aggregations: aggs } } : { structuredQuery: sqj };
    const r = await call('POST', path, body);
    const arr = Array.isArray(r.body) ? r.body : [r.body];
    const err = arr.find((x) => x && x.error)?.error;
    let summary;
    if (err) summary = { ok: false, status: err.status, code: err.code, message: err.message, rawBody: r.body };
    else {
      const ids = arr.filter((x) => x.document).map((x) => x.document.name.split('/').slice(-3).join('/'));
      const aggr = arr.filter((x) => x.result).map((x) => x.result.aggregateFields);
      summary = { ok: true, ids: agg ? undefined : ids, aggregates: agg ? aggr : undefined };
    }
    return { id, desc, kind, http: r.http, request: { path: path.replace(root, '{root}'), body }, ...summary };
  };
  const commit = async (writes) => {
    const r = await call('POST', `${db}/documents:commit`, { writes });
    if (r.http !== 200) throw new Error(`commit failed: HTTP ${r.http}`);
  };

  const out = { mode, project, startedAt: new Date().toISOString(), database: null, empty: [], populated: [], cleanup: null };
  const dbr = await call('GET', db);
  out.database = dbr.http === 200 ? { type: dbr.body.type, databaseEdition: dbr.body.databaseEdition, concurrencyMode: dbr.body.concurrencyMode, locationId: dbr.body.locationId } : { http: dbr.http };
  if (mode === 'prod') {
    out.indexes = (await call('GET', `${db}/collectionGroups/-/indexes`)).body;
    out.fieldOverrides = (await call('GET', `${db}/collectionGroups/-/fields?filter=${encodeURIComponent('indexConfig.usesAncestorConfig=false OR ttlConfig:*')}`)).body;
  }
  for (const c of cases) {
    out.empty.push(await runCase(c, EMPTY, EMPTY_CG));
    process.stderr.write('.');
  }
  await commit(written.map(([name, data]) => ({ update: { name, fields: fields(data) } })));
  try {
    for (const c of cases) {
      out.populated.push(await runCase(c, COLL, CG));
      process.stderr.write('+');
    }
  } finally {
    await commit(written.map(([name]) => ({ delete: name })));
    const list = async (p) => ((await call('GET', `${root}/${p}?pageSize=100&showMissing=true`)).body.documents || []).map((d) => d.name);
    const cgq = await call('POST', `${root}:runQuery`, { structuredQuery: { from: [{ collectionId: CG, allDescendants: true }] } });
    out.cleanup = {
      deleted: written.map(([n]) => n.replace(root, '{root}')),
      listTopLevel: await list(COLL),
      listSub_d1: await list(`${COLL}/d1/${CG}`),
      listSub_d2: await list(`${COLL}/d2/${CG}`),
      collectionGroupDocsRemaining: (Array.isArray(cgq.body) ? cgq.body : []).filter((x) => x.document).length,
    };
  }
  out.finishedAt = new Date().toISOString();
  process.stderr.write('\n');
  return out;
}

// ---- normalising into the corpus ----

// The message templates production uses for a missing index (verified on the
// 45 refusals of 2026-10-26). {url} is the console link.
const TEMPLATES = [
  ['multiple-range-fields', /^The query requires an index\. You can create it here: (?<url>\S+) \.\nThe query contains range and inequality filters on multiple fields, please refer to the documentation for index selection best practices: https:\/\/cloud\.google\.com\/firestore\/docs\/query-data\/multiple-range-fields\.$/,
    'The query requires an index. You can create it here: {url} .\nThe query contains range and inequality filters on multiple fields, please refer to the documentation for index selection best practices: https://cloud.google.com/firestore/docs/query-data/multiple-range-fields.'],
  ['index', /^The query requires an index\. You can create it here: (?<url>\S+)$/,
    'The query requires an index. You can create it here: {url}'],
  ['multiple-indexes', /^The query requires multiple indexes\. You can create them here:(?<url>\S+)$/,
    'The query requires multiple indexes. You can create them here:{url}'],
  ['collection-group-single-field', /^The query requires a (?<kind>COLLECTION_GROUP_(?:ASC|DESC|CONTAINS)) index for collection (?<collection>\S+) and field (?<field>\S+)\. You can create it here: (?<url>\S+)$/,
    'The query requires a {kind} index for collection {collection} and field {field}. You can create it here: {url}'],
];

function template(message) {
  for (const [id, re, text] of TEMPLATES) {
    const m = re.exec(message);
    if (m) return { id, text, args: { ...m.groups } };
  }
  return null;
}

// decodeIndex reads the base64url payload of a create_* parameter: a
// serialised google.firestore.admin.v1.Index (1 name, 2 query_scope,
// 3 fields: 1 field_path, 2 order, 3 array_config).
function decodeIndex(payload) {
  const buf = Buffer.from(payload.replace(/-/g, '+').replace(/_/g, '/'), 'base64');
  const read = (b) => {
    let i = 0;
    const out = [];
    const varint = () => {
      let r = 0, s = 0;
      for (;;) {
        const x = b[i++];
        r += (x & 0x7f) * 2 ** s;
        if (!(x & 0x80)) return r;
        s += 7;
      }
    };
    while (i < b.length) {
      const key = varint();
      const fn = key >>> 3, wt = key & 7;
      if (wt === 0) out.push([fn, varint()]);
      else if (wt === 2) {
        const n = varint();
        out.push([fn, b.subarray(i, i + n)]);
        i += n;
      } else throw new Error(`wire type ${wt}`);
    }
    return out;
  };
  const SCOPE = { 1: 'COLLECTION', 2: 'COLLECTION_GROUP', 3: 'COLLECTION_RECURSIVE' };
  const ORDER = { 1: 'ASCENDING', 2: 'DESCENDING' };
  const idx = { fields: [] };
  for (const [fn, val] of read(buf)) {
    if (fn === 1) idx.name = val.toString('utf8');
    else if (fn === 2) idx.queryScope = SCOPE[val] ?? val;
    else if (fn === 3) {
      const f = {};
      for (const [gn, gv] of read(val)) {
        if (gn === 1) f.fieldPath = gv.toString('utf8');
        else if (gn === 2) f.order = ORDER[gv] ?? gv;
        else if (gn === 3) f.arrayConfig = gv === 1 ? 'CONTAINS' : gv;
        else f[`field${gn}`] = typeof gv === 'number' ? gv : gv.toString('base64');
      }
      idx.fields.push(f);
    } else idx[`field${fn}`] = typeof val === 'number' ? val : val.toString('base64');
  }
  return idx;
}

function links(url) {
  const u = new URL(url);
  return [...u.searchParams.entries()].filter(([k]) => k.startsWith('create_')).map(([param, payload]) => ({ param, payload, index: decodeIndex(payload) }));
}

function outcome(r, coll) {
  if (r.ok) {
    const o = { verdict: 'ok', http: r.http };
    if (r.ids) o.ids = r.ids;
    if (r.aggregates) o.aggregates = r.aggregates;
    return coll ? { collection: coll, ...o } : o;
  }
  const t = template(r.message);
  const o = {
    verdict: r.status,
    http: r.http,
    status: r.status,
    message: r.message,
    bodyIsArray: Array.isArray(r.rawBody),
  };
  if (t) {
    o.template = t;
    o.links = links(t.args.url);
  }
  return coll ? { collection: coll, ...o } : o;
}

const KIND = { q: 'query', cg: 'collection-group', agg: 'aggregation', cgagg: 'collection-group-aggregation' };

function convert(raw, generator, extraVersions) {
  const emptyById = new Map(raw.empty.map((c) => [c.id, c]));
  const out = raw.populated.map((p) => {
    const e = emptyById.get(p.id);
    const body = JSON.parse(JSON.stringify(p.request.body).replaceAll(`projects/${raw.project}/databases/(default)/documents`, '{root}'));
    return {
      id: p.id,
      description: p.desc,
      kind: KIND[p.kind],
      request: { method: 'POST', path: p.request.path.replace('{root}', 'projects/{project}/databases/(default)/documents'), body },
      ...outcome(p),
      emptyCollection: outcome(e, e.kind === 'cg' || e.kind === 'cgagg' ? EMPTY_CG : EMPTY),
    };
  });
  const prod = raw.mode === 'prod';
  const header = {
    oracle: prod ? 'production' : 'emulator',
    target: prod
      ? `${raw.project} (default): ${raw.database.type}, ${raw.database.databaseEdition}, ${raw.database.concurrencyMode}, ${raw.database.locationId}; firestore.googleapis.com REST v1`
      : `Firestore emulator (project ${raw.project}), REST v1`,
    generator: generator,
    recordedAt: `${raw.startedAt} .. ${raw.finishedAt}`,
    versions: extraVersions,
    notes: [
      'Each shape ran twice: on a never-written collection (emptyCollection) and on the populated collection aruma-ref-idx (6 documents, plus 5 collection-group members aruma-ref-idxcg).',
      'request.body names the database root as {root}; the cursor and __name__ references point into the queried collection.',
      prod ? 'The project had no composite index and no field override (indexes and fieldOverrides below), so every verdict is the automatic-index rule.' : 'The emulator enforces no composite index (firebase.google.com/docs/emulator-suite/connect_firestore).',
      'gRPC status code for FAILED_PRECONDITION is 9; only REST was recorded, where it is HTTP 400 and the streaming endpoints wrap the error object in a JSON array (bodyIsArray).',
      'template.text with template.args substituted reproduces message byte for byte; links[].index is the decoded payload (google.firestore.admin.v1.Index field numbers).',
    ],
  };
  const body = {
    database: raw.database,
    cases: out,
    cleanup: raw.cleanup,
  };
  if (prod) {
    body.indexes = raw.indexes;
    body.fieldOverrides = raw.fieldOverrides;
  }
  return { header, body };
}

async function main() {
  const imp = opt('--import');
  const emu = opt('--emulator');
  let raw, generator, ver;
  if (imp) {
    raw = JSON.parse(readFileSync(imp, 'utf8'));
    generator = `.cache/briefs/research/artifacts/composite-index-probe.mjs ${raw.mode} (the same shapes as test/oracle/documents/production/record-indexes.mjs), converted by record-indexes.mjs --import ${basename(imp)}`;
    ver = raw.mode === 'prod'
      ? { api: 'firestore.googleapis.com REST v1', node: 'v24.21.0', 'firebase-tools (auth only)': '13.23.0' }
      : { emulator: 'cloud-firestore-emulator-v1.22.0.jar (firebase-tools cache)', java: 'OpenJDK 21 (Homebrew openjdk@21), -Xmx384m', node: 'v24.21.0' };
  } else {
    raw = await runProbe(emu ? 'emu' : 'prod', emu);
    generator = `test/oracle/documents/production/record-indexes.mjs${emu ? ' --emulator' : ''}`;
    ver = emu ? versions({ emulator: process.env.EMULATOR_VERSION || 'unknown (set EMULATOR_VERSION)' }) : versions({ api: 'firestore.googleapis.com REST v1' });
  }
  const { header, body } = convert(raw, generator, ver);
  const left = raw.cleanup;
  if (left && (left.listTopLevel.length || left.listSub_d1.length || left.listSub_d2.length || left.collectionGroupDocsRemaining)) {
    throw new Error('cleanup verification failed: documents remain under aruma-ref-idx');
  }
  writeCorpus(raw.mode === 'prod' ? 'indexes-105.json' : 'indexes-105.emulator.json', header, body);
}

main().catch((e) => {
  console.error('record-indexes failed:', e.message);
  process.exit(1);
});
