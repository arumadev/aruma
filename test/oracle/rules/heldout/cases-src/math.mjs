// Numbers: int and float, the math namespace, conversions, hashing and bytes.
import { createHash } from 'node:crypto';

const DOC = 'DOC';

export const topic = 'math';
export const service = 'firestore';
export const resource = DOC;

const x = (id, expr, more = {}) => ({ id, desc: expr, expr, ...more });

const bytesLit = (buf) => "b'" + [...buf].map((b) => '\\x' + b.toString(16).padStart(2, '0')).join('') + "'";
const sha256 = createHash('sha256').update('abc').digest();
const md5 = createHash('md5').update('abc').digest();

export const cases = [
  x('int_plus_float', '1 + 1.5 == 2.5 && (1 + 1.5) is float'),
  x('int_div_is_int', '(3 / 2) is int && 3 / 2 == 1'),
  x('float_div_is_float', '(3.0 / 2) is float'),
  x('point_one_plus_point_two', '0.1 + 0.2 == 0.3'),
  x('float_of_int', 'float(2) == 2.0 && float(2) is float'),
  x('int_of_int', 'int(2) is int'),
  x('neg_zero', '-0.0 == 0.0'),
  x('float_mod', '5.5 % 2 == 1.5'),
  x('neg_mod', '-5 % 3 == -2'),
  x('unary_minus_field', '-resource.data.n == -1'),
  x('unary_minus_string', "-'a' == 'a'"),
  x('int_float_list_eq', '[1] == [1.0]'),
  x('int_float_map_eq', "{'a': 1} == {'a': 1.0}"),
  x('int_float_in_list', '1 in [1.0]'),
  x('nan_eq_nan', '(0.0 / 0.0) == (0.0 / 0.0)'),
  x('nan_ne_nan', '(0.0 / 0.0) != (0.0 / 0.0)'),
  x('abs_int', 'math.abs(-1) == 1 && math.abs(-1) is int'),
  x('abs_float', 'math.abs(-1.5) == 1.5'),
  x('abs_int_min', 'math.abs(-9223372036854775807 - 1) < 0'),
  x('ceil_float', 'math.ceil(1.2) == 2.0 && math.ceil(1.2) is float'),
  x('ceil_int', 'math.ceil(1) == 1.0 && math.ceil(1) is float'),
  x('floor_negative', 'math.floor(-1.2) == -2.0'),
  x('round_half_up', 'math.round(2.5) == 3 && math.round(2.5) is int'),
  x('round_neg_half', 'math.round(-2.5) == -2'),
  x('round_neg_half_away', 'math.round(-2.5) == -3'),
  x('round_nan', 'math.round(0.0 / 0.0) == 0'),
  x('round_inf', 'math.round(1.0 / 0.0) == 9223372036854775807'),
  x('sqrt', 'math.sqrt(4) == 2.0 && math.sqrt(4) is float'),
  x('sqrt_negative_nan', 'math.isNaN(math.sqrt(-1))'),
  x('pow', 'math.pow(2, 10) == 1024.0 && math.pow(2, 10) is float'),
  x('pow_fraction', 'math.pow(4, 0.5) == 2.0'),
  x('isnan_int', 'math.isNaN(1) == false'),
  x('isinfinite', 'math.isInfinite(1.0 / 0.0)'),
  x('abs_string', "math.abs('a') == 1"),
  x('abs_two_args', 'math.abs(1, 2) == 1'),
  x('trunc_absent', 'math.trunc(1.5) == 1.0'),
  x('max_absent', 'math.max(1, 2) == 2'),
  x('sha256_string', `hashing.sha256('abc') == ${bytesLit(sha256)}`),
  x('sha256_bytes', `hashing.sha256(b'abc') == ${bytesLit(sha256)}`),
  x('sha256_base64', `hashing.sha256('abc').toBase64() == '${sha256.toString('base64')}'`),
  x('md5_hex_upper', `hashing.md5('abc').toHexString() == '${md5.toString('hex').toUpperCase()}'`),
  x('md5_hex_lower', `hashing.md5('abc').toHexString() == '${md5.toString('hex')}'`),
  x('crc32', "hashing.crc32('abc') == b'\\x35\\x24\\x41\\xc2'"),
  x('crc32_is_bytes', "hashing.crc32('abc') is bytes"),
  x('crc32c', "hashing.crc32c('abc') == b'\\x36\\x4b\\x3f\\xb7'"),
  x('crc32c_is_bytes', "hashing.crc32c('abc') is bytes"),
  x('sha256_int', 'hashing.sha256(1) is bytes'),
  x('bytes_size', "b'abc'.size() == 3"),
  x('bytes_hex_escape', "b'\\x00\\xff'.size() == 2"),
  x('bytes_base64', "b'abc'.toBase64() == 'YWJj'"),
  x('bytes_base64_url', "b'\\xfb\\xff'.toBase64() == '-_8='"),
  x('bytes_base64_std', "b'\\xfb\\xff'.toBase64() == '+/8='"),
  x('bytes_compare', "b'a' < b'b'"),
  x('bytes_index', "b'abc'[0] == 97"),
  x('bytes_eq_string', "b'abc' == 'abc'"),
  x('latlng_value', 'latlng.value(10.0, 20.0).latitude() == 10.0'),
  x('latlng_distance', 'latlng.value(0.0, 0.0).distance(latlng.value(0.0, 1.0)) > 111000'),
  x('latlng_out_of_range', 'latlng.value(91.0, 0.0) is latlng'),
];

// Cited by docs/research/rules/A-oracle-findings.md: kept in the development set.
const PIN = new Set(['round_neg_half', 'round_neg_half_away', 'ceil_int', 'isinfinite', 'md5_hex_upper', 'md5_hex_lower', 'int_float_list_eq', 'nan_eq_nan', 'bytes_base64_url', 'bytes_base64_std']);
for (const c of cases) if (PIN.has(c.id)) c.pin = true;
