// Cloud Storage match paths: {name=**} under rules_version 1 and 2, zero
// segments, a wildcard that does not cross a slash, nested matches, overlap.
const OBJECT = 'OBJECT';

export const topic = 'storage-paths';
export const service = 'storage';
export const resource = OBJECT;

const st = (version, body) => `${version ? `rules_version = '${version}';\n` : ''}service firebase.storage {
  match /b/{bucket}/o {
${body}
  }
}
`;
const r = (id, desc, rules, path, more = {}) => ({ id, desc, rules, path, ...more });
const rest = (cond) => `    match /p/{rest=**} {
      allow get: if ${cond};
    }`;

export const cases = [
  r('v2_rest_is_path', 'v2: {rest=**} is a path', st('2', rest('rest is path')), 'p/a/b.txt'),
  r('v2_rest_value', 'v2: {rest=**} of p/a/b.txt', st('2', rest('rest == /a/b.txt')), 'p/a/b.txt'),
  r('v2_rest_string', 'v2: string({rest=**})', st('2', rest("string(rest) == '/a/b.txt'")), 'p/a/b.txt'),
  r('v1_rest_is_string', 'v1: {rest=**} is a string', st('1', rest('rest is string')), 'p/a/b.txt'),
  r('v1_rest_value', 'v1: {rest=**} of p/a/b.txt', st('1', rest("rest == 'a/b.txt'")), 'p/a/b.txt'),
  r('noversion_rest_is_string', 'no rules_version: {rest=**} is a string', st(null, rest('rest is string')), 'p/a/b.txt'),
  r('v2_zero_segments', 'v2: match /p/{rest=**} on the object p', st('2', rest('true')), 'p'),
  r('v1_zero_segments', 'v1: match /p/{rest=**} on the object p', st('1', rest('true')), 'p'),
  r('v2_zero_segments_value', 'v2: {rest=**} when it matches no segment', st('2', rest('rest == /')), 'p'),
  r('v2_all_paths', 'v2: match /{allPaths=**}', st('2', `    match /{allPaths=**} {
      allow get: if true;
    }`), 'a/b/c.txt'),
  r('v1_all_paths', 'v1: match /{allPaths=**}', st('1', `    match /{allPaths=**} {
      allow get: if true;
    }`), 'a/b/c.txt'),
  r('v1_not_final', 'v1: {rest=**} followed by more segments', st('1', `    match /{rest=**}/x/{f} {
      allow get: if true;
    }`), 'a/x/f.txt', { pin: true }),
  r('v2_not_final', 'v2: {rest=**} followed by more segments', st('2', `    match /{rest=**}/x/{f} {
      allow get: if true;
    }`), 'a/x/f.txt'),
  r('v2_nested_under_recursive', "v2: a nested match under a block whose own path ends in {rest=**} (match /p/{rest=**} { match /x/{f} })", st('2', `    match /p/{rest=**} {
      match /x/{f} {
        allow get: if true;
      }
    }`), 'p/a/x/f.txt'),
  r('v1_nested_under_recursive', 'v1: a nested match under {rest=**}', st('1', `    match /p/{rest=**} {
      match /x/{f} {
        allow get: if true;
      }
    }`), 'p/a/x/f.txt'),
  r('single_no_slash', 'match /p/{f} does not match p/a/b.txt', st('2', `    match /p/{f} {
      allow get: if true;
    }`), 'p/a/b.txt'),
  r('single_with_dot', 'match /p/{f} on p/a.b.c', st('2', `    match /p/{f} {
      allow get: if f == 'a.b.c';
    }`), 'p/a.b.c'),
  r('space_in_name', 'an object name with a space', st('2', `    match /p/{f} {
      allow get: if f == 'a b.txt';
    }`), 'p/a b.txt'),
  r('encoded_name', 'an object name given percent-encoded', st('2', `    match /p/{f} {
      allow get: if f == 'a b.txt';
    }`), 'p/a%20b.txt'),
  r('overlap_or', 'two matches: one false, one true', st('2', `    match /p/{f} {
      allow get: if false;
    }
    match /{d}/{f} {
      allow get: if d == 'p';
    }`), 'p/a.txt'),
  r('bucket_literal', 'match a bucket by name', `rules_version = '2';
service firebase.storage {
  match /b/demo-oracle.appspot.com/o {
    match /p/{f} {
      allow get: if true;
    }
  }
}
`, 'p/a.txt'),
  r('bucket_other', 'a rule for another bucket', `rules_version = '2';
service firebase.storage {
  match /b/other-bucket/o {
    match /p/{f} {
      allow get: if true;
    }
  }
}
`, 'p/a.txt'),
  r('let_v1', "a let binding in a function under rules_version '1'", st('1', `    function g() { let a = 1; return a == 1; }
    match /p/{f} {
      allow get: if g();
    }`), 'p/a.txt'),
  r('function_v1', "a function under rules_version '1'", st('1', `    function g() { return true; }
    match /p/{f} {
      allow get: if g();
    }`), 'p/a.txt'),
  r('list_on_prefix', 'list with a path that ends in a slash', st('2', `    match /p/{rest=**} {
      allow list: if true;
    }`), 'p/', { method: 'list', resource: null }),
  r('list_on_folder', 'list on the folder p/a', st('2', `    match /p/{f} {
      allow list: if f == 'a';
    }`), 'p/a', { method: 'list', resource: null }),
];

// Cited by docs/research/rules/A-oracle-findings.md: kept in the development set.
const PIN = new Set(['v2_rest_is_path', 'v1_rest_is_string', 'v2_zero_segments', 'v1_zero_segments', 'v2_not_final', 'v2_nested_under_recursive', 'let_v1']);
for (const c of cases) if (PIN.has(c.id)) c.pin = true;
