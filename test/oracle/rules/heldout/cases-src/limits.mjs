// Limits: function call depth, recursion, let bindings per function,
// evaluated expressions per request, ruleset size, and get()/exists() calls.
const DOC = 'DOC';

export const topic = 'limits';
export const service = 'firestore';
export const resource = DOC;

const fsRules = (fns, cond, coll = 'lim') => `rules_version = '2';
service cloud.firestore {
  match /databases/{database}/documents {
${fns}
    match /${coll}/{doc} {
      allow get: if ${cond};
    }
  }
}
`;

const alone = (id, desc, rules, more = {}) => ({ id, desc, rules, path: 'lim/doc', ...more });

// A chain f1 -> f2 -> ... -> fN; fN returns true. Calling f1 from the
// condition makes N nested calls.
function chain(n) {
  const fns = [];
  for (let i = 1; i < n; i++) fns.push(`    function f${i}() { return f${i + 1}(); }`);
  fns.push(`    function f${n}() { return true; }`);
  return fns.join('\n');
}

// A function with k let bindings, each used.
function lets(k) {
  const body = [];
  for (let i = 1; i <= k; i++) body.push(`let a${i} = ${i};`);
  return `    function g() { ${body.join(' ')} return a${k} == ${k}; }`;
}

// k terms joined with &&: each term is the literal true.
const andChain = (k) => Array(k).fill('true').join(' && ');
// k terms joined with &&: each term is resource.data.n == 1 (a few expressions each).
const andFields = (k) => Array(k).fill('resource.data.n == 1').join(' && ');
// A list literal of k ints.
const listOf = (k) => `[${Array(k).fill('1').join(', ')}].size() == ${k}`;

// A function f(n) that calls itself n times.
const recurse = `    function r(n) { return n <= 0 ? true : r(n - 1); }`;
const mutual = `    function p(n) { return n <= 0 ? true : q(n - 1); }
    function q(n) { return n <= 0 ? true : p(n - 1); }`;

const docPath = (i) => `/databases/$(database)/documents/m/d${i}`;
function lookups(k, fn = 'exists') {
  const terms = [];
  const mocks = [];
  for (let i = 1; i <= k; i++) {
    terms.push(fn === 'exists' ? `exists(${docPath(i)})` : `get(${docPath(i)}).data.v == ${i}`);
    mocks.push(fn === 'exists' ? { fn: 'exists', path: `m/d${i}`, value: true } : { fn: 'get', path: `m/d${i}`, data: { v: i } });
  }
  return { cond: terms.join(' && '), mocks };
}

export const cases = [];

