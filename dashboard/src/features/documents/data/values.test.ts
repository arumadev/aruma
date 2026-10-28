import { describe, expect, it } from 'vitest';
import type { Value } from '../api';
import {
  autoId,
  draftOf,
  edited,
  emptyDraft,
  fieldPath,
  formatGeoPoint,
  formatTimestamp,
  formatValue,
  idProblem,
  isValid,
  nameProblem,
  nestedFields,
  normalizePath,
  panels,
  parseDouble,
  parseInt64,
  parseLocalTimestamp,
  parseReference,
  retyped,
  typeOf,
  valueAt,
  valueOf,
  valueProblem,
} from './values';

const DB = 'projects/p/databases/(default)';
const opts = { locale: 'en', database: DB, timeZone: 'UTC' };

describe('display', () => {
  it('writes each type as the Firebase console does', () => {
    expect(formatValue({ stringValue: 'hi' }, opts)).toBe('"hi"');
    expect(formatValue({ integerValue: '9223372036854775807' }, opts)).toBe('9223372036854775807');
    expect(formatValue({ doubleValue: 1.5 }, opts)).toBe('1.5');
    expect(formatValue({ doubleValue: 'NaN' }, opts)).toBe('NaN');
    expect(formatValue({ doubleValue: '-Infinity' }, opts)).toBe('-Infinity');
    expect(formatValue({ doubleValue: -0 }, opts)).toBe('-0');
    expect(formatValue({ booleanValue: false }, opts)).toBe('false');
    expect(formatValue({ nullValue: null }, opts)).toBe('null');
    expect(formatValue({ referenceValue: `${DB}/documents/users/u1` }, opts)).toBe('/users/u1');
    expect(formatValue({ referenceValue: 'projects/q/databases/(default)/documents/a/b' }, opts)).toBe('projects/q/databases/(default)/documents/a/b');
    expect(formatValue({ bytesValue: 'AAE=' }, opts)).toBe('AAE=');
  });

  it('writes a timestamp with its zone offset', () => {
    expect(formatTimestamp('2026-10-17T14:56:25.123456789Z', 'en', 'Asia/Riyadh')).toBe('October 17, 2026 at 5:56:25 PM UTC+3');
    expect(formatTimestamp('2026-10-17T14:56:25Z', 'en', 'UTC')).toBe('October 17, 2026 at 2:56:25 PM UTC');
    expect(formatTimestamp('2026-10-17T14:56:25Z', 'en', 'Asia/Kolkata')).toBe('October 17, 2026 at 8:26:25 PM UTC+5:30');
    expect(formatTimestamp('2026-10-17T14:56:25Z', 'ar', 'Asia/Riyadh')).toMatch(/^17 أكتوبر 2026 في 5:56:25\s?م UTC\+3$/);
  });

  it('writes a geopoint with its hemispheres', () => {
    expect(formatGeoPoint(24.5, 0)).toBe('[24.5° N, 0° E]');
    expect(formatGeoPoint(-33.86, -151.2)).toBe('[33.86° S, 151.2° W]');
  });

  it('names every type', () => {
    const samples: [Value, string][] = [
      [{ stringValue: '' }, 'string'],
      [{ integerValue: '1' }, 'int64'],
      [{ doubleValue: 1 }, 'double'],
      [{ booleanValue: true }, 'boolean'],
      [{ mapValue: {} }, 'map'],
      [{ arrayValue: {} }, 'array'],
      [{ nullValue: null }, 'null'],
      [{ timestampValue: '2026-01-01T00:00:00Z' }, 'timestamp'],
      [{ geoPointValue: {} }, 'geopoint'],
      [{ referenceValue: `${DB}/documents/a/b` }, 'reference'],
      [{ bytesValue: '' }, 'bytes'],
    ];
    for (const [v, type] of samples) expect(typeOf(v)).toBe(type);
  });
});

