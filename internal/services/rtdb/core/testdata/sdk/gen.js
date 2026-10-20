#!/usr/bin/env node
// gen.js - computes hash / ordering / export vectors with the official
// Firebase JS SDK (@firebase/database) compiled Node bundle.
//
// Usage: node gen.js <path to node_modules/@firebase/database> [outDir]
//
// The bundle named by package.json "main" (dist/index.node.cjs.js) is read,
// two lines are patched in memory (a hook in sha1() that records the
// pre-hash string, and a trailing module.exports.__t = {...} exposing
// internal functions), and it is compiled as a module located at the
// original bundle path so its dependencies (@firebase/util, ...) resolve from
// the package's own node_modules. Nothing is written into node_modules.
// If @firebase/app cannot be resolved, a stub is used: the bundle only needs
// it to register the database component, not for any of the code used here.
'use strict';

const Module = require('module');
const fs = require('fs');
const path = require('path');

const pkgDir = path.resolve(process.argv[2] || '');
const outDir = path.resolve(process.argv[3] || __dirname);
if (!process.argv[2]) {
  console.error('usage: node gen.js <path to @firebase/database package> [outDir]');
  process.exit(2);
}
const pkg = JSON.parse(fs.readFileSync(path.join(pkgDir, 'package.json'), 'utf8'));
const bundleRel = pkg.main;
const bundlePath = path.join(pkgDir, bundleRel);
let src = fs.readFileSync(bundlePath, 'utf8');

const SHA1_DECL = 'const sha1 = function (str) {';
if (src.split(SHA1_DECL).length !== 2) throw new Error('sha1 declaration not found exactly once');
src = src.replace(SHA1_DECL, SHA1_DECL + ' if (globalThis.__sha1Inputs) globalThis.__sha1Inputs.push(str);');
const HOOKED = [
  'nodeFromJSON',
  'nameCompare',
  'stringCompare',
  'tryParseInt',
  'doubleToIEEE754String',
  'priorityHashText',
  'sha1',
  'NamedNode',
  'PathIndex',
  'Path',
  'LeafNode',
  'ChildrenNode',
  'PRIORITY_INDEX',
  'VALUE_INDEX',
  'KEY_INDEX',
];
src += `\nmodule.exports.__t = { ${HOOKED.join(', ')} };\n`;

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
      return {
        SDK_VERSION: '0.0.0-stub',
        _registerComponent() {},
        registerVersion() {},
        _isFirebaseServerApp() {
          return false;
        },
        getApp() {
          throw new Error('stub');
        },
        _getProvider() {
          throw new Error('stub');
        },
      };
    }
  }
  return origRequire(id);
};
mod._compile(src, bundlePath);
const T = mod.exports.__t;
const { nodeFromJSON, nameCompare, NamedNode, PathIndex, Path, PRIORITY_INDEX, VALUE_INDEX } = T;

