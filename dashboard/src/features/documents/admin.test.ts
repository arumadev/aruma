import { describe, expect, it } from 'vitest';
import { ApiError } from '@/lib/api/client';
import type { Field, Index, UsageReport } from './api';
import {
  automaticSettings,
  compositeIndex,
  compositeProblems,
  exemptionForm,
  exemptionIndexes,
  groupOf,
  hasProblems,
  idOf,
  idProblem,
  isExemption,
  listedFields,
  newCompositeForm,
  pageOf,
} from './indexes/indexes';
import { authPayload, lineMarks, methodNoun, parseData, pathProblem, refusedIssues, toValue } from './rules/rulesText';
import { merge, niceMax, perDay, points, emptyRow } from './usage/usage';

describe('rules helpers', () => {
  it('checks a simulation path', () => {
    expect(pathProblem('')).toBe('required');
    expect(pathProblem('users//x')).toBe('empty');
    expect(pathProblem('users')).toBe('odd');
    expect(pathProblem('/users/alice/')).toBeUndefined();
    expect(pathProblem('users/alice/posts/p1')).toBeUndefined();
  });

  it('builds the recorded authentication payload', () => {
    expect(authPayload({ uid: 'u1', email: 'a@b.c', emailVerified: true, provider: 'password' }, 'chat-app')).toEqual({
      uid: 'u1',
      token: {
        sub: 'u1',
        aud: 'chat-app',
        email: 'a@b.c',
        email_verified: true,
        phone_number: '',
        name: '',
        firebase: { sign_in_provider: 'password' },
      },
    });
    expect(authPayload({}, 'p')).toMatchObject({ uid: '', token: { firebase: { sign_in_provider: 'google.com' }, email_verified: false } });
  });

  it('turns JSON data into REST values', () => {
    expect(toValue(3)).toEqual({ integerValue: '3' });
    expect(toValue(1.5)).toEqual({ doubleValue: 1.5 });
    expect(toValue(null)).toEqual({ nullValue: null });
    expect(parseData('{"a":[1,"x"],"b":{"c":true}}')).toEqual({
      fields: {
        a: { arrayValue: { values: [{ integerValue: '1' }, { stringValue: 'x' }] } },
        b: { mapValue: { fields: { c: { booleanValue: true } } } },
      },
    });
    expect(parseData('  ')).toEqual({ fields: {} });
    expect(parseData('{')).toEqual({ problem: 'syntax' });
    expect(parseData('[1]')).toEqual({ problem: 'object' });
  });

  it('names a get a read', () => {
    expect(methodNoun('get')).toBe('read');
    expect(methodNoun('update')).toBe('update');
  });

  it('marks the line of an issue, an error over a warning', () => {
    const at = (line: number) => ({ fileName: 'f', line, column: 1, currentOffset: 0, endOffset: 1 });
    const marks = lineMarks([
      { description: 'w', severity: 'WARNING', sourcePosition: at(2) },
      { description: 'e', severity: 'ERROR', sourcePosition: at(2) },
      { description: 'x', severity: 'WARNING', sourcePosition: at(5) },
      { description: 'nowhere', severity: 'ERROR' },
    ]);
    expect(marks.get(2)).toEqual({ severity: 'error', message: 'w\ne' });
    expect(marks.get(5)).toEqual({ severity: 'warning', message: 'x' });
    expect(marks.size).toBe(2);
  });

  it('reads the issues of a refused source', () => {
    const issue = { description: 'bad', severity: 'ERROR' as const };
    const err = new ApiError(400, 'INVALID_ARGUMENT', 'x', [], undefined, [{ '@type': 'type.aruma.dev/documents.RulesIssues', issues: [issue] }]);
    expect(refusedIssues(err)).toEqual([issue]);
    expect(refusedIssues(new ApiError(500, 'INTERNAL', 'x'))).toEqual([]);
    expect(refusedIssues(new Error('x'))).toEqual([]);
  });
});

