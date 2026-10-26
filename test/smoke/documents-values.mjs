// The values production recorded (test/oracle/documents/corpus/values-order.json), for the Node
// smoke tests of the Document Database: every value is written and must read back as production
// stored it. Values are in the REST (proto3 JSON) form there; protoValue turns one into the object
// form google-gax takes on gRPC and REST alike, and canon prints either form canonically, so
// a response compares to the recording exactly (doubles by Object.is: -0 is not 0, NaN is NaN).
import { readFileSync } from 'node:fs';

export const recordedProject = 'chatapp-8ae21';

// loadValues returns [{id, sent, stored, error}] with the recorded project's references rewritten
// to project. stored is undefined, and error {status, message} set, where production refused.
export function loadValues(project) {
  const text = readFileSync(new URL('../oracle/documents/corpus/values-order.json', import.meta.url), 'utf8');
  const file = JSON.parse(local(text, project));
  const out = file.values.map((v) => ({
    id: v.id,
    sent: v.sent,
    stored: v.write.http === 200 ? v.stored : undefined,
    error: v.write.http === 200 ? undefined : v.write.error,
  }));
  if (out.length < 200) throw new Error(`values-order.json holds ${out.length} values`);
  return out;
}

export function local(s, project) {
  return s.replaceAll(`projects/${recordedProject}/`, `projects/${project}/`);
}

// protoValue converts a proto3 JSON Value into the object form of protobufjs (fromObject).
export function protoValue(v) {
  const [kind, x] = Object.entries(v)[0];
  switch (kind) {
    case 'nullValue': return { nullValue: 'NULL_VALUE' };
    case 'doubleValue': return { doubleValue: Number(x) };
    case 'timestampValue': return { timestampValue: parseTimestamp(x) };
    case 'bytesValue': return { bytesValue: Buffer.from(x, 'base64') };
    case 'arrayValue': return { arrayValue: { values: (x.values ?? []).map(protoValue) } };
    case 'mapValue': return { mapValue: { fields: protoFields(x.fields ?? {}) } };
    case 'geoPointValue': return { geoPointValue: { latitude: x.latitude ?? 0, longitude: x.longitude ?? 0 } };
    default: return { [kind]: x };
  }
}

export function protoFields(fields) {
  return Object.fromEntries(Object.entries(fields).map(([k, v]) => [k, protoValue(v)]));
}

export function parseTimestamp(s) {
  const m = /^(.*T\d\d:\d\d:\d\d)(?:\.(\d{1,9}))?Z$/.exec(s);
  if (!m) throw new Error(`timestamp ${s}`);
  return { seconds: String(Date.parse(`${m[1]}Z`) / 1000), nanos: Number((m[2] ?? '').padEnd(9, '0')) };
}

const kinds = ['nullValue', 'booleanValue', 'integerValue', 'doubleValue', 'timestampValue', 'stringValue', 'bytesValue',
  'referenceValue', 'geoPointValue', 'arrayValue', 'mapValue'];

// canon prints a Value of either form (proto3 JSON, or a decoded gax response) canonically.
export function canon(v) {
  if (v === undefined || v === null) return 'undefined';
  const kind = v.valueType ?? kinds.find((k) => v[k] !== undefined && v[k] !== null) ?? (v.nullValue === null ? 'nullValue' : undefined);
  const x = v[kind];
  switch (kind) {
    case 'nullValue': return 'null';
    case 'booleanValue': return `bool:${x}`;
    case 'integerValue': return `int:${String(x)}`;
    case 'doubleValue': return `double:${num(x)}`;
    case 'timestampValue': {
      const t = typeof x === 'string' ? parseTimestamp(x) : x;
      return `ts:${String(t.seconds ?? 0)}.${String(t.nanos ?? 0).padStart(9, '0')}`;
    }
    case 'stringValue': return `string:${JSON.stringify(x)}`;
    case 'bytesValue': return `bytes:${Buffer.isBuffer(x) || x instanceof Uint8Array ? Buffer.from(x).toString('base64') : x}`;
    case 'referenceValue': return `ref:${x}`;
    case 'geoPointValue': return `geo:${num(x.latitude ?? 0)},${num(x.longitude ?? 0)}`;
    case 'arrayValue': return `[${(x.values ?? []).map(canon).join(',')}]`;
    case 'mapValue': return canonFields(x.fields ?? {});
    default: return `?${JSON.stringify(v)}`;
  }
}

export function canonFields(fields) {
  return `{${Object.keys(fields).sort().map((k) => `${JSON.stringify(k)}:${canon(fields[k])}`).join(',')}}`;
}

function num(x) {
  const n = Number(x);
  return Object.is(n, -0) ? '-0' : String(n);
}