const SDK_COMMIT = 'd7bb0e16f2b13ba15f74b690596eb70ef145d4f6';
const source = {
  package: '@firebase/database',
  version: pkg.version,
  bundle: bundleRel,
  generator: 'gen.js',
  appStub: undefined, // filled below
  hooks: HOOKED,
  note: 'sha1() patched in memory to record its input (the canonical pre-hash string); no other code changed.',
};
const R = (file, line, what) => ({ file: `packages/database/src/${file}`, line, what });
const refsCommon = { repo: 'firebase-js-sdk', commit: SDK_COMMIT };
const references = {
  hash: [
    R('core/snap/LeafNode.ts', 183, 'LeafNode.hash: [priority:<priorityHashText>:]<typeof value>:<value or doubleToIEEE754String>'),
    R('core/snap/ChildrenNode.ts', 235, 'ChildrenNode.hash: [priority:<p>:] then :<key>:<childHash> per child in PRIORITY_INDEX order, skipping empty child hashes; "" stays ""'),
    R('core/snap/snap.ts', 31, 'priorityHashText: number:<ieee754 hex> | string:<s>'),
    R('core/util/util.ts', 52, 'sha1: SHA-1 of UTF-8 bytes, base64 (standard alphabet, padded)'),
    R('core/util/util.ts', 393, 'doubleToIEEE754String: 16 lowercase hex chars, big-endian'),
    R('core/snap/nodeFromJSON.ts', 40, 'nodeFromJSON: .priority/.value handling, null children dropped, arrays as objects'),
    R('core/snap/indexes/PriorityIndex.ts', 36, 'PriorityIndex.compare (child order used by hash)'),
  ],
  order: [
    R('core/util/util.ts', 254, 'nameCompare: int32-like keys first numerically (tie: shorter first), then strings by JS < (UTF-16 code units)'),
    R('core/util/util.ts', 496, 'INTEGER_REGEXP_ = /^-?(0*)\\d{1,10}$/'),
    R('core/util/util.ts', 511, 'tryParseInt: within [-2147483648, 2147483647]'),
    R('core/util/util.ts', 282, 'stringCompare'),
    R('core/snap/indexes/ValueIndex.ts', 25, 'ValueIndex.compare: node.compareTo then nameCompare'),
    R('core/snap/LeafNode.ts', 212, 'LeafNode.compareTo: leaf < non-empty children; empty < leaf'),
    R('core/snap/LeafNode.ts', 226, 'compareToLeafNode_: type order via VALUE_TYPE_ORDER (LeafNode.ts:55) object, boolean, number, string; same type: JS < and ==='),
    R('core/snap/ChildrenNode.ts', 386, 'ChildrenNode.compareTo: empty first, any two non-empty children nodes equal'),
    R('core/snap/indexes/PriorityIndex.ts', 36, 'PriorityIndex.compare: priority node compareTo, then nameCompare'),
    R('core/snap/indexes/PathIndex.ts', 44, 'PathIndex.compare: child node compareTo, then nameCompare'),
    R('core/snap/indexes/KeyIndex.ts', 36, 'KeyIndex.compare: nameCompare'),
  ],
  export: [
    R('core/snap/ChildrenNode.ts', 197, 'ChildrenNode.val: array when all keys match /^(0|[1-9]\\d*)$/ and maxKey < 2*numKeys (not in export format); export adds .priority'),
    R('core/snap/LeafNode.ts', 171, 'LeafNode.val: export format {.value,.priority} when priority set'),
    R('core/snap/nodeFromJSON.ts', 40, 'nodeFromJSON'),
  ],
};

const sign = (x) => (x < 0 ? -1 : x > 0 ? 1 : 0);
const write = (name, obj) => {
  const f = path.join(outDir, name);
  fs.writeFileSync(f, JSON.stringify(obj, null, 2) + '\n');
  console.log('wrote', f);
};

// ------------------------------------------------------------------ hash
function hashOf(json) {
  globalThis.__sha1Inputs = [];
  const node = nodeFromJSON(json);
  const h = node.hash();
  const inputs = globalThis.__sha1Inputs;
  globalThis.__sha1Inputs = null;
  const canonical = h === '' ? '' : inputs[inputs.length - 1];
  if (h !== '' && T.sha1(canonical) !== h) throw new Error('canonical capture mismatch');
  return { hash: h, canonical };
}

const deep = (n) => {
  let d = 'leaf';
  for (let i = 0; i < n; i++) d = { ['l' + (n - i)]: d };
  return d;
};
const wide = () => {
  const o = {};
  for (let i = 0; i < 50; i++) o['k' + String(i).padStart(2, '0')] = i % 3 === 0 ? i : i % 3 === 1 ? 'v' + i : i % 2 === 0;
  return o;
};