for (const n of [5, 9, 10, 11, 12, 19, 20, 21, 22, 30]) {
  cases.push(alone(`call_depth_${n}`, `a chain of ${n} nested function calls`, fsRules(chain(n), 'f1()')));
}
for (const n of [0, 1, 5, 8, 9, 10, 18, 19, 20, 25]) {
  cases.push(alone(`recursion_${n}`, `a function that calls itself ${n} times (r(${n}))`, fsRules(recurse, `r(${n})`)));
}
cases.push(alone('mutual_recursion_4', 'two functions calling each other, 4 calls', fsRules(mutual, 'p(4)')));
cases.push(alone('mutual_recursion_30', 'two functions calling each other, 30 calls', fsRules(mutual, 'p(30)')));
for (const k of [1, 5, 9, 10, 11, 12, 20, 21]) {
  cases.push(alone(`lets_${k}`, `a function with ${k} let bindings`, fsRules(lets(k), 'g()')));
}
for (const [np, k] of [[2, 9], [2, 10], [3, 8], [5, 10]]) {
  const params = Array.from({ length: np }, (_, i) => `p${i}`).join(', ');
  const body = [];
  for (let i = 1; i <= k; i++) body.push(`let a${i} = ${i};`);
  const fn = `    function g(${params}) { ${body.join(' ')} return a${k} == ${k}; }`;
  const args = Array.from({ length: np }, () => '0').join(', ');
  cases.push(alone(`lets_${k}_params_${np}`, `a function with ${np} parameters and ${k} let bindings`, fsRules(fn, `g(${args})`)));
}
cases.push(
  alone('let_rules_v1', "a function with a let binding under rules_version '1'", fsRules(lets(2), 'g()').replace("rules_version = '2';", "rules_version = '1';")),
  alone('let_no_version', 'a function with a let binding and no rules_version line', fsRules(lets(2), 'g()').replace("rules_version = '2';\n", '')),
);
cases.push(
  alone('let_outside_function', 'let at the top of an allow condition', fsRules('', 'true', 'lim').replace('allow get: if true;', 'let a = 1;\n      allow get: if a == 1;')),
);
for (const k of [2, 10, 20, 30, 40, 50, 60, 70, 80, 90, 92, 94, 96, 98, 100, 300, 1000]) {
  cases.push(alone(`and_true_${k}`, `${k} literal true terms joined with && (${2 * k - 1} nodes)`, fsRules('', andChain(k))));
}
for (const k of [10, 20, 30, 40, 100]) {
  cases.push(alone(`and_field_${k}`, `${k} terms resource.data.n == 1 joined with &&`, fsRules('', andFields(k))));
}
for (const k of [100, 500, 600, 700, 800, 900, 950, 990, 993, 994, 995, 996, 997, 998, 1000, 2000]) {
  cases.push(alone(`list_literal_${k}`, `a list literal of ${k} ints, then size()`, fsRules('', listOf(k))));
}
// A balanced tree of && with 2^d leaves: depth d, many nodes.
function balanced(d) {
  if (d === 0) return 'true';
  const sub = balanced(d - 1);
  return `(${sub} && ${sub})`;
}
for (const d of [3, 4, 5, 6, 7, 8, 9]) {
  cases.push(alone(`and_balanced_${d}`, `a balanced tree of && with ${2 ** d} literal true leaves (depth ${d})`, fsRules('', balanced(d))));
}
for (const n of [10, 50, 60, 70, 80, 90, 95, 96, 97, 98, 99, 100, 200]) {
  cases.push(alone(`parens_${n}`, `true inside ${n} nested parentheses`, fsRules('', '('.repeat(n) + 'true' + ')'.repeat(n))));
}
for (const n of [10, 20, 30, 35, 40, 45, 50]) {
  let t = 'true';
  for (let i = 0; i < n; i++) t = `(1 == 1 ? ${t} : false)`;
  cases.push(alone(`ternary_nested_${n}`, `${n} nested ternaries`, fsRules('', t)));
}
// The same small function called many times: does the count include the calls'
// bodies?
cases.push(alone('fn_calls_300', '300 calls of a one-comparison function', fsRules('    function t() { return 1 == 1; }', Array(300).fill('t()').join(' && '))));
cases.push(alone('fn_calls_400', '400 calls of a one-comparison function', fsRules('    function t() { return 1 == 1; }', Array(400).fill('t()').join(' && '))));
for (const total of [65536, 131072, 250000, 256000, 262143, 262144, 262145, 300000, 1048576]) {
  cases.push({ id: `size_comment_${total}`, desc: `a ${total}-byte ruleset, mostly one comment`, gen: { kind: 'comment-padding', total }, path: 'lim/doc' });
}
for (const n of [100, 500, 700, 900, 950, 1000, 2000]) {
  cases.push({ id: `size_functions_${n}`, desc: `${n} small functions`, gen: { kind: 'functions', count: n }, path: 'lim/doc' });
}
for (const k of [1, 9, 10, 11, 20, 21, 50, 101]) {
  const { cond, mocks } = lookups(k);
  cases.push(alone(`exists_${k}`, `${k} exists() calls on distinct documents, all mocked true`, fsRules('', cond), { mocks }));
}
for (const k of [10, 11]) {
  const { cond, mocks } = lookups(k, 'get');
  cases.push(alone(`get_${k}`, `${k} get() calls on distinct documents, all mocked`, fsRules('', cond), { mocks }));
}
{
  const same = Array(15).fill(`exists(${docPath(1)})`).join(' && ');
  cases.push(alone('exists_same_15', '15 exists() calls on the same document', fsRules('', same), { mocks: [{ fn: 'exists', path: 'm/d1', value: true }] }));
  const mix = [];
  for (let i = 1; i <= 6; i++) mix.push(`exists(${docPath(i)}) && get(${docPath(i)}).data.v == ${i}`);
  const mocks = [];
  for (let i = 1; i <= 6; i++) mocks.push({ fn: 'exists', path: `m/d${i}`, value: true }, { fn: 'get', path: `m/d${i}`, data: { v: i } });
  cases.push(alone('exists_get_same_6', 'exists() and get() on each of 6 documents (12 calls, 6 documents)', fsRules('', mix.join(' && ')), { mocks }));
  cases.push(alone('get_unmocked', 'get() of a document the test case does not mock', fsRules('', `get(${docPath(1)}) == null`)));
  cases.push(alone('exists_unmocked', 'exists() of a document the test case does not mock', fsRules('', `exists(${docPath(1)}) == false`)));
  cases.push(alone('get_unmocked_or_true', 'get() of an unmocked document, || true', fsRules('', `get(${docPath(1)}).data.v == 1 || true`)));
  cases.push(alone('get_mock_null', 'get() mocked to return no document', fsRules('', `get(${docPath(1)}) == null`), { mocks: [{ fn: 'get', path: 'm/d1', data: null }] }));
  cases.push(alone('get_mock_value_null', 'get() mocked with value null', fsRules('', `get(${docPath(1)}) == null`), { mocks: [{ fn: 'get', path: 'm/d1', data: 'VALUE_NULL' }] }));
  cases.push(alone('exists_mock_false', 'exists() mocked false', fsRules('', `!exists(${docPath(1)})`), { mocks: [{ fn: 'exists', path: 'm/d1', value: false }] }));
  cases.push(alone('get_string_path', 'get() of a string, not a path', fsRules('', `get('/databases/' + database + '/documents/m/d1').data.v == 1`), { mocks: [{ fn: 'get', path: 'm/d1', data: { v: 1 } }] }));
  cases.push(alone('get_short_circuit', 'false && get(): the lookup is skipped', fsRules('', `false && get(${docPath(1)}).data.v == 1`)));
}

// Cited by docs/research/rules/A-oracle-findings.md: kept in the development set.
const PIN = new Set([
  'call_depth_20',
  'call_depth_21',
  'parens_98',
  'parens_99',
  'let_rules_v1',
  'call_depth_22',
  'recursion_0',
  'mutual_recursion_4',
  'lets_11',
  'lets_12',
  'lets_10_params_5',
  'list_literal_996',
  'list_literal_997',
  'and_balanced_8',
  'and_balanced_9',
  'size_comment_256000',
  'size_comment_262143',
  'exists_21',
  'exists_50',
  'get_unmocked',
  'get_mock_value_null',
]);
for (const c of cases) if (PIN.has(c.id)) c.pin = true;