describe('parsing', () => {
  it('keeps every bit of an int64 and refuses what is out of range', () => {
    expect(parseInt64('9223372036854775807')).toBe('9223372036854775807');
    expect(parseInt64('-9223372036854775808')).toBe('-9223372036854775808');
    expect(parseInt64('9223372036854775808')).toBeUndefined();
    expect(parseInt64('007')).toBe('7');
    expect(parseInt64('1.5')).toBeUndefined();
    expect(parseInt64('')).toBeUndefined();
  });

  it('reads doubles, NaN and the infinities', () => {
    expect(parseDouble('3.25')).toBe(3.25);
    expect(parseDouble('-1e-7')).toBe(-1e-7);
    expect(parseDouble('NaN')).toBe('NaN');
    expect(parseDouble('Infinity')).toBe('Infinity');
    expect(parseDouble('-Infinity')).toBe('-Infinity');
    expect(parseDouble('1e999')).toBeUndefined();
    expect(parseDouble('abc')).toBeUndefined();
    expect(parseDouble('')).toBeUndefined();
  });

  it('reads references as document paths', () => {
    expect(parseReference('users/u1', DB)).toBe(`${DB}/documents/users/u1`);
    expect(parseReference('/users/u1', DB)).toBe(`${DB}/documents/users/u1`);
    expect(parseReference('users', DB)).toBeUndefined();
    expect(parseReference('users//u1', DB)).toBeUndefined();
    expect(parseReference('projects/q/databases/(default)/documents/a/b', DB)).toBe('projects/q/databases/(default)/documents/a/b');
  });

  it('reads a local date and time', () => {
    const iso = parseLocalTimestamp('2026-10-17', '17:56:25');
    expect(iso).toBe(new Date(2026, 9, 17, 17, 56, 25).toISOString());
    expect(parseLocalTimestamp('2026-10-17', '')).toBeUndefined();
  });

  it('checks geopoint ranges with the console messages', () => {
    expect(valueProblem({ ...emptyDraft('geopoint'), lat: '91' }, DB)).toBe('latitude');
    expect(valueProblem({ ...emptyDraft('geopoint'), lng: '-181' }, DB)).toBe('longitude');
    expect(valueProblem({ ...emptyDraft('geopoint'), lat: '-90', lng: '180' }, DB)).toBeNull();
  });
});

describe('ids', () => {
  it('gives the Firebase console messages', () => {
    expect(idProblem('')).toBe('required');
    expect(idProblem('a/b')).toBe('slash');
    expect(idProblem('__a__')).toBe('underscores');
    expect(idProblem('..')).toBe('dot');
    expect(idProblem('.a')).toBe('dot');
    expect(idProblem('__a')).toBeNull();
    expect(idProblem('مرحبا 🌍')).toBeNull();
    expect(idProblem('x'.repeat(1501))).toBe('long');
  });

  it('makes 20-character ids', () => {
    const ids = new Set(Array.from({ length: 50 }, autoId));
    expect(ids.size).toBe(50);
    for (const id of ids) expect(id).toMatch(/^[A-Za-z0-9]{20}$/);
  });

  it('checks field names among their siblings', () => {
    expect(nameProblem('', [])).toBe('required');
    expect(nameProblem('a', ['a'])).toBe('duplicate');
    expect(nameProblem('a', ['b'])).toBeNull();
  });
});

describe('paths', () => {
  it('quotes every field path segment as the console masks do', () => {
    expect(fieldPath(['name'])).toBe('`name`');
    expect(fieldPath(['a b', 'c`d\\e'])).toBe('`a b`.`c\\`d\\\\e`');
  });

  it('nests an edit along its path', () => {
    expect(nestedFields(['a', 'b'], { integerValue: '1' })).toEqual({ a: { mapValue: { fields: { b: { integerValue: '1' } } } } });
    expect(nestedFields(['a'], undefined)).toEqual({});
    expect(valueAt({ a: { mapValue: { fields: { b: { booleanValue: true } } } } }, ['a', 'b'])).toEqual({ booleanValue: true });
    expect(valueAt({ a: { stringValue: 'x' } }, ['a', 'b'])).toBeUndefined();
  });

  it('lays a path out in three panels', () => {
    expect(panels('')).toEqual({ parent: '' });
    expect(panels('users')).toEqual({ parent: '', collection: 'users' });
    expect(panels('users/u1')).toEqual({ parent: '', collection: 'users', document: 'users/u1' });
    expect(panels('users/u1/posts')).toEqual({ parent: 'users/u1', collection: 'users/u1/posts' });
    expect(panels('users/u1/posts/p1')).toEqual({ parent: 'users/u1', collection: 'users/u1/posts', document: 'users/u1/posts/p1' });
  });

  it('normalizes a typed path', () => {
    expect(normalizePath(' /users/u1/ ')).toBe('users/u1');
    expect(normalizePath('/')).toBe('');
    expect(normalizePath('users//u1')).toBeUndefined();
  });
});