const hashCases = [
  ['null', null],
  ['true', true],
  ['false', false],
  ['zero', 0],
  ['negative zero (json text -0)', -0, '-0'],
  ['one', 1],
  ['minus one', -1],
  ['1.5', 1.5],
  ['-2.25', -2.25],
  ['0.1', 0.1],
  ['3.14159', 3.14159],
  ['100', 100],
  ['1e21', 1e21],
  ['1e-7', 1e-7],
  ['1e300', 1e300],
  ['-1e-300', -1e-300],
  ['2^31-1', 2147483647],
  ['-2^31', -2147483648],
  ['2^32', 4294967296],
  ['2^53', 2 ** 53],
  ['2^53+2', 2 ** 53 + 2],
  ['max double', 1.7976931348623157e308],
  ['min denormal', 5e-324],
  ['min normal 2^-1022', 2.2250738585072014e-308],
  ['123456789012', 123456789012],
  ['2^53-1 (SDK ieee754 differs)', 9007199254740991],
  ['2^-29 (SDK ieee754 differs)', 2 ** -29],
  ['3.9999999999999996 (SDK ieee754 differs)', 3.9999999999999996],
  ['1e-300 (SDK ieee754 differs)', 1e-300],
  ['empty string', ''],
  ['a', 'a'],
  ['string "1"', '1'],
  ['string "true"', 'true'],
  ['string "null"', 'null'],
  ['arabic', 'مرحبا'],
  ['japanese', '日本'],
  ['emoji', '😀'],
  ['unicode mix', 'a-日本-😀-مرحبا é'],
  ['quotes', 'say "hi"'],
  ['backslash', 'back\\slash'],
  ['colons', 'a:b:c:'],
  ['newline and tab', 'line1\nline2\tend'],
  ['string with priority: prefix', 'priority:number:3ff0000000000000:'],
  ['new (datastale txmissing value)', 'new'],
  ['{s:1} (datastale value)', { s: 1 }],
  ['{s:2}', { s: 2 }],
  ['{a:1}', { a: 1 }],
  ['{a:1,b:2}', { a: 1, b: 2 }],
  ['{b:2,a:1} (same as {a:1,b:2})', { b: 2, a: 1 }],
  ['nested 3 levels', { a: { b: { c: 1 } } }],
  ['mixed types', { s: 'x', n: 1, b: true, f: false, o: { x: 1 } }],
  [
    'int-like keys',
    { '1': 'i1', '2': 'i2', '10': 'i10', '-1': 'm1', '001': 'z1', '2147483648': 'big', '-2147483649': 'mbig', a: 'a', A: 'A', _: 'u' },
  ],
  ['numeric key order', { b: 1, a: 2, '10': 3, '9': 4 }],
  ['unicode keys', { '日本': 1, '😀': 2, a: 3, '￿': 4 }],
  ['object with null child', { a: 1, b: null }],
  ['object with only null children', { a: null, b: null }],
  ['empty object', {}],
  ['object with empty child', { a: {}, b: 1 }],
  ['array [1,2,3]', [1, 2, 3]],
  ['array ["a",null,"c"]', ['a', null, 'c']],
  ['empty array', []],
  ['nested arrays', [[1, 2], [3]]],
  ['leaf priority number', { '.value': 'v', '.priority': 7 }],
  ['leaf priority string', { '.value': 1, '.priority': 'p' }],
  ['leaf priority 0', { '.value': true, '.priority': 0 }],
  ['leaf priority negative', { '.value': 'x', '.priority': -3 }],
  ['leaf priority float', { '.value': 2, '.priority': 1.5 }],
  ['leaf priority numeric string', { '.value': 'x', '.priority': '7' }],
  ['leaf priority null', { '.value': 1, '.priority': null }],
  ['object priority', { '.priority': 2, a: 1 }],
  ['object string priority with child priority', { '.priority': 's', b: { '.value': 1, '.priority': 3 } }],
  ['.value object with priority', { '.value': { a: 1 }, '.priority': 5 }],
  ['only .priority', { '.priority': 1 }],
  ['children with priorities (priority order != key order)', { a: { '.value': 1, '.priority': 3 }, b: { '.value': 2, '.priority': 1 }, c: 3 }],
  ['children with string and number priorities', { x: { '.value': 1, '.priority': 'b' }, y: { '.value': 2, '.priority': 'a' }, z: { '.value': 3, '.priority': 10 }, w: 4 }],
  ['children equal priorities tie by key', { b: { '.value': 1, '.priority': 1 }, a: { '.value': 2, '.priority': 1 }, '5': { '.value': 3, '.priority': 1 } }],
  ['deep 10 levels', deep(10)],
  ['wide 50 keys', wide()],
  ['rtdb export typical', { users: { alice: { name: 'Alice', age: 30, admin: true }, bob: { name: 'Bob', age: 25.5, tags: ['x', 'y'] } } }],
];

const hashVectors = hashCases.map(([name, json, jsonText]) => {
  const { hash, canonical } = hashOf(json);
  const v = { name, json };
  if (jsonText) v.jsonText = jsonText;
  v.hash = hash;
  v.canonical = canonical;
  return v;
});

