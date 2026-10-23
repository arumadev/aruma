// What the Rules Test API does with the JSON it is given: ints and floats,
// strings that look like timestamps, request.time, request.auth, the
// resource of a case without one, and the variables a match binds.
const DOC = 'DOC';
const ABSENT = 'ABSENT';

export const topic = 'encoding';
export const service = 'firestore';
export const resource = DOC;

const x = (id, expr, more = {}) => ({ id, desc: expr, expr, ...more });
const T = '2026-03-04T05:06:07.123456789Z';

export const cases = [
  x('json_int_is_int', 'resource.data.n is int && resource.data.n == 1'),
  x('json_float_is_float', 'resource.data.f is float'),
  x('json_2_0_is_float', 'resource.data.g is float', { resource: { g: 777001 }, raw: [[777001, '2.0']] }),
  x('json_2_0_is_int', 'resource.data.g is int', { resource: { g: 777002 }, raw: [[777002, '2.0']] }),
  x('json_1e2_is_int', 'resource.data.g is int && resource.data.g == 100', { resource: { g: 777003 }, raw: [[777003, '1e2']] }),
  x('json_int64_max', 'resource.data.g == 9223372036854775807', { resource: { g: 777004 }, raw: [[777004, '9223372036854775807']] }),
  x('json_2pow53_plus1', 'resource.data.g == 9007199254740993', { resource: { g: 777005 }, raw: [[777005, '9007199254740993']] }),
  x('json_2pow53_plus1_is_int', 'resource.data.g is int', { resource: { g: 777006 }, raw: [[777006, '9007199254740993']] }),
  x('json_huge_number', 'resource.data.g is float', { resource: { g: 777007 }, raw: [[777007, '1e300']] }),
  x('json_neg_zero', 'resource.data.g == 0 && resource.data.g is int', { resource: { g: 777008 }, raw: [[777008, '-0']] }),
  x('json_ts_string_is_string', 'resource.data.t is string', { resource: { t: '2020-01-02T03:04:05Z' } }),
  x('json_ts_string_is_timestamp', 'resource.data.t is timestamp', { resource: { t: '2020-01-02T03:04:05Z' } }),
  x('json_null_field', 'resource.data.nul == null'),
  x('json_nested_list_map', "resource.data.m.y == 'z' && resource.data.arr[2] == 3"),
  x('time_unset_is_timestamp', 'request.time is timestamp', { time: ABSENT }),
  x('time_unset_eq_null', 'request.time == null', { time: ABSENT }),
  x('time_default_is_timestamp', 'request.time is timestamp'),
  x('time_set', 'request.time == timestamp.date(2026, 3, 4) + duration.time(5, 6, 7, 123456789)', { time: T }),
  x('time_set_nanos', 'request.time.nanos() == 123456789', { time: T }),
  x('time_set_millis_precision', 'request.time.toMillis() == 1772600767123', { time: T }),
  x('time_offset', 'request.time == timestamp.date(2026, 3, 4)', { time: '2026-03-04T03:00:00+03:00' }),
  x('time_bad_format', 'true', { time: 'yesterday', alone: true }),
  x('time_no_fraction', 'request.time.nanos() == 0', { time: '2026-03-04T05:06:07Z' }),
  x('resource_absent_is_null', 'resource == null', { resource: ABSENT }),
  x('resource_absent_or_true', 'resource.data.n == 1 || true', { resource: ABSENT }),
  x('resource_null_is_null', 'resource == null', { resource: null }),
  x('resource_null_data', 'resource.data == null', { resource: null }),
  x('resource_null_data_or_true', 'resource.data.n == 1 || true', { resource: null }),
  x('resource_empty_map_data', 'resource.data.size() == 0', { resource: {} }),
  x('resource_id', "resource.id == 'doc'"),
  x('resource_name', 'resource.__name__ == /databases/(default)/documents/c_resource_name/doc'),
  x('resource_name_absent', 'resource.__name__ == null || true'),
  x('request_resource_absent_on_get', 'request.resource == null'),
  x('request_resource_null_on_get', 'request.resource == null', { data: null }),
  x('request_path_value', 'request.path == /databases/(default)/documents/c_request_path_value/doc'),
  x('request_path_is_path', 'request.path is path'),
  x('request_method_get', "request.method == 'get'"),
  x('database_var', "database == '(default)'"),
  x('doc_var', "doc == 'doc'"),
  x('auth_uid', "request.auth.uid == 'alice'"),
  x('auth_token_email', "request.auth.token.email == 'alice@example.com'"),
  x('auth_token_nested', "request.auth.token.firebase.sign_in_provider == 'password'"),
  x('auth_token_list', "request.auth.token.groups.hasAny(['g2'])"),
  x('auth_token_bool', 'request.auth.token.admin == true'),
  x('auth_token_missing_claim', 'request.auth.token.missing == true'),
  x('auth_token_missing_claim_get', "request.auth.token.get('missing', false) == false"),
  x('auth_absent', 'request.auth == null', { auth: ABSENT }),
  x('auth_null', 'request.auth == null', { auth: null }),
  x('auth_null_uid', "request.auth.uid == 'alice'", { auth: null }),
  x('auth_null_uid_or_true', "request.auth.uid == 'alice' || true", { auth: null }),
  x('auth_without_token', 'request.auth.token == null', { auth: { uid: 'bob' } }),
  x('auth_without_token_size', 'request.auth.token.size() == 0', { auth: { uid: 'bob' } }),
  x('auth_uid_int', 'request.auth.uid is int', { auth: { uid: 7 } }),
  x('request_keys', "request.keys().hasAll(['auth', 'method', 'path', 'time'])"),
  x('request_is_map', 'request is map && resource is map'),
  x('request_query_on_get', 'request.query == null'),
];

// Cited by docs/research/rules/A-oracle-findings.md: kept in the development set.
const PIN = new Set([
  'json_2_0_is_int',
  'json_2_0_is_float',
  'json_ts_string_is_timestamp',
  'resource_absent_is_null',
  'resource_null_is_null',
  'auth_absent',
  'auth_null',
  'time_unset_is_timestamp',
  'time_default_is_timestamp',
]);
for (const c of cases) if (PIN.has(c.id)) c.pin = true;
