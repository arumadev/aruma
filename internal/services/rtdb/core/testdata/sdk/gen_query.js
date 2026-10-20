#!/usr/bin/env node
// gen_query.js - computes query vectors with the official Firebase JS SDK: for each data set
// and query, the wire query object the SDK sends (queryParamsGetQueryObject) and the
// children its own filter keeps (queryParamsGetNodeFilter(params).updateFullNode), in index
// order. The server must select the same window: the client re-applies the query to what
// it receives and cannot fetch what the server left out.
//
// Usage: node gen_query.js <path to node_modules/@firebase/database> [outDir]
//
// The bundle is loaded as gen.js loads it: compiled in memory with a trailing export of
// internal functions; nothing is written into node_modules.
'use strict';

const Module = require('module');
const fs = require('fs');
const path = require('path');

const pkgDir = path.resolve(process.argv[2] || '');
const outDir = path.resolve(process.argv[3] || __dirname);
if (!process.argv[2]) {
  console.error('usage: node gen_query.js <path to @firebase/database package> [outDir]');
  process.exit(2);
}
const pkg = JSON.parse(fs.readFileSync(path.join(pkgDir, 'package.json'), 'utf8'));
const bundlePath = path.join(pkgDir, pkg.main);
const HOOKED = [
  'nodeFromJSON', 'PathIndex', 'Path', 'ChildrenNode', 'PRIORITY_INDEX', 'VALUE_INDEX', 'KEY_INDEX',
  'QueryParams', 'queryParamsGetNodeFilter', 'queryParamsGetQueryObject', 'queryParamsOrderBy',
  'queryParamsStartAt', 'queryParamsStartAfter', 'queryParamsEndAt', 'queryParamsEndBefore',
  'queryParamsLimitToFirst', 'queryParamsLimitToLast',
];
const src = fs.readFileSync(bundlePath, 'utf8') + `\nmodule.exports.__t = { ${HOOKED.join(', ')} };\n`;
let appStub = false;
const mod = new Module(bundlePath, null);
mod.filename = bundlePath;
mod.paths = Module._nodeModulePaths(path.dirname(bundlePath));
const origRequire = mod.require.bind(mod);
mod.require = (id) => {
  if (id === '@firebase/app') {
    try {
      return origRequire(id);
    } catch {
      appStub = true;
      return { SDK_VERSION: '0.0.0-stub', _registerComponent() {}, registerVersion() {}, _isFirebaseServerApp: () => false };
    }
  }
  return origRequire(id);
};
mod._compile(src, bundlePath);
const T = mod.exports.__t;

function params(order, ops) {
  let p = new T.QueryParams();
  if (order === 'key') p = T.queryParamsOrderBy(p, T.KEY_INDEX);
  else if (order === 'value') p = T.queryParamsOrderBy(p, T.VALUE_INDEX);
  else if (order.startsWith('child:')) p = T.queryParamsOrderBy(p, new T.PathIndex(new T.Path(order.slice(6))));
  for (const [op, ...args] of ops) {
    switch (op) {
      case 'startAt': p = T.queryParamsStartAt(p, ...args); break;
      case 'startAfter': p = T.queryParamsStartAfter(p, ...args); break;
      case 'endAt': p = T.queryParamsEndAt(p, ...args); break;
      case 'endBefore': p = T.queryParamsEndBefore(p, ...args); break;
      case 'equalTo': p = T.queryParamsEndAt(T.queryParamsStartAt(p, ...args), ...args); break;
      case 'limitToFirst': p = T.queryParamsLimitToFirst(p, args[0]); break;
      case 'limitToLast': p = T.queryParamsLimitToLast(p, args[0]); break;
      default: throw new Error(op);
    }
  }
  return p;
}

function window(data, p) {
  const node = T.nodeFromJSON(data);
  const filter = T.queryParamsGetNodeFilter(p);
  const out = filter.updateFullNode(T.ChildrenNode.EMPTY_NODE, node.withIndex(p.getIndex()), null);
  const keys = [];
  out.forEachChild(p.getIndex(), (k) => { keys.push(k); });
  return keys;
}

const data = {
  mixed: {
    a: { s: 3, n: 'x' }, b: { s: 1 }, c: { '.priority': 2, s: 2 }, d: { s: 0 }, e: { '.priority': 1, s: 'str' },
    f: 5, g: { t: 1 }, h: { s: true }, i: { s: false }, j: { s: null, u: 1 }, '1': { s: 1 }, '10': { s: 1 }, '2': { s: -1 },
  },
  scores: { alice: { score: 10, meta: { at: 3 } }, bob: { score: 7, meta: { at: 1 } }, carol: { score: 10, meta: { at: 2 } }, dave: { score: 2 }, eve: { score: 'x' } },
  leaves: { a: 'b', b: 3, c: true, d: false, e: 'a', f: 3, g: { x: 1 }, h: -1, '5': 0 },
  priorities: { a: { '.value': 1, '.priority': 'b' }, b: { '.value': 2, '.priority': 3 }, c: 3, d: { '.value': 4, '.priority': 'a' }, e: { '.value': 5, '.priority': 3 }, f: { '.value': 6, '.priority': -1 } },
};