// doubleToIEEE754String vs the true IEEE-754 bytes
function trueHex(v) {
  const b = Buffer.alloc(8);
  b.writeDoubleBE(v);
  return b.toString('hex');
}
const ieeeFixed = [0, -0, 1, -1, 0.1, 0.5, 1.5, -2.25, 3.14159, 100, 1e21, 1e-7, 1e300, 2 ** 53, 2 ** 53 + 2,
  1.7976931348623157e308, 5e-324, 2.2250738585072014e-308, 2.225073858507201e-308, 2147483647, -2147483648, 4294967296, 123456789012,
  // known SDK/IEEE differences (see notes)
  9007199254740991, -9007199254740991, 2 ** -29, 2 ** -31, 3.9999999999999996, 2147483647.9999998, 1e-300, -1e-300, 2 ** -971, 2 ** -972, 2 ** -1000];
// Scan 1: 2^k, its predecessor and successor, for every k.
const scanPow2 = [];
let pow2Checked = 0;
for (let k = -1074; k <= 1023; k++) {
  const p = 2 ** k;
  for (const v of [p, p * (1 - 2 ** -53), p * (1 + 2 ** -52)]) {
    if (!Number.isFinite(v) || v === 0) continue;
    pow2Checked++;
    if (T.doubleToIEEE754String(v) !== trueHex(v)) scanPow2.push({ k, v });
  }
}
// Scan 2: deterministic pseudo-random bit patterns (all exponents equally likely).
let seed = 12345;
const rnd = () => (seed = (seed * 1103515245 + 12345) % 2147483648) / 2147483648;
let randomChecked = 0;
let randomBad = 0;
let randomBadMinExp = Infinity;
let randomBadMaxExp = -Infinity;
for (let i = 0; i < 200000; i++) {
  const b = Buffer.alloc(8);
  for (let j = 0; j < 8; j++) b[j] = Math.floor(rnd() * 256);
  const v = b.readDoubleBE();
  if (!Number.isFinite(v)) continue;
  randomChecked++;
  if (T.doubleToIEEE754String(v) !== trueHex(v)) {
    randomBad++;
    const e = ((b.readUInt16BE(0) >> 4) & 0x7ff) - 1023;
    randomBadMinExp = Math.min(randomBadMinExp, e);
    randomBadMaxExp = Math.max(randomBadMaxExp, e);
  }
}
const ieeeValues = [...ieeeFixed, ...scanPow2.filter((x) => x.k >= -60 && x.k <= 64).map((x) => x.v)];
const ieee = [...new Set(ieeeValues)].map((v) => {
  const sdk = T.doubleToIEEE754String(v);
  const t = trueHex(v);
  return { value: v, jsonText: Object.is(v, -0) ? '-0' : undefined, sdk, ieee754: t, equal: sdk === t };
});
const ieeeNote =
  `ieee754 compares the SDK's doubleToIEEE754String (used for every number in a hash) with the true IEEE-754 big-endian hex. ` +
  `They differ in two classes: (1) |v| < 2^-971 with a normal exponent: Math.pow(2, 52 - ln) overflows to Infinity and the mantissa bits come out as 0 ` +
  `(random scan: ${randomBad} of ${randomChecked} pseudo-random finite doubles differ, all with unbiased exponent in [${randomBadMinExp}, ${randomBadMaxExp}]); ` +
  `(2) values at or next to a power of two, where floor(log(v)/LN2) is off by one (scan of 2^k, its predecessor and successor: ${scanPow2.length} of ${pow2Checked} differ, ` +
  `e.g. 2^53-1 = 9007199254740991, 2^-29, 3.9999999999999996). The list contains fixed values plus every scan mismatch with k in [-60, 64]. ` +
  `A server that must produce the same hashes as JS clients has to reproduce this algorithm, not the true bits.`;

write('hash.json', {
  source: { ...source, appStub },
  references: references.hash.map((r) => ({ ...refsCommon, ...r })),
  notes: [
    'json is the input in export format; it goes through nodeFromJSON(json) and hash is node.hash().',
    'canonical is the exact string passed to sha1() for the top-level node (captured by hooking sha1); "" means no sha1 call (empty node, hash "").',
    'jsonText, when present, is the exact JSON text of the input when JSON.stringify cannot express it (negative zero).',
    'Child hashes are hashed recursively; children with an empty hash (null / empty objects) are skipped.',
    ieeeNote,
  ],
  vectors: hashVectors,
  ieee754: ieee,
});