describe('drafts', () => {
  const doc: Value = {
    mapValue: {
      fields: {
        big: { integerValue: '-9223372036854775808' },
        nan: { doubleValue: 'NaN' },
        inf: { doubleValue: 'Infinity' },
        negZero: { doubleValue: -0 },
        ts: { timestampValue: '2026-10-17T14:56:25.123456789Z' },
        raw: { bytesValue: 'AAECAw==' },
        ref: { referenceValue: `${DB}/documents/users/u1` },
        geo: { geoPointValue: { latitude: 24.5, longitude: 0 } },
        list: { arrayValue: { values: [{ nullValue: null }, { stringValue: 'مرحبا 👋' }] } },
        nested: { mapValue: { fields: { deep: { booleanValue: false } } } },
        empty: { mapValue: {} },
      },
    },
  };

  it('sends back a value it did not change exactly as read', () => {
    const d = draftOf(doc, DB);
    expect(isValid(d, DB)).toBe(true);
    expect(valueOf(d, DB)).toBe(doc);
  });

  it('rebuilds a map around a changed child and keeps the others as read', () => {
    const d = draftOf(doc, DB);
    const entries = d.entries.map((e) => (e.name === 'nested' ? { ...e, value: edited(e.value, {}) } : e));
    const out = valueOf(edited(d, { entries }), DB);
    if (!('mapValue' in out)) throw new Error('not a map');
    const fields = out.mapValue.fields!;
    expect(fields.big).toEqual({ integerValue: '-9223372036854775808' });
    expect(fields.ts).toEqual({ timestampValue: '2026-10-17T14:56:25.123456789Z' });
    expect(fields.negZero).toEqual({ doubleValue: -0 });
    expect(fields.raw).toEqual({ bytesValue: 'AAECAw==' });
    expect(fields.nested).toEqual({ mapValue: { fields: { deep: { booleanValue: false } } } });
  });

  it('parses an edited scalar from its text', () => {
    const int = edited(draftOf({ integerValue: '1' }, DB), { text: '9223372036854775807' });
    expect(valueOf(int, DB)).toEqual({ integerValue: '9223372036854775807' });
    const dbl = edited(draftOf({ doubleValue: 1 }, DB), { text: '-Infinity' });
    expect(valueOf(dbl, DB)).toEqual({ doubleValue: '-Infinity' });
    const ref = edited(draftOf({ referenceValue: `${DB}/documents/a/b` }, DB), { text: 'c/d' });
    expect(valueOf(ref, DB)).toEqual({ referenceValue: `${DB}/documents/c/d` });
    expect(draftOf({ referenceValue: `${DB}/documents/a/b` }, DB).text).toBe('a/b');
    const geo = edited(emptyDraft('geopoint'), { lat: '24.5' });
    expect(valueOf(geo, DB)).toEqual({ geoPointValue: { latitude: 24.5, longitude: 0 } });
    expect(valueOf(emptyDraft('null'), DB)).toEqual({ nullValue: null });
    expect(valueOf(emptyDraft('boolean'), DB)).toEqual({ booleanValue: true });
  });

  it('refuses a map with a duplicate or empty name, or a bad child', () => {
    const child = (name: string, value = emptyDraft('string')) => ({ key: name + Math.random(), name, value });
    expect(isValid({ ...emptyDraft('map'), entries: [child('a'), child('a')] }, DB)).toBe(false);
    expect(isValid({ ...emptyDraft('map'), entries: [child('')] }, DB)).toBe(false);
    expect(isValid({ ...emptyDraft('map'), entries: [child('a', { ...emptyDraft('int64'), text: 'x' })] }, DB)).toBe(false);
    expect(isValid({ ...emptyDraft('array'), entries: [child('', { ...emptyDraft('int64'), text: '12' })] }, DB)).toBe(true);
  });

  it('keeps the text between text types when the type changes', () => {
    expect(retyped({ ...emptyDraft('string'), text: '42' }, 'int64').text).toBe('42');
    expect(retyped({ ...emptyDraft('string'), text: '42' }, 'map').entries).toEqual([]);
  });
});