describe('index helpers', () => {
  const name = 'projects/p/databases/(default)/collectionGroups/posts/indexes/abc123';

  it('reads names', () => {
    expect(groupOf(name)).toBe('posts');
    expect(idOf(name)).toBe('abc123');
  });

  it('lists __name__ last, in the direction of the last field', () => {
    const asc: Index = { queryScope: 'COLLECTION', fields: [{ fieldPath: 'a', order: 'ASCENDING' }, { fieldPath: 'b', order: 'DESCENDING' }] };
    expect(listedFields(asc).map((f) => [f.fieldPath, f.order])).toEqual([['a', 'ASCENDING'], ['b', 'DESCENDING'], ['__name__', 'DESCENDING']]);
    const named: Index = { queryScope: 'COLLECTION', fields: [{ fieldPath: 'a', order: 'ASCENDING' }, { fieldPath: '__name__', order: 'ASCENDING' }] };
    expect(listedFields(named)).toHaveLength(2);
  });

  it('checks the composite form', () => {
    const form = newCompositeForm();
    expect(hasProblems(compositeProblems(form))).toBe(true);
    form.collection = 'posts';
    form.fields = [
      { path: 'a', kind: 'ASCENDING' },
      { path: ' tags ', kind: 'ARRAYS' },
    ];
    expect(hasProblems(compositeProblems(form))).toBe(false);
    expect(compositeIndex(form)).toEqual({
      queryScope: 'COLLECTION',
      fields: [{ fieldPath: 'a', order: 'ASCENDING' }, { fieldPath: 'tags', arrayConfig: 'CONTAINS' }],
    });
    expect(compositeProblems({ ...form, fields: [form.fields[0]!] }).count).toBe(true);
    expect(idProblem('a/b')).toBe('slash');
    expect(idProblem('__a__')).toBe('name');
    expect(idProblem(' ')).toBe('required');
  });

  it('lists only the enabled kinds of an exemption, and leaves an unchosen scope as it was', () => {
    const kinds = { ASCENDING: true, DESCENDING: true, ARRAYS: false };
    const only = exemptionIndexes({ scopes: { COLLECTION: true, COLLECTION_GROUP: false }, kinds });
    expect(only.map((i) => [i.queryScope, i.fields[0]])).toEqual([
      ['COLLECTION', { order: 'ASCENDING' }],
      ['COLLECTION', { order: 'DESCENDING' }],
    ]);
    const group = exemptionIndexes({ scopes: { COLLECTION: false, COLLECTION_GROUP: true }, kinds });
    expect(group.filter((i) => i.queryScope === 'COLLECTION')).toHaveLength(3);
    expect(group.filter((i) => i.queryScope === 'COLLECTION_GROUP')).toHaveLength(2);
  });

  it('opens an exemption in the form it was made with', () => {
    const field: Field = {
      name: 'projects/p/databases/(default)/collectionGroups/posts/fields/tags',
      indexConfig: {
        usesAncestorConfig: false,
        indexes: [
          { queryScope: 'COLLECTION', fields: [{ order: 'ASCENDING' }] },
          { queryScope: 'COLLECTION', fields: [{ order: 'DESCENDING' }] },
        ],
      },
    };
    const form = exemptionForm(field);
    expect(form).toMatchObject({ collection: 'posts', field: 'tags', scopes: { COLLECTION: true, COLLECTION_GROUP: false }, kinds: { ASCENDING: true, DESCENDING: true, ARRAYS: false } });
    expect(isExemption(field)).toBe(true);
    expect(isExemption({ ...field, name: 'projects/p/databases/(default)/collectionGroups/__default__/fields/__default__' })).toBe(false);
    expect(isExemption({ name: field.name, indexConfig: { usesAncestorConfig: true } })).toBe(false);
  });

  it('takes the automatic settings from the default override, or a new database’s', () => {
    expect(automaticSettings([])).toEqual({
      COLLECTION: { ASCENDING: true, DESCENDING: true, ARRAYS: true },
      COLLECTION_GROUP: { ASCENDING: false, DESCENDING: false, ARRAYS: false },
    });
    const override: Field = {
      name: 'projects/p/databases/(default)/collectionGroups/__default__/fields/*',
      indexConfig: { indexes: [{ queryScope: 'COLLECTION', fields: [{ order: 'ASCENDING' }] }] },
    };
    expect(automaticSettings([override]).COLLECTION).toEqual({ ASCENDING: true, DESCENDING: false, ARRAYS: false });
  });

  it('pages a list', () => {
    const items = Array.from({ length: 25 }, (_, i) => i);
    expect(pageOf(items, 2, 10)).toEqual({ rows: [20, 21, 22, 23, 24], pages: 3, page: 2 });
    expect(pageOf(items, 9, 10).page).toBe(2);
    expect(pageOf([], 0, 10)).toEqual({ rows: [], pages: 1, page: 0 });
  });
});

describe('usage helpers', () => {
  const row = (hour: string, reads: number, peak = 0) => ({ ...emptyRow(), hour, reads, listenersPeak: peak });
  const report = (period: UsageReport['period'], start: string, end: string, hours: UsageReport['hours']): UsageReport => ({
    period,
    start,
    end,
    granularity: 'hour',
    totals: emptyRow(),
    hours,
  });

  it('draws an hour a point and zeroes the quiet ones', () => {
    const pts = points(report('60m', '2026-10-28T09:00:00Z', '2026-10-28T10:30:00Z', [row('2026-10-28T10:00:00Z', 7)]));
    expect(pts.map((p) => p.row.reads)).toEqual([0, 7]);
  });

  it('groups a week or a month into days, adding counts and keeping the peaks', () => {
    expect(perDay('24h')).toBe(false);
    expect(perDay('7d')).toBe(true);
    const pts = points(
      report('7d', '2026-10-21T10:00:00Z', '2026-10-22T12:00:00Z', [row('2026-10-21T10:00:00Z', 1, 4), row('2026-10-21T23:00:00Z', 2, 9), row('2026-10-22T01:00:00Z', 5)]),
    );
    expect(pts.map((p) => [p.row.reads, p.row.listenersPeak])).toEqual([[3, 9], [5, 0]]);
  });

  it('merges counts by sum and peaks by maximum', () => {
    expect(merge({ ...emptyRow(), reads: 1, connectionsPeak: 3 }, { ...emptyRow(), reads: 2, connectionsPeak: 2 })).toMatchObject({ reads: 3, connectionsPeak: 3 });
  });

  it('rounds an axis up to 1, 2 or 5 times a power of ten', () => {
    expect([0, 1, 3, 7, 12, 130, 2100].map(niceMax)).toEqual([1, 1, 5, 10, 20, 200, 5000]);
  });

  it('ignores a report with no dates', () => {
    expect(points(report('24h', 'x', 'y', []))).toEqual([]);
  });
});