// ------------------------------------------------------------------ order
const keys = ['0', '1', '2', '10', '-1', '-10', '01', '001', '00', '-0', '2147483647', '2147483648', '-2147483648', '-2147483649',
  '9999999999', '10000000000', 'a', 'b', 'A', 'Z', '_', '-', '~', 'a1', '1a', 'aa', 'a b', ' ', '日本', '😀', '￿', 'é', 'e', '!',
  '1.5', '+1', ' 1', '1e3', '٣', 'zz'];
const sortedKeys = [...keys].sort(nameCompare);
const kp = [
  ['1', '2'], ['2', '10'], ['10', '9'], ['1', '01'], ['01', '001'], ['0', '-0'], ['-0', '00'], ['-1', '0'], ['-10', '-9'],
  ['2147483647', '2147483648'], ['2147483648', 'a'], ['2147483648', '3'], ['-2147483648', '-2147483649'], ['-2147483649', '0'],
  ['9999999999', '10000000000'], ['1', 'a'], ['a', '1'], ['a', 'b'], ['A', 'a'], ['Z', '_'], ['_', 'a'], ['-', '0'], ['-', 'a'],
  ['~', 'z'], ['a1', '1a'], ['1a', '2'], ['aa', 'b'], [' ', '!'], ['é', 'z'], ['日本', '😀'], ['😀', '￿'], ['￿', '𐀀'],
  ['1.5', '2'], ['+1', '1'], [' 1', '1'], ['1e3', '2'], ['٣', '3'], ['a', 'a'], ['10', '10'], ['abc', 'abd'], ['ab', 'abc'],
  ['[MIN_NAME]', 'a'], ['a', '[MAX_NAME]'],
];
const keyPairs = kp.map(([a, b]) => ({ a, b, cmp: sign(nameCompare(a, b)) }));

const vp = [
  [null, false], [null, null], [null, 0], [null, ''], [null, { a: 1 }], [false, true], [true, true], [false, false], [true, 0],
  [true, -1e300], [false, 'a'], [0, 1], [-1, 0], [1, 1], [1, 1.0], [1.5, 2], [-2.25, -2], [1e21, 1e22], [0.1, 0.2], [5e-324, 0],
  [-0, 0], [2 ** 53, 2 ** 53 + 2], [1e300, 'a'], [0, ''], [1, '1'], ['', 'a'], ['a', 'b'], ['a', 'B'], ['B', 'a'], ['a', 'aa'],
  ['aa', 'b'], ['1', '10'], ['10', '9'], ['2', '10'], ['é', 'f'], ['￿', '😀'], ['😀', '￿'], ['日本', '😀'], ['𐀀', '￿'],
  ['a', 'a'], ['abc', 'abd'], ['Z', 'a'], ['z', '{'], ['a', { a: 1 }], [1, { a: 1 }], [true, { a: 1 }], [{ a: 1 }, { b: 2 }],
  [{ a: 1 }, { a: 1 }], [{ z: 0 }, { a: 'x' }], [{}, null], [{}, 1], [[1], { a: 1 }], ['x', { '.value': 'x', '.priority': 1 }],
  [{ '.value': 1, '.priority': 9 }, { '.value': 2, '.priority': 1 }], [{ '.value': 'a', '.priority': 'z' }, 'a'], [{ a: null }, 0],
  [-1e300, -1], [1.7976931348623157e308, ''], ['1e3', 1000], ['true', true],
];
const valuePairs = vp.map(([a, b]) => {
  const c = VALUE_INDEX.compare(new NamedNode('k', nodeFromJSON(a)), new NamedNode('k', nodeFromJSON(b)));
  const e = { a, b, cmp: sign(c) };
  if (Object.is(a, -0)) e.aJsonText = '-0';
  if (Object.is(b, -0)) e.bJsonText = '-0';
  return e;
});
const vpn = [
  ['a', 1, 'b', 1], ['b', 1, 'a', 1], ['1', 'x', 'a', 'x'], ['10', 'x', '9', 'x'], ['a', null, 'b', null], ['b', { x: 1 }, 'a', { y: 2 }],
  ['k2', true, 'k1', true], ['2', 5, '10', 5], ['a', 1, 'b', 2], ['b', 1, 'a', 2], ['001', 0, '1', 0], ['a', { x: 1 }, 'b', 'zzz'],
];
for (const [an, a, bn, b] of vpn) {
  valuePairs.push({ an, a, bn, b, cmp: sign(VALUE_INDEX.compare(new NamedNode(an, nodeFromJSON(a)), new NamedNode(bn, nodeFromJSON(b)))) });
}

