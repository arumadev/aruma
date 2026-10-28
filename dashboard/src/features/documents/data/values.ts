// Firestore values as the data browser shows and edits them. Values arrive and leave in their REST
// form (api.Value); a draft is the editor's state for one. A value the editor did not change is
// sent back exactly as it came (an int64's 64 bits, a timestamp's nanoseconds, -0, bytes), and
// one it did change is parsed back from what was typed.
import type { Fields, Value } from '../api';

/** The types the editors offer, in the Firebase console's order. Bytes are shown, not created. */
export const FIELD_TYPES = ['string', 'int64', 'double', 'boolean', 'map', 'array', 'null', 'timestamp', 'geopoint', 'reference'] as const;
export type FieldType = (typeof FIELD_TYPES)[number];
export type ValueType = FieldType | 'bytes';

export function typeOf(v: Value): ValueType {
  if ('stringValue' in v) return 'string';
  if ('integerValue' in v) return 'int64';
  if ('doubleValue' in v) return 'double';
  if ('booleanValue' in v) return 'boolean';
  if ('mapValue' in v) return 'map';
  if ('arrayValue' in v) return 'array';
  if ('timestampValue' in v) return 'timestamp';
  if ('geoPointValue' in v) return 'geopoint';
  if ('referenceValue' in v) return 'reference';
  if ('bytesValue' in v) return 'bytes';
  return 'null';
}

/** A map's fields, in the order the console lists them: by name. */
export function sortedEntries(fields: Fields | undefined): [string, Value][] {
  return Object.entries(fields ?? {}).sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0));
}

export const mapFields = (v: Value): Fields => ('mapValue' in v ? (v.mapValue.fields ?? {}) : {});
export const arrayValues = (v: Value): Value[] => ('arrayValue' in v ? (v.arrayValue.values ?? []) : []);

/** A double as typed and shown: NaN and the infinities by name, -0 with its sign. */
export function doubleText(d: number | string): string {
  if (typeof d === 'string') return d;
  return Object.is(d, -0) ? '-0' : String(d);
}

/** The offset of a zone at an instant, as the Firebase console writes it: "UTC", "UTC+3", "UTC-5:30". */
export function utcOffset(date: Date, timeZone?: string): string {
  const part = new Intl.DateTimeFormat('en-US', { timeZone, timeZoneName: 'shortOffset' }).formatToParts(date).find((p) => p.type === 'timeZoneName');
  const offset = (part?.value ?? '').replace(/^(GMT|UTC)/, '');
  return offset === '' || /^[+-]0$/.test(offset) ? 'UTC' : `UTC${offset}`;
}

/**
 * A timestamp as the Firebase console shows it, "October 17, 2026 at 5:56:25 PM UTC+3", in the
 * reader's zone. Arabic reads "17 أكتوبر 2026 في 5:56:25 م UTC+3", with Western digits as the
 * rest of the dashboard writes them.
 */
export function formatTimestamp(iso: string, locale: string, timeZone?: string): string {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return iso;
  const tag = locale === 'ar' ? 'ar-u-nu-latn' : 'en-US';
  const day = new Intl.DateTimeFormat(tag, { timeZone, year: 'numeric', month: 'long', day: 'numeric' }).format(date);
  const time = new Intl.DateTimeFormat(tag, { timeZone, hour: 'numeric', minute: '2-digit', second: '2-digit', hour12: true }).format(date);
  return `${day} ${locale === 'ar' ? 'في' : 'at'} ${time} ${utcOffset(date, timeZone)}`;
}

/** A geopoint as the Firebase console shows it: "[24.5° N, 0° E]". */
export function formatGeoPoint(lat: number, lng: number): string {
  return `[${Math.abs(lat)}° ${lat < 0 ? 'S' : 'N'}, ${Math.abs(lng)}° ${lng < 0 ? 'W' : 'E'}]`;
}

/** The path a reference shows: "/users/alice" in this database, else its full name. */
export function referencePath(name: string, database: string): string {
  const root = `${database}/documents/`;
  return name.startsWith(root) ? `/${name.slice(root.length)}` : name;
}

/**
 * A scalar value's display text (a map's or an array's is its children): strings quoted, the
 * other types as the Firebase console writes them.
 */