const queries = [
  ['priority', []],
  ['priority', [['limitToFirst', 2]]],
  ['priority', [['limitToLast', 3]]],
  ['priority', [['startAt', 3]]],
  ['priority', [['startAt', 3, 'e']]],
  ['priority', [['startAt', 'a'], ['endAt', 'b']]],
  ['priority', [['startAfter', 3]]],
  ['priority', [['endBefore', 3]]],
  ['priority', [['startAt', null], ['endAt', null]]],
  ['priority', [['equalTo', 3]]],
  ['priority', [['startAt', 3], ['limitToLast', 1]]],
  ['key', []],
  ['key', [['startAt', 'b'], ['endAt', 'e']]],
  ['key', [['startAfter', 'b'], ['endBefore', 'e']]],
  ['key', [['limitToFirst', 3]]],
  ['key', [['limitToLast', 2]]],
  ['key', [['equalTo', 'c']]],
  ['key', [['startAt', '1'], ['endAt', '2']]],
  ['key', [['startAt', 'c'], ['limitToFirst', 2]]],
  ['value', []],
  ['value', [['limitToFirst', 3]]],
  ['value', [['limitToLast', 3]]],
  ['value', [['startAt', 3]]],
  ['value', [['startAt', 3, 'f']]],
  ['value', [['startAfter', 3]]],
  ['value', [['endBefore', 'b']]],
  ['value', [['equalTo', true]]],
  ['value', [['startAt', false], ['endAt', 3]]],
  ['value', [['startAt', 'a']]],
  ['child:s', []],
  ['child:s', [['limitToFirst', 4]]],
  ['child:s', [['limitToLast', 4]]],
  ['child:s', [['startAt', 1]]],
  ['child:s', [['startAt', 1, 'b']]],
  ['child:s', [['startAfter', 1]]],
  ['child:s', [['startAfter', 1, '1']]],
  ['child:s', [['endAt', 1]]],
  ['child:s', [['endBefore', 1]]],
  ['child:s', [['endBefore', 1, 'b']]],
  ['child:s', [['equalTo', 1]]],
  ['child:s', [['equalTo', null]]],
  ['child:s', [['startAt', false], ['endAt', true]]],
  ['child:s', [['startAt', 'a'], ['endAt', 'z']]],
  ['child:s', [['startAt', 0], ['limitToLast', 2]]],
  ['child:s', [['startAt', 0], ['endAt', 10], ['limitToFirst', 2]]],
  ['child:score', [['limitToLast', 2]]],
  ['child:score', [['equalTo', 10]]],
  ['child:score', [['startAt', 7], ['endAt', 10]]],
  ['child:meta/at', []],
  ['child:meta/at', [['startAt', 2]]],
  ['child:meta/at', [['limitToFirst', 2]]],
];

const vectors = [];
for (const [name, d] of Object.entries(data)) {
  for (const [order, ops] of queries) {
    const p = params(order, ops);
    vectors.push({ data: name, order, ops, wire: T.queryParamsGetQueryObject(p), keys: window(d, p) });
  }
}

const out = {
  source: {
    package: '@firebase/database',
    version: pkg.version,
    bundle: pkg.main,
    generator: 'gen_query.js',
    appStub,
    note: 'keys = the children queryParamsGetNodeFilter(params).updateFullNode keeps, in index order; wire = queryParamsGetQueryObject(params).',
  },
  references: [
    { repo: 'firebase-js-sdk', commit: 'd7bb0e16f2b13ba15f74b690596eb70ef145d4f6', file: 'packages/database/src/core/view/QueryParams.ts', what: 'query parameters, startAfter/endBefore sentinels, the wire query object' },
    { repo: 'firebase-js-sdk', commit: 'd7bb0e16f2b13ba15f74b690596eb70ef145d4f6', file: 'packages/database/src/core/view/filter/RangedFilter.ts', what: 'range bounds as posts, inclusive or not' },
    { repo: 'firebase-js-sdk', commit: 'd7bb0e16f2b13ba15f74b690596eb70ef145d4f6', file: 'packages/database/src/core/view/filter/LimitedFilter.ts', what: 'limit from the left or the right' },
  ],
  data,
  vectors,
};
fs.writeFileSync(path.join(outDir, 'query.json'), JSON.stringify(out, null, 2) + '\n');
console.log(`query.json: ${vectors.length} vectors`);