const P = (v, p) => ({ '.value': v, '.priority': p });
const pp = [
  ['k1', 1, 'k2', 1], ['k1', P(1, 1), 'k2', 1], ['k1', 1, 'k2', P(1, 1)], ['k1', P(1, 1), 'k2', P(1, 2)], ['k1', P(1, 2), 'k2', P(1, 1)],
  ['k1', P(1, 5), 'k2', P(1, 'a')], ['k1', P(1, 'a'), 'k2', P(1, 5)], ['k1', P(1, 'a'), 'k2', P(1, 'b')], ['k1', P(1, 'b'), 'k2', P(1, 'B')],
  ['k2', P(1, 3), 'k1', P(1, 3)], ['k1', P(1, 3), 'k2', P(1, 3)], ['a', P(1, 'x'), 'b', P(9, 'x')], ['10', P(0, 0), '9', P(0, 0)],
  ['k1', P(1, -1), 'k2', P(1, 0)], ['k1', P(1, 0), 'k2', 1], ['k1', P(1, 1.5), 'k2', P(1, 1.25)], ['k1', P(1, 1e21), 'k2', P(1, '1')],
  ['k1', P(1, '10'), 'k2', P(1, '9')], ['k1', P(1, ''), 'k2', P(1, 0)], ['k1', P(1, ''), 'k2', 1],
  ['k1', { '.priority': 2, a: 1 }, 'k2', { '.priority': 1, a: 1 }], ['k1', { '.priority': 'b', a: 1 }, 'k2', P('x', 'a')],
  ['k1', { a: 1 }, 'k2', P(1, 1)], ['k1', { '.priority': 1, a: 1 }, 'k2', P(1, 1)], ['b', { '.priority': 1, a: 1 }, 'a', P(1, 1)],
  ['k1', null, 'k2', 1], ['k1', null, 'k2', P(1, 1)], ['a', 'z', 'b', 'a'], ['1', P(1, '😀'), '2', P(1, '￿')], ['k1', P(true, 7), 'k2', P(false, 7)],
];
const priorityPairs = pp.map(([an, a, bn, b]) => ({
  an, a, bn, b, cmp: sign(PRIORITY_INDEX.compare(new NamedNode(an, nodeFromJSON(a)), new NamedNode(bn, nodeFromJSON(b)))),
}));

const cp = [
  ['s', 'k1', { s: 1 }, 'k2', { s: 2 }], ['s', 'k1', { s: 2 }, 'k2', { s: 1 }], ['s', 'k1', { s: 1 }, 'k2', { s: 1 }],
  ['s', 'k2', { s: 1 }, 'k1', { s: 1 }], ['s', 'k1', { x: 1 }, 'k2', { s: 1 }], ['s', 'k1', { s: 1 }, 'k2', { x: 1 }],
  ['s', 'k1', { x: 1 }, 'k2', { y: 1 }], ['s', 'b', { x: 1 }, 'a', { y: 1 }], ['s', 'k1', { s: null }, 'k2', { s: false }],
  ['s', 'k1', { s: false }, 'k2', { s: true }], ['s', 'k1', { s: true }, 'k2', { s: 0 }], ['s', 'k1', { s: 99 }, 'k2', { s: '' }],
  ['s', 'k1', { s: 'a' }, 'k2', { s: 'b' }], ['s', 'k1', { s: 'z' }, 'k2', { s: { t: 1 } }], ['s', 'k1', { s: { t: 1 } }, 'k2', { s: { u: 2 } }],
  ['s', 'k1', 5, 'k2', { s: 1 }], ['s', 'k1', 'str', 'k2', 'str'], ['s', '10', { s: 1 }, '9', { s: 1 }],
  ['s', 'k1', { s: P(1, 5) }, 'k2', { s: 2 }], ['a/b', 'k1', { a: { b: 2 } }, 'k2', { a: { b: 1 } }], ['a/b', 'k1', { a: 1 }, 'k2', { a: { b: 1 } }],
  ['a/b', 'k1', { a: { c: 1 } }, 'k2', { a: { b: 'x' } }], ['a/b', 'b', { a: { b: 1 } }, 'a', { a: { b: 1 } }],
];
const childPairs = cp.map(([idx, an, a, bn, b]) => ({
  index: idx, an, a, bn, b,
  cmp: sign(new PathIndex(new Path(idx)).compare(new NamedNode(an, nodeFromJSON(a)), new NamedNode(bn, nodeFromJSON(b)))),
}));