export function formatValue(v: Value, opts: { locale: string; database: string; timeZone?: string }): string {
  if ('stringValue' in v) return `"${v.stringValue}"`;
  if ('integerValue' in v) return v.integerValue;
  if ('doubleValue' in v) return doubleText(v.doubleValue);
  if ('booleanValue' in v) return String(v.booleanValue);
  if ('timestampValue' in v) return formatTimestamp(v.timestampValue, opts.locale, opts.timeZone);
  if ('geoPointValue' in v) return formatGeoPoint(v.geoPointValue.latitude ?? 0, v.geoPointValue.longitude ?? 0);
  if ('referenceValue' in v) return referencePath(v.referenceValue, opts.database);
  if ('bytesValue' in v) return v.bytesValue;
  if ('mapValue' in v) return '{…}';
  if ('arrayValue' in v) return '[…]';
  return 'null';
}

// Drafts.

export interface DraftEntry {
  /** A stable React key. */
  key: string;
  /** The field's name in a map; unused in an array. */
  name: string;
  value: Draft;
}

export interface Draft {
  type: ValueType;
  /** string, int64, double, reference. */
  text: string;
  bool: boolean;
  /** timestamp, in the reader's zone: yyyy-mm-dd and HH:MM:SS. */
  date: string;
  time: string;
  lat: string;
  lng: string;
  /** map and array. */
  entries: DraftEntry[];
  /** The value as read, sent back as it was unless the draft was edited. */
  original?: Value;
}

let keys = 0;
export const newKey = () => `k${++keys}`;

const pad = (n: number, w = 2) => String(n).padStart(w, '0');

/** A local date and time of an instant, as date and time inputs take them. */
export function localParts(date: Date): { date: string; time: string } {
  return {
    date: `${pad(date.getFullYear(), 4)}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`,
    time: `${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`,
  };
}

/** An empty draft of a type, as a new field starts. A timestamp starts at now, a geopoint at 0, 0. */
export function emptyDraft(type: ValueType, now = new Date()): Draft {
  return { type, text: '', bool: true, ...localParts(now), lat: '0', lng: '0', entries: [] };
}

