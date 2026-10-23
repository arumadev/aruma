// Error semantics: how an evaluation error travels through ||, &&, !, the
// ternary, comparisons, functions and let; missing keys; type errors;
// division by zero; int overflow; non-bool operands.
const DOC = 'DOC';

export const topic = 'errors';
export const service = 'firestore';
export const resource = DOC;
export const fns = `    function errf() { return resource.data.missing == 1; }
    function lazyLet() { let x = resource.data.missing; return true; }
    function usedLet() { let x = resource.data.missing; return x == 1 || true; }
    function nonBool() { return 1; }
    function ignores(a) { return true; }
    function uses(a) { return a == 1 || true; }`;

const E = 'resource.data.missing == 1'; // Property missing is undefined on object.
const Z = '1 / 0 == 0'; // Divide by zero error.
const x = (id, expr, more = {}) => ({ id, desc: expr, expr, ...more });

export const cases = [
  x('missing_eq', E),
  x('missing_eq_null', 'resource.data.missing == null'),
  x('missing_ne_null', 'resource.data.missing != null'),
  x('null_field_eq_null', 'resource.data.nul == null'),
  x('err_or_true', `${E} || true`),
  x('true_or_err', `true || ${E}`),
  x('err_or_false', `${E} || false`),
  x('false_or_err', `false || ${E}`),
  x('err_and_false', `${E} && false`),
  x('false_and_err', `false && ${E}`),
  x('err_and_true', `${E} && true`),
  x('true_and_err', `true && ${E}`),
  x('err_or_err', `${E} || ${Z}`),
  x('not_err', `!(${E})`),
  x('not_err_or_true', `!(${E}) || true`),
  x('not_not_err', `!!(${E})`),
  x('paren_err_or_true_and', `(${E} || true) && (true || ${E})`),
  x('err_and_false_or_true', `(${E} && false) || true`),
  x('false_and_err_or_true', `(false && ${E}) || true`),
  x('ternary_err_cond', `(${E}) ? true : true`),
  x('ternary_true_skips_err', `true ? true : (${E})`),
  x('ternary_false_skips_err', `false ? (${E}) : true`),
  x('ternary_takes_err', `true ? (${E}) : true`),
  x('err_eq_err', `(${E}) == (${E})`),
  x('err_ne_true', `(${E}) != true`),
  x('err_eq_false', `(${E}) == false`),
  x('err_in_list', `(${E}) in [true, false]`),
  x('in_missing_list', '1 in resource.data.missing'),
  x('err_list_literal', `[${E}].size() == 1`),
  x('err_map_literal_value', `{'a': ${E}}.size() == 1`),
  x('fn_err', 'errf()'),
  x('fn_ignores_err_arg', `ignores(resource.data.missing)`),
  x('fn_uses_err_arg_or_true', `uses(resource.data.missing)`),
  x('fn_ignores_div_zero_arg', `ignores(1 / 0)`),
  x('horses_or_true', "'horses' || true"),
  x('true_or_horses', "true || 'horses'"),
  x('horses_and_false', "'horses' && false"),
  x('debug_or_true', 'debug(true) || true', { alone: true }),
  x('fn_err_or_true', 'errf() || true'),
  x('let_unused_err', 'lazyLet()'),
  x('let_used_err_or_true', 'usedLet()'),
  x('missing_key_in', "'missing' in resource.data"),
  x('present_key_in', "'n' in resource.data"),
  x('missing_get_default', "resource.data.get('missing', 7) == 7"),
  x('nested_get_default', "resource.data.get(['m', 'x'], 0) == 1"),
  x('nested_get_missing_default', "resource.data.get(['m', 'q'], 7) == 7"),
  x('nested_get_missing_parent', "resource.data.get(['missing', 'q'], 7) == 7"),
  x('nested_get_through_scalar', "resource.data.get(['n', 'q'], 7) == 7"),
  x('index_missing_key', "resource.data['missing'] == 1"),
  x('nested_missing_key', 'resource.data.m.q == 1'),
  x('null_member', 'resource.data.nul.x == 1'),
  x('scalar_member', 'resource.data.n.x == 1'),
  x('string_plus_int', "resource.data.s + 1 == 'abc1'"),
  x('string_lt_int', 'resource.data.s < 1'),
  x('int_eq_string', "resource.data.n == 'a'"),
  x('int_ne_string', "resource.data.n != 'a'"),
  x('int_eq_float', '1 == 1.0'),
  x('int_lt_float', '1 < 1.5'),
  x('list_lt_list', '[1] < [2]'),
  x('null_eq_null', 'null == null'),
  x('null_lt_int', 'null < 1'),
  x('bool_lt_bool', 'false < true'),
  x('string_cmp', "'a' < 'b' && 'B' < 'a'"),
  x('nonbool_int_or_true', '1 || true'),
  x('true_or_nonbool_int', 'true || 1'),
  x('nonbool_string_and_true', "'abc' && true"),
  x('false_and_nonbool', 'false && 1'),
  x('not_nonbool', '!1'),
  x('nonbool_condition_fn', 'nonBool()'),
  x('nonbool_condition_field', 'resource.data.n'),
  x('nonbool_condition_null', 'resource.data.nul'),
  x('ternary_nonbool_cond', '1 ? true : false'),
  x('div_zero', Z),
  x('div_zero_unspaced', '1/0 == 0', { alone: true }),
  x('mod_zero', '1 % 0 == 0'),
  x('div_zero_or_true', `${Z} || true`),
  x('float_div_zero_inf', '1.0 / 0.0 > 100000000000000000000.0'),
  x('float_div_int_zero', '1.0 / 0 > 100000000000000000000.0'),
  x('neg_float_div_zero', '-1.0 / 0.0 < -100000000000000000000.0'),
  x('float_exponent_literal', '1.0e3 == 1000.0', { alone: true }),
  x('int_exponent_literal', '1e3 == 1000.0', { alone: true }),
  x('zero_div_zero_nan', 'math.isNaN(0.0 / 0.0)'),
  x('float_mod_zero', 'math.isNaN(1.5 % 0.0)'),
  x('int_max_plus_one', '9223372036854775807 + 1 < 0'),
  x('int_max_plus_one_or_true', '(9223372036854775807 + 1 < 0) || true'),
  x('int_min_minus_one', '-9223372036854775807 - 2 > 0'),
  x('int_max_times_two', '9223372036854775807 * 2 < 0'),
  x('int_min_negate', '-(-9223372036854775807 - 1) < 0'),
  x('int_min_div_neg1', '(-9223372036854775807 - 1) / -1 < 0'),
  x('int_min_mod_neg1', '(-9223372036854775807 - 1) % -1 == 0'),
  x('int_max_literal', '9223372036854775807 > 0'),
  x('int_literal_overflow', '9223372036854775808 > 0', { alone: true }),
  x('int_min_literal', '-9223372036854775808 < 0'),
  x('int_of_huge_float', 'int(100000000000000000000.0) > 0'),
  x('int_of_inf', 'int(1.0 / 0.0) > 0'),
  x('int_of_nan', 'int(0.0 / 0.0) == 0'),
  x('int_div_trunc', '7 / 2 == 3 && -7 / 2 == -3'),
  x('int_mod_sign', '-7 % 2 == -1 && 7 % -2 == 1'),
  x('float_div', '7.0 / 2 == 3.5 && 7 / 2.0 == 3.5'),
  x('unknown_identifier', 'foo == 1', { alone: true }),
  x('unknown_request_field', 'request.foo == 1'),
  x('unknown_function', 'nosuch(1)', { alone: true }),
  x('wrong_arg_count', "'a'.size(1) == 1", { alone: true }),
  x('debug_function', 'debug(true)', { alone: true }),
  {
    id: 'err_stmt_then_true_stmt',
    desc: 'two statements on one match: the first errors, the second allows',
    rules: `rules_version = '2';
service cloud.firestore {
  match /databases/{database}/documents {
    match /two/{doc} {
      allow get: if resource.data.missing == 1;
      allow get: if true;
    }
  }
}
`,
    path: 'two/doc',
  },
  {
    id: 'err_match_then_true_match',
    desc: 'two match blocks for the same path: one errors, the other allows',
    rules: `rules_version = '2';
service cloud.firestore {
  match /databases/{database}/documents {
    match /two/{doc} {
      allow get: if resource.data.missing == 1;
    }
    match /{coll}/{doc} {
      allow read: if coll == 'two';
    }
  }
}
`,
    path: 'two/doc',
  },
];

// Cited by docs/research/rules/A-oracle-findings.md: kept in the development set.
const PIN = new Set([
  'err_or_true',
  'true_or_err',
  'err_or_false',
  'false_or_err',
  'err_and_false',
  'false_and_err',
  'err_and_true',
  'true_and_err',
  'not_err',
  'ternary_err_cond',
  'ternary_true_skips_err',
  'let_unused_err',
  'missing_key_in',
  'missing_get_default',
  'div_zero',
  'int_max_plus_one',
  'int_min_div_neg1',
  'nonbool_int_or_true',
  'horses_or_true',
  'fn_ignores_err_arg',
  'debug_function',
  'float_exponent_literal',
  'unknown_identifier',
  'div_zero_unspaced',
]);
for (const c of cases) if (PIN.has(c.id)) c.pin = true;