write('order.json', {
  source: { ...source, appStub },
  references: references.order.map((r) => ({ ...refsCommon, ...r })),
  notes: [
    'cmp is the sign (-1, 0, 1) of the SDK comparator result; the SDK may return any negative/positive number.',
    'sortedKeys = keys sorted with nameCompare. keyPairs: sign(nameCompare(a, b)); [MIN_NAME]/[MAX_NAME] pairs show the sentinels.',
    'valuePairs without an/bn: sign(VALUE_INDEX.compare(NamedNode("k", nodeFromJSON(a)), NamedNode("k", nodeFromJSON(b)))); with an/bn the given names are used (key tiebreak).',
    'priorityPairs: sign(PRIORITY_INDEX.compare(NamedNode(an, nodeFromJSON(a)), NamedNode(bn, nodeFromJSON(b)))); a/b in export format.',
    'childPairs: sign(new PathIndex(new Path(index)).compare(NamedNode(an, nodeFromJSON(a)), NamedNode(bn, nodeFromJSON(b)))).',
    'aJsonText/bJsonText give the exact JSON text when the value is negative zero.',
    'Strings compare with JS < (UTF-16 code units), so "😀" (U+1F600, surrogates D83D DE00) sorts before "￿".',
  ],
  keys,
  sortedKeys,
  keyPairs,
  valuePairs,
  priorityPairs,
  childPairs,
});

// ------------------------------------------------------------------ export
const ex = [
  { '0': 'a', '1': 'b' }, { '0': 'a', '2': 'c' }, { '0': 'a', '3': 'c' }, { '1': 'a' }, { '2': 'a' }, { '1': 'a', '2': 'b' },
  { '0': 'a', b: 1 }, { '-1': 'x', '0': 'y' }, { '00': 'x' }, { '01': 'x', '1': 'y' }, { '0': 'a', '1': null, '2': 'c' },
  { '5': 'a', '6': 'b', '7': 'c' }, { '4': 'a', '5': 'b', '6': 'c' }, ['a', 'b', 'c'], ['a', null, 'c'], [null, 'b'], [[1, 2], [3, [4]]],
  [{ a: 1 }, { b: 2 }], [], {}, null, { a: null }, { a: 1, b: null }, { a: {}, b: 1 }, 1, 1.0, 1.5, -0, 'str', true, false,
  P('v', 7), P(1, 'p'), { '.priority': 2, a: 1 }, { '.priority': 's', b: P(1, 3) }, { '.value': { a: 1 }, '.priority': 5 },
  { '0': P('a', 1), '1': 'b' }, { '.priority': 1, '0': 'a', '1': 'b' }, { a: { '0': 'x', '1': 'y' }, b: { '1': 'z' } },
  { '2147483648': 'x', '0': 'y' }, { '0': 'a', '1': 'b', x: { '.priority': 3, '0': 1 } }, { a: [1, null, 3, null] },
  { '0': 'a', '10': 'b' }, { '0': 1, '1': 2, '2': 3, '3': 4, '9': 5 },
];
const exportVectors = ex.map((json) => {
  const node = nodeFromJSON(json);
  const v = { json, val: node.val(false), export: node.val(true) };
  if (Object.is(json, -0)) v.jsonText = '-0';
  return v;
});

write('export.json', {
  source: { ...source, appStub },
  references: references.export.map((r) => ({ ...refsCommon, ...r })),
  notes: [
    'val = nodeFromJSON(json).val(false); export = nodeFromJSON(json).val(true). Values are serialized with JSON.stringify.',
    'Arrays produced by val(false) may have holes (sparse keys); JSON.stringify writes holes as null, so ["a",null,"c"] here may mean a hole at index 1.',
    'The array rule: all keys match /^(0|[1-9]\\d*)$/ and maxKey < 2 * numKeys -> array (non-export only).',
    'JS has one number type: 1 and 1.0 are the same value and serialize as 1; -0 serializes as 0 (jsonText "-0" marks it).',
    'Input JS objects are serialized here as JSON.stringify writes them: integer-like keys come first in ascending order (JS property order), which does not affect the result.',
  ],
  vectors: exportVectors,
});
