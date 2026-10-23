// Cloud Storage rules: resource and request.resource fields, auth claims,
// methods, cross-service firestore.get()/exists(), and the language basics
// that should behave as in Firestore rules.
const OBJECT = 'OBJECT';

export const topic = 'storage';
export const service = 'storage';
export const resource = OBJECT;

const x = (id, expr, more = {}) => ({ id, desc: expr, expr, ...more });
const UPLOAD = { name: 'c_x/file.txt', size: 2048, contentType: 'image/png', metadata: { k: 'v', owner: 'alice' } };
const w = (id, expr, more = {}) => x(id, expr, { method: 'create', allow: 'create', resource: null, data: UPLOAD, ...more });
const DB = '/databases/(default)/documents';

export const cases = [
  x('resource_size', 'resource.size == 1024 && resource.size is int'),
  x('resource_content_type', "resource.contentType == 'text/plain'"),
  x('resource_metadata', "resource.metadata.owner == 'alice'"),
  x('resource_metadata_missing', "resource.metadata.missing == 'x'"),
  x('resource_metadata_get', "resource.metadata.get('missing', 'd') == 'd'"),
  x('resource_name', "resource.name == 'dir/file.txt'"),
  x('resource_bucket', "resource.bucket == 'demo-oracle.appspot.com'"),
  x('resource_time_created_is_ts', 'resource.timeCreated is timestamp'),
  x('resource_time_created_is_string', 'resource.timeCreated is string'),
  x('resource_updated_is_ts', 'resource.updated is timestamp'),
  x('resource_generation_int', 'resource.generation is int'),
  x('resource_md5', 'resource.md5Hash is string'),
  x('resource_missing_field', 'resource.missing == 1'),
  x('resource_size_string', 'resource.size == 1024', { resource: { size: '1024' } }),
  x('resource_size_string_is_int', 'resource.size is int', { resource: { size: '1024' } }),
  x('resource_time_created_int', 'resource.timeCreated < request.time', { resource: { timeCreated: 1700000000000 } }),
  x('resource_time_created_bad', 'resource.timeCreated is timestamp', { resource: { timeCreated: 'yesterday' } }),
  x('resource_custom_field', "resource.whatever == 'x'", { resource: { whatever: 'x' } }),
  x('resource_null_on_get', 'resource == null', { resource: null }),
  x('resource_absent_on_get', 'resource == null', { resource: 'ABSENT' }),
  x('request_resource_on_get', 'request.resource == null'),
  x('request_path', 'request.path == /b/demo-oracle.appspot.com/o/c_request_path/file.txt'),
  x('request_path_string', "request.path == '/b/demo-oracle.appspot.com/o/c_request_path_string/file.txt'"),
  x('request_method', "request.method == 'get'"),
  x('bucket_var', "bucket == 'demo-oracle.appspot.com'"),
  x('file_var', "file == 'file.txt'"),
  x('auth_uid', "request.auth.uid == 'alice'"),
  x('auth_claim_bool', 'request.auth.token.admin == true'),
  x('auth_claim_list', "request.auth.token.groups.hasAny(['g1'])"),
  x('auth_provider', "request.auth.token.firebase.sign_in_provider == 'password'"),
  x('auth_null', 'request.auth == null', { auth: null }),
  x('auth_absent', 'request.auth == null', { auth: 'ABSENT' }),
  x('auth_null_uid_or_true', "request.auth.uid == 'x' || true", { auth: null }),
  x('time_set', 'request.time == timestamp.date(2026, 1, 15) + duration.time(10, 20, 30, 123000000)'),
  w('create_size', 'request.resource.size < 5 * 1024 * 1024'),
  w('create_content_type', "request.resource.contentType.matches('image/.*')"),
  w('create_metadata', "request.resource.metadata.k == 'v'"),
  w('create_name', "request.resource.name == 'c_x/file.txt'"),
  w('create_name_absent', "request.resource.bucket == 'x'"),
  w('create_resource_null', 'resource == null'),
  w('create_metadata_owner', 'request.resource.metadata.owner == request.auth.uid'),
  w('create_size_float_cmp', 'request.resource.size < 2.5 * 1024'),
  w('create_keys', "request.resource.keys().hasAll(['size', 'contentType'])"),
  x('update_both', "request.resource.size > resource.size && request.resource.contentType == resource.contentType", {
    method: 'update',
    allow: 'update',
    data: { size: 4096, contentType: 'text/plain' },
  }),
  x('delete_request_resource', 'request.resource == null', { method: 'delete', allow: 'delete', data: null }),
  x('read_get', 'true', { allow: 'read' }),
  x('read_list', 'true', { allow: 'read', method: 'list', resource: null }),
  x('get_list', 'true', { allow: 'get', method: 'list', resource: null }),
  x('write_create', 'true', { allow: 'write', method: 'create', resource: null, data: UPLOAD }),
  x('write_update', 'true', { allow: 'write', method: 'update', data: UPLOAD }),
  x('write_delete', 'true', { allow: 'write', method: 'delete', data: null }),
  x('create_update', 'true', { allow: 'create', method: 'update', data: UPLOAD }),
  x('method_list', "request.method == 'list'", { allow: 'list', method: 'list', resource: null }),
  x('fs_get', `firestore.get(${DB}/users/$(request.auth.uid)).data.role == 'admin'`, {
    mocks: [{ fn: 'get', path: 'users/alice', data: { role: 'admin' } }],
  }),
  x('fs_exists', `firestore.exists(${DB}/users/$(request.auth.uid))`, { mocks: [{ fn: 'exists', path: 'users/alice', value: true }] }),
  x('fs_get_unmocked', `firestore.get(${DB}/users/alice) == null`),
  x('fs_get_id', `firestore.get(${DB}/users/alice).id == 'alice'`, { mocks: [{ fn: 'get', path: 'users/alice', data: { role: 'admin' } }] }),
  x('fs_get_name', `firestore.get(${DB}/users/alice).__name__ == ${DB}/users/alice`, {
    mocks: [{ fn: 'get', path: 'users/alice', data: { role: 'admin' } }],
  }),
  x('fs_database_var', `firestore.exists(/databases/$(database)/documents/users/alice)`, {
    alone: true,
    mocks: [{ fn: 'exists', path: 'users/alice', value: true }],
  }),
  x('fs_bare_get', `get(${DB}/users/alice).data.role == 'admin'`, { mocks: [{ fn: 'get', path: 'users/alice', data: { role: 'admin' } }] }),
  x('fs_lookups_2', `firestore.exists(${DB}/u/a) && firestore.exists(${DB}/u/b)`, {
    mocks: ['a', 'b'].map((d) => ({ fn: 'exists', path: `u/${d}`, value: true })),
  }),
  x('fs_lookups_3', `firestore.exists(${DB}/u/a) && firestore.exists(${DB}/u/b) && firestore.exists(${DB}/u/c)`, {
    mocks: ['a', 'b', 'c'].map((d) => ({ fn: 'exists', path: `u/${d}`, value: true })),
  }),
  x('fs_lookups_11', Array.from({ length: 11 }, (_, i) => `firestore.exists(${DB}/u/d${i})`).join(' && '), {
    mocks: Array.from({ length: 11 }, (_, i) => ({ fn: 'exists', path: `u/d${i}`, value: true })),
  }),
  x('err_or_true', 'resource.missing == 1 || true'),
  x('err_and_false', 'resource.missing == 1 && false'),
  x('not_err', '!(resource.missing == 1)'),
  x('horses_or_true', "'horses' || true"),
  x('string_plus_int', "file + resource.size == 'x'"),
  x('div_zero', '1 / 0 == 0'),
  x('int_overflow', '9223372036854775807 + 1 < 0'),
  x('matches', "file.matches('.*[.]txt')"),
  x('split', "resource.name.split('/') == ['dir', 'file.txt']"),
  x('size_emoji', "'😀'.size() == 2"),
  x('math_round', 'math.round(-2.5) == -2'),
  x('list_neg_zero', '[0.0] == [-0.0]'),
  x('set_in_float', '1 in [1.0].toSet()'),
  x('map_keys_order', "{'b': 1, 'a': 2}.keys() == ['a', 'b']"),
  x('debug', 'debug(true)', { alone: true }),
  x('hashing', "hashing.md5(b'abc').toBase64() == 'kAFQmDzST7DWlj99KOF/cg=='"),
];

// Cited by docs/research/rules/A-oracle-findings.md: kept in the development set.
const PIN = new Set([
  'resource_time_created_is_ts', 'resource_size_string', 'resource_null_on_get', 'resource_absent_on_get', 'auth_null', 'auth_absent',
  'request_path', 'create_name_absent', 'read_list', 'write_delete', 'fs_get_unmocked', 'fs_get_id', 'fs_lookups_3', 'fs_bare_get',
]);
for (const c of cases) if (PIN.has(c.id)) c.pin = true;