/** A draft of a value read from the server. */
export function draftOf(v: Value, database: string): Draft {
  const type = typeOf(v);
  const d = emptyDraft(type);
  d.original = v;
  if ('stringValue' in v) d.text = v.stringValue;
  else if ('integerValue' in v) d.text = v.integerValue;
  else if ('doubleValue' in v) d.text = doubleText(v.doubleValue);
  else if ('booleanValue' in v) d.bool = v.booleanValue;
  else if ('referenceValue' in v) d.text = referencePath(v.referenceValue, database).replace(/^\//, '');
  else if ('bytesValue' in v) d.text = v.bytesValue;
  else if ('geoPointValue' in v) {
    d.lat = String(v.geoPointValue.latitude ?? 0);
    d.lng = String(v.geoPointValue.longitude ?? 0);
  } else if ('timestampValue' in v) {
    const at = new Date(v.timestampValue);
    if (!Number.isNaN(at.getTime())) Object.assign(d, localParts(at));
  } else if ('mapValue' in v) {
    d.entries = sortedEntries(v.mapValue.fields).map(([name, child]) => ({ key: newKey(), name, value: draftOf(child, database) }));
  } else if ('arrayValue' in v) {
    d.entries = (v.arrayValue.values ?? []).map((child) => ({ key: newKey(), name: '', value: draftOf(child, database) }));
  }
  return d;
}

/** A draft after an edit: no longer the value as read. */
export const edited = (d: Draft, patch: Partial<Draft>): Draft => ({ ...d, ...patch, original: undefined });

/** What is wrong with a value as typed, by its key in documents.data.problem. */
export type ValueProblem = 'int64' | 'double' | 'latitude' | 'longitude' | 'timestamp' | 'reference' | 'required' | 'duplicate' | 'slash' | 'underscores' | 'dot' | 'long';

const INT64_MIN = -(2n ** 63n);
const INT64_MAX = 2n ** 63n - 1n;

/** An int64 as typed, normalized ("007" is "7"), or undefined. */
export function parseInt64(text: string): string | undefined {
  const s = text.trim();
  if (!/^[+-]?\d+$/.test(s)) return undefined;
  const n = BigInt(s);
  return n < INT64_MIN || n > INT64_MAX ? undefined : n.toString();
}

/** A double as typed: a number, or "NaN", "Infinity", "-Infinity" as the REST form writes them. */
export function parseDouble(text: string): number | string | undefined {
  const s = text.trim();
  if (s === 'NaN' || s === 'Infinity' || s === '-Infinity') return s;
  if (s === '+Infinity') return 'Infinity';
  if (!/^[+-]?(\d+\.?\d*|\.\d+)([eE][+-]?\d+)?$/.test(s)) return undefined;
  const n = Number(s);
  return Number.isFinite(n) ? n : undefined;
}

function parseCoordinate(text: string, bound: number): number | undefined {
  const n = parseDouble(text);
  return typeof n === 'number' && n >= -bound && n <= bound ? n : undefined;
}

/** A reference typed as a document path ("users/alice", a leading / allowed), or a full name. */
export function parseReference(text: string, database: string): string | undefined {
  const s = text.trim();
  if (s.startsWith('projects/')) {
    const m = /^projects\/[^/]+\/databases\/[^/]+\/documents\/(.+)$/.exec(s);
    return m?.[1] && isDocumentPath(m[1]) ? s : undefined;
  }
  const rel = s.replace(/^\/+/, '');
  return isDocumentPath(rel) ? `${database}/documents/${rel}` : undefined;
}

/** A path of ids, none empty, naming a document: an even number of them. */
export function isDocumentPath(rel: string): boolean {
  const ids = rel.split('/');
  return ids.length % 2 === 0 && ids.every((id) => id !== '');
}

/** The local date and time of a timestamp draft as an RFC 3339 instant, or undefined. */
export function parseLocalTimestamp(date: string, time: string): string | undefined {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(date) || !/^\d{2}:\d{2}(:\d{2})?$/.test(time)) return undefined;
  const at = new Date(`${date}T${time.length === 5 ? `${time}:00` : time}`);
  return Number.isNaN(at.getTime()) ? undefined : at.toISOString();
}

/** The first problem of a draft's own value (not its children's), or null. */
export function valueProblem(d: Draft, database: string): ValueProblem | null {
  if (d.original) return null;
  switch (d.type) {
    case 'int64':
      return parseInt64(d.text) === undefined ? 'int64' : null;
    case 'double':
      return parseDouble(d.text) === undefined ? 'double' : null;
    case 'geopoint':
      return parseCoordinate(d.lat, 90) === undefined ? 'latitude' : parseCoordinate(d.lng, 180) === undefined ? 'longitude' : null;
    case 'timestamp':
      return parseLocalTimestamp(d.date, d.time) === undefined ? 'timestamp' : null;
    case 'reference':
      return parseReference(d.text, database) === undefined ? 'reference' : null;
    default:
      return null;
  }
}

/** A field name's problem among its siblings: empty, or taken by an earlier one. */
export function nameProblem(name: string, earlier: readonly string[]): ValueProblem | null {
  if (name === '') return 'required';
  return earlier.includes(name) ? 'duplicate' : null;
}

/** Whether a draft and everything in it can be sent. */
export function isValid(d: Draft, database: string): boolean {
  if (valueProblem(d, database)) return false;
  if (d.type === 'map') {
    const names: string[] = [];
    for (const e of d.entries) {
      if (nameProblem(e.name, names)) return false;
      names.push(e.name);
    }
  }
  return (d.type === 'map' || d.type === 'array') && !d.original ? d.entries.every((e) => isValid(e.value, database)) : true;
}

