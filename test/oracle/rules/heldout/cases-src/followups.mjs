// Follow-ups to the first recording: cases that pin down a behaviour the
// first round left ambiguous. All are cited by the findings, so all are
// pinned to the development set.
const DOC = 'DOC';

export const topic = 'followups';
export const service = 'firestore';
export const resource = DOC;

const x = (id, expr, more = {}) => ({ id, desc: expr, expr, pin: true, ...more });
const fs = (version, body) => `${version ? `rules_version = '${version}';\n` : ''}service cloud.firestore {
  match /databases/{database}/documents {
${body}
  }
}
`;
const r = (id, desc, rules, path, more = {}) => ({ id, desc, rules, path, pin: true, ...more });
const rest = (cond) => `    match /p/{rest=**} {
      allow get: if ${cond};
    }`;

export const cases = [
  x('crc32_little_endian', "hashing.crc32('abc') == b'\\xc2\\x41\\x24\\x35'"),
  x('crc32_big_endian', "hashing.crc32('abc') == b'\\x35\\x24\\x41\\xc2'"),
  x('crc32_hex', "hashing.crc32('abc').toHexString() == '352441C2'"),
  x('crc32c_hex', "hashing.crc32c('abc').toHexString() == '364B3FB7'"),
  x('lower_non_ascii_unchanged', "'É'.lower() == 'É'"),
  x('upper_non_ascii_unchanged', "'é'.upper() == 'é'"),
  x('lower_ascii_mixed', "'AbÉ'.lower() == 'abÉ'"),
  x('index_emoji_utf16', "'😀a'[2] == 'a'"),
  x('index_emoji_half', "'😀a'[0].size() == 1"),
  x('split_empty_sep_size_3', "'abc'.split('').size() == 3"),
  x('split_empty_sep_size_4', "'abc'.split('').size() == 4"),
  x('split_empty_sep_first', "'abc'.split('')[0] == 'a'"),
  x('split_only_separators', "',,'.split(',').size() == 0"),
  x('split_empty_sep_first_empty', "'abc'.split('')[0] == ''"),
  x('split_empty_sep_last', "'abc'.split('')[3] == 'c'"),
  x('split_only_separators_1', "',,'.split(',').size() == 1"),
  x('split_only_separators_3', "',,'.split(',').size() == 3"),
  x('split_trailing_two_empties', "'a,,'.split(',') == ['a']"),
  x('split_middle_empty_kept', "'a,,b'.split(',') == ['a', '', 'b']"),
  x('split_slash_name', "'dir/sub/file.txt'.split('/') == ['dir', 'sub', 'file.txt']"),
  x('base64_url_md5', "hashing.md5(b'abc').toBase64() == 'kAFQmDzST7DWlj99KOF_cg=='"),
  x('diff_added_direction', "{'a': 1, 'c': 4}.diff({'a': 1}).addedKeys() == ['c'].toSet()"),
  x('diff_removed_direction', "{'a': 1}.diff({'a': 1, 'c': 4}).removedKeys() == ['c'].toSet()"),
  x('int_of_inf_value', 'int(1.0 / 0.0) == 9223372036854775807'),
  x('int_of_huge_float_value', 'int(100000000000000000000.0) == 9223372036854775807'),
  x('int_of_nan_value', 'int(0.0 / 0.0) == 0'),
  x('int_min_div_neg1_value', '(-9223372036854775807 - 1) / -1 == -9223372036854775807 - 1'),
  x('list_eq_int_float_strict', '[1.0] == [1.0] && [1] == [1]'),
  x('map_eq_int_float', "{'a': 1} == {'a': 1.0}"),
  x('set_eq_int_float', '[1].toSet() == [1.0].toSet()'),
  x('neg_zero_in_list', '0.0 in [-0.0]'),
  x('allow_unknown_verb_word', 'true', { allow: 'fetch', alone: true }),
  r('v1_rest_is_path', 'v1: is {rest=**} a path?', fs('1', rest('rest is path')), 'p/a/b/c'),
  r('v1_rest_eq_path', 'v1: {rest=**} == /a/b/c', fs('1', rest('rest == /a/b/c')), 'p/a/b/c'),
  r('v1_rest_size', 'v1: the size of {rest=**}', fs('1', rest('rest.size() == 5')), 'p/a/b/c'),
  r('v1_rest_string', 'v1: string({rest=**})', fs('1', rest("string(rest) == 'a/b/c'")), 'p/a/b/c'),
  r('v1_rest_one_segment', 'v1: {rest=**} of a single segment', fs('1', rest("rest == 'a'")), 'p/a'),
  r('v1_rest_is_map', 'v1: rest is map', fs('1', rest('rest is map')), 'p/a/b/c'),
  r('v2_rest_on_one_segment', 'v2: {rest=**} of p/a is /a', fs('2', rest('rest == /a')), 'p/a'),
  r('nested_under_root_recursive', "v2: a nested match under a block whose own path ends in {rest=**}: match /{rest=**} { match /x/{d} } on x/d", fs('2', `    match /{rest=**} {
      match /x/{d} {
        allow get: if true;
      }
    }`), 'x/d'),
  r('nested_under_recursive_deep', "v2: a nested match under a block whose own path ends in {rest=**}: match /p/{rest=**} { match /x/{d} } on p/a/b/x/d", fs('2', `    match /p/{rest=**} {
      match /x/{d} {
        allow get: if true;
      }
    }`), 'p/a/b/x/d'),
  r('nested_under_recursive_var', "v2: the {rest=**} variable inside a match nested under a block whose own path ends in {rest=**}", fs('2', `    match /p/{rest=**} {
      match /x/{d} {
        allow get: if rest == /a;
      }
    }`), 'p/a/x/d'),
];

// Boundaries the limits topic left between two sizes.
for (const total of [262000, 262100, 262110, 262117, 262118, 262119, 262120, 262128, 262129, 262140]) {
  cases.push({ id: `size_comment_${total}`, desc: `a ${total}-byte ruleset, mostly one comment`, gen: { kind: 'comment-padding', total }, path: 'lim/doc', pin: true });
}
const fsRules = (cond) => `rules_version = '2';
service cloud.firestore {
  match /databases/{database}/documents {
    match /lim/{doc} {
      allow get: if ${cond};
    }
  }
}
`;
cases.push(r('and_true_99', '99 literal true terms joined with &&', fsRules(Array(99).fill('true').join(' && ')), 'lim/doc'));
for (const n of [46, 47, 48, 49]) {
  let t = 'true';
  for (let i = 0; i < n; i++) t = `(1 == 1 ? ${t} : false)`;
  cases.push(r(`ternary_nested_${n}`, `${n} nested ternaries`, fsRules(t), 'lim/doc'));
}
