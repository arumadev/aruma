// Firestore paths: the path type, $(var) interpolation, indexing, match
// variables, recursive wildcards under rules_version 1 and 2, overlapping
// matches.
const DOC = 'DOC';

export const topic = 'paths';
export const service = 'firestore';
export const resource = DOC;

const x = (id, expr, more = {}) => ({ id, desc: expr, expr, ...more });
const fs = (version, body) => `${version ? `rules_version = '${version}';\n` : ''}service cloud.firestore {
  match /databases/{database}/documents {
${body}
  }
}
`;
const r = (id, desc, rules, path, more = {}) => ({ id, desc, rules, path, ...more });

export const cases = [
  x('literal_eq', '/a/b == /a/b'),
  x('literal_ne', '/a/b != /a/c'),
  x('interpolate', "/databases/$(database)/documents/c_interpolate/$(doc) == request.path"),
  x('interpolate_slash', "/a/$('b/c')/d == /a/b/c/d"),
  x('interpolate_slash_size', "/a/$('b/c')/d[1] == 'b/c'"),
  x('interpolate_int', '/a/$(1) == /a/1'),
  x('interpolate_whole', "/$('a/b') == /a/b"),
  x('path_fn', "path('/a/b') == /a/b"),
  x('path_fn_relative', "path('a/b') == /a/b"),
  x('path_index', "/a/b/c[1] == 'b'"),
  x('path_index_out_of_range', "/a/b[5] == 'x'"),
  x('path_eq_string', "/a/b == '/a/b'"),
  x('path_string', "string(/a/b) == '/a/b'"),
  x('path_concat', "/a/b + '/c' == '/a/b/c'"),
  x('path_size', '/a/b/c.size() == 3'),
  x('request_path_index', "request.path[3] == 'c_request_path_index'"),
  x('request_path_tail', "request.path[4] == 'doc'"),
  x('path_bind', "/a/$(x)/c.bind({'x': 'b'}) == /a/b/c"),
  x('path_is_path', '/a/b is path'),
  x('path_in', "'b' in /a/b"),
  x('path_special_chars', "/a/$('b c')/d[1] == 'b c'"),
  x('path_encoded_segment', "/a/b%20c[1] == 'b c'"),
  x('path_dot_segment', "/a/b.c[1] == 'b.c'"),
  x('path_dash_segment', "/a/b-c[1] == 'b-c'"),
  x('path_parens_segment', "/databases/(default)/documents[1] == '(default)'"),
  r('wild_v2_rest_is_path', 'v2: a {rest=**} variable is a path', fs('2', `    match /p/{rest=**} {
      allow get: if rest is path;
    }`), 'p/a/b/c'),
  r('wild_v2_rest_value', 'v2: the {rest=**} variable of p/a/b/c', fs('2', `    match /p/{rest=**} {
      allow get: if rest == /a/b/c;
    }`), 'p/a/b/c'),
  r('wild_v1_rest_is_string', 'v1: a {rest=**} variable is a string', fs('1', `    match /p/{rest=**} {
      allow get: if rest is string;
    }`), 'p/a/b/c'),
  r('wild_v1_rest_value', 'v1: the {rest=**} variable of p/a/b/c', fs('1', `    match /p/{rest=**} {
      allow get: if rest == 'a/b/c';
    }`), 'p/a/b/c'),
  r('wild_noversion_rest_is_string', 'no rules_version: a {rest=**} variable is a string', fs(null, `    match /p/{rest=**} {
      allow get: if rest is string;
    }`), 'p/a/b/c'),
  r('wild_v1_not_final', 'v1: {rest=**} followed by more segments', fs('1', `    match /{rest=**}/docs/{d} {
      allow get: if true;
    }`), 'a/b/docs/x'),
  r('wild_v2_not_final', 'v2: {rest=**} followed by more segments (collection group style)', fs('2', `    match /{rest=**}/docs/{d} {
      allow get: if true;
    }`), 'a/b/docs/x'),
  r('wild_v2_not_final_zero', 'v2: /{rest=**}/docs/{d} on a top-level docs/x', fs('2', `    match /{rest=**}/docs/{d} {
      allow get: if rest == /;
    }`), 'docs/x'),
  r('wild_v2_two_recursive', 'v2: two recursive wildcards in one match', fs('2', `    match /{a=**}/x/{b=**} {
      allow get: if true;
    }`), 'q/x/y/z'),
  r('wild_v2_nested_under_recursive', "v2: a nested match under a block whose own path ends in {rest=**} (match /p/{rest=**} { match /docs/{d} })", fs('2', `    match /p/{rest=**} {
      match /docs/{d} {
        allow get: if true;
      }
    }`), 'p/a/docs/x'),
  r('wild_v1_nested_under_recursive', 'v1: a nested match under {rest=**}', fs('1', `    match /p/{rest=**} {
      match /docs/{d} {
        allow get: if true;
      }
    }`), 'p/a/docs/x'),
  r('wild_v2_zero_segments_doc', 'v2: match /c/{d}/{rest=**} on the document c/d itself', fs('2', `    match /c/{d}/{rest=**} {
      allow get: if true;
    }`), 'c/d'),
  r('wild_v1_zero_segments_doc', 'v1: match /c/{d}/{rest=**} on the document c/d itself', fs('1', `    match /c/{d}/{rest=**} {
      allow get: if true;
    }`), 'c/d'),
  r('single_wildcard_one_segment', 'match /c/{d} does not match a subcollection document', fs('2', `    match /c/{d} {
      allow get: if true;
    }`), 'c/d/sub/e'),
  r('overlap_or', 'two matches on the same document: one false, one true', fs('2', `    match /c/{d} {
      allow get: if false;
    }
    match /{coll}/{d} {
      allow get: if coll == 'c';
    }`), 'c/d'),
  r('nested_parent_rule_not_inherited', 'a rule on /c/{d} does not cover /c/{d}/sub/{e}', fs('2', `    match /c/{d} {
      allow read: if true;
      match /sub/{e} {
        allow read: if false;
      }
    }`), 'c/d/sub/e'),
  r('nested_variables', 'a nested match sees its parent variables', fs('2', `    match /c/{d} {
      match /sub/{e} {
        allow get: if d == 'd' && e == 'e';
      }
    }`), 'c/d/sub/e'),
  r('var_shadowing', 'the same variable name in a nested match', fs('2', `    match /c/{d} {
      match /sub/{d} {
        allow get: if d == 'e';
      }
    }`), 'c/d/sub/e', { alone: true }),
  r('function_sees_match_var', 'a function declared in a match block reads its variables', fs('2', `    match /c/{d} {
      function isD() { return d == 'd'; }
      allow get: if isD();
    }`), 'c/d'),
  r('function_outer_var_not_visible', 'a top-level function reads a variable of a match it is not in', fs('2', `    function isD() { return d == 'd'; }
    match /c/{d} {
      allow get: if isD();
    }`), 'c/d'),
  r('partial_segment_wildcard', 'a wildcard inside a segment', fs('2', `    match /c/pre{d} {
      allow get: if true;
    }`), 'c/prex'),
  r('version_3', "rules_version = '3'", fs('3', `    match /c/{d} {
      allow get: if true;
    }`), 'c/d'),
];

// Cited by docs/research/rules/A-oracle-findings.md: kept in the development set.
const PIN = new Set([
  'interpolate_slash', 'wild_v2_rest_is_path', 'wild_v1_rest_is_string', 'wild_v1_not_final', 'wild_v2_not_final',
  'wild_v2_nested_under_recursive', 'wild_v2_zero_segments_doc', 'wild_v1_zero_segments_doc', 'nested_parent_rule_not_inherited',
  'function_outer_var_not_visible',
]);
for (const c of cases) if (PIN.has(c.id)) c.pin = true;