/** A draft as a REST value. The draft must be valid. */
export function valueOf(d: Draft, database: string): Value {
  if (d.original) return d.original;
  switch (d.type) {
    case 'string':
      return { stringValue: d.text };
    case 'int64':
      return { integerValue: parseInt64(d.text) ?? '0' };
    case 'double':
      return { doubleValue: parseDouble(d.text) ?? 0 };
    case 'boolean':
      return { booleanValue: d.bool };
    case 'null':
      return { nullValue: null };
    case 'timestamp':
      return { timestampValue: parseLocalTimestamp(d.date, d.time) ?? new Date(0).toISOString() };
    case 'geopoint':
      return { geoPointValue: { latitude: parseCoordinate(d.lat, 90) ?? 0, longitude: parseCoordinate(d.lng, 180) ?? 0 } };
    case 'reference':
      return { referenceValue: parseReference(d.text, database) ?? '' };
    case 'bytes':
      return { bytesValue: d.text };
    case 'map':
      return { mapValue: { fields: Object.fromEntries(d.entries.map((e) => [e.name, valueOf(e.value, database)])) } };
    case 'array':
      return { arrayValue: { values: d.entries.map((e) => valueOf(e.value, database)) } };
  }
}

/** A draft changed to another type, keeping what it can (the text between text types). */
export function retyped(d: Draft, type: FieldType): Draft {
  if (d.type === type) return d;
  const next = emptyDraft(type);
  if (['string', 'int64', 'double', 'reference'].includes(type) && ['string', 'int64', 'double', 'reference'].includes(d.type)) next.text = d.text;
  return next;
}

// Ids and paths.

/** A document's or collection's id as typed: the Firebase console's checks, in its order. */
export function idProblem(id: string): ValueProblem | null {
  if (id === '') return 'required';
  if (id.includes('/')) return 'slash';
  if (id.startsWith('.')) return 'dot';
  if (id.length >= 4 && id.startsWith('__') && id.endsWith('__')) return 'underscores';
  if (new TextEncoder().encode(id).length > 1500) return 'long';
  return null;
}

const AUTO_ID_CHARS = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789';

/** A random 20-character id, as the SDKs and the Firebase console make them. */
export function autoId(): string {
  const out: string[] = [];
  while (out.length < 20) {
    const bytes = crypto.getRandomValues(new Uint8Array(40));
    for (const b of bytes) {
      // 248 = 62 × 4: drop the bytes above it, so every character is equally likely.
      if (b < 248 && out.length < 20) out.push(AUTO_ID_CHARS[b % 62]!);
    }
  }
  return out.join('');
}

/** A field path segment, always quoted, as the Firebase console's update masks write it: `name`. */
export function quoteSegment(name: string): string {
  return `\`${name.replace(/[\\`]/g, (c) => `\\${c}`)}\``;
}

/** A field path of map keys as an update mask entry: `a`.`b`. */
export const fieldPath = (segments: readonly string[]) => segments.map(quoteSegment).join('.');

/** A field path as the delete dialog shows it: a.b, the segments joined. */
export const displayPath = (segments: readonly string[]) => segments.join('.');

/** The fields an edit at a path sends: the value nested in maps along the path. */
export function nestedFields(segments: readonly string[], v: Value | undefined): Fields {
  const [first, ...rest] = segments;
  if (first === undefined) return {};
  if (rest.length === 0) return v === undefined ? {} : { [first]: v };
  return { [first]: { mapValue: { fields: nestedFields(rest, v) } } };
}

/** The value at a path of map keys in a document's fields, if there is one. */
export function valueAt(fields: Fields | undefined, segments: readonly string[]): Value | undefined {
  let cur: Value | undefined = { mapValue: { fields: fields ?? {} } };
  for (const s of segments) {
    if (!cur || !('mapValue' in cur)) return undefined;
    cur = cur.mapValue.fields?.[s];
  }
  return cur;
}

/** The ids of a path below the documents root ("" is the root). */
export const splitPath = (path: string): string[] => (path === '' ? [] : path.split('/'));

/** A path as typed in the edit-path box: slashes around it dropped, or undefined when an id is empty. */
export function normalizePath(text: string): string | undefined {
  const s = text.trim().replace(/^\/+|\/+$/g, '');
  if (s === '') return '';
  return s.split('/').every((id) => id !== '') ? s : undefined;
}

/** The three panels of a path: the parent ("" or a document), the collection, the document. */
export function panels(path: string): { parent: string; collection?: string; document?: string } {
  const ids = splitPath(path);
  if (ids.length === 0) return { parent: '' };
  if (ids.length % 2 === 1) return { parent: ids.slice(0, -1).join('/'), collection: path };
  return { parent: ids.slice(0, -2).join('/'), collection: ids.slice(0, -1).join('/'), document: path };
}
