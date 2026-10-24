import { describe, expect, it } from 'vitest';
import { ApiError } from '@/lib/api/client';
import type { StoredObject } from './api';
import { allowsEveryOrigin, parseCors } from './cors';
import { formOf, hasProblems, patchOf, problemsOf } from './metadata';
import { byteSize, crumbs, folderProblem, isRasterImage, leafName, nameProblem, parentPrefix } from './paths';
import { issueRange, LOCKED_RULES, offsetOf, openAccess, refusedIssues, SIGNED_IN_RULES, sortIssues, unitsOf } from './rulesText';

describe('paths', () => {
  it('splits a folder into its crumbs', () => {
    expect(crumbs('')).toEqual([]);
    expect(crumbs('a/b c/')).toEqual([
      { label: 'a', prefix: 'a/' },
      { label: 'b c', prefix: 'a/b c/' },
    ]);
    expect(crumbs('a//')).toEqual([
      { label: 'a', prefix: 'a/' },
      { label: '', prefix: 'a//' },
    ]);
  });

  it('finds a name’s folder and its name in it', () => {
    expect(parentPrefix('a/b/c.png')).toBe('a/b/');
    expect(parentPrefix('c.png')).toBe('');
    expect(parentPrefix('a/b/')).toBe('a/');
    expect(leafName('a/b/c.png', 'a/b/')).toBe('c.png');
    expect(leafName('a/b/', 'a/')).toBe('b');
  });

  it('refuses the names the server refuses', () => {
    expect(nameProblem('photos/cat.png')).toBeUndefined();
    expect(nameProblem('')).toBe('empty');
    expect(nameProblem('a\nb')).toBe('chars');
    expect(nameProblem('..')).toBe('dots');
    expect(nameProblem('.well-known/acme-challenge/x')).toBe('reserved');
    expect(nameProblem('é'.repeat(513))).toBe('long');
    expect(nameProblem('é'.repeat(512))).toBeUndefined();
    expect(folderProblem('a/b')).toBe('slash');
    expect(folderProblem('  ')).toBe('empty');
  });

  it('previews raster images only', () => {
    expect(isRasterImage('image/png')).toBe(true);
    expect(isRasterImage('IMAGE/JPEG; charset=binary')).toBe(true);
    expect(isRasterImage('image/svg+xml')).toBe(false);
    expect(isRasterImage('text/html')).toBe(false);
    expect(isRasterImage(undefined)).toBe(false);
  });

  it('picks the unit of a size', () => {
    expect(byteSize(0)).toEqual({ value: 0, unit: 'byte' });
    expect(byteSize(1023)).toEqual({ value: 1023, unit: 'byte' });
    expect(byteSize(1536)).toEqual({ value: 1.5, unit: 'kilobyte' });
    expect(byteSize(5 * 1024 ** 3)).toEqual({ value: 5, unit: 'gigabyte' });
  });
});

describe('rules text', () => {
  it('converts the server’s code point positions to the editor’s', () => {
    const text = 'a😀b\nxy😀z';
    expect(unitsOf('😀b', 1)).toBe(2);
    expect(offsetOf(text, 1, 3)).toBe(3);
    expect(offsetOf(text, 2, 4)).toBe(text.indexOf('z'));
    expect(offsetOf(text, 9, 1)).toBe(text.length);
    // "z" is code point 7 of the file.
    expect(issueRange(text, { description: '', severity: 'ERROR', sourcePosition: { fileName: 'storage.rules', line: 2, column: 4, currentOffset: 7, endOffset: 8 } })).toEqual([
      text.indexOf('z'),
      text.indexOf('z') + 1,
    ]);
  });

  it('reads the issues of a refused publish, errors first', () => {
    const issues = [
      { description: 'w', severity: 'WARNING' as const, sourcePosition: { fileName: 'f', line: 1, column: 1, currentOffset: 0, endOffset: 1 } },
      { description: 'e', severity: 'ERROR' as const, sourcePosition: { fileName: 'f', line: 3, column: 1, currentOffset: 9, endOffset: 10 } },
    ];
    const err = new ApiError(400, 'INVALID_ARGUMENT', 'x', [], undefined, [{ '@type': 'type.aruma.dev/storage.RulesIssues', issues }]);
    expect(sortIssues(refusedIssues(err)).map((i) => i.description)).toEqual(['e', 'w']);
    expect(refusedIssues(new ApiError(500, 'INTERNAL', 'x'))).toEqual([]);
  });

  it('warns about rules without a condition', () => {
    expect(openAccess(LOCKED_RULES)).toBe(false);
    expect(openAccess(SIGNED_IN_RULES)).toBe(false);
    expect(openAccess('match /{a=**} { allow read, write: if true; }')).toBe(true);
    expect(openAccess('match /{a=**} { allow read; }')).toBe(true);
    expect(openAccess('// allow read, write: if true;\nallow read: if false;')).toBe(false);
  });
});

const object: StoredObject = {
  name: 'a.txt',
  bucket: 'b',
  size: 3,
  contentType: 'text/plain',
  cacheControl: 'max-age=60',
  metadata: { owner: 'amal', team: 'x' },
  downloadTokens: [],
  downloadUrls: [],
  generation: '1',
  metageneration: '1',
  md5Hash: '',
  crc32c: '',
  timeCreated: '2026-10-24T10:00:00.000Z',
  updated: '2026-10-24T10:00:00.000Z',
};

describe('metadata form', () => {
  it('patches only what changed: null removes a field or a key', () => {
    const form = formOf(object);
    expect(patchOf(object, form)).toEqual({});
    form.fields.cacheControl = '';
    form.fields.contentLanguage = 'ar';
    form.rows = form.rows.filter((r) => r.key !== 'team').map((r) => (r.key === 'owner' ? { ...r, value: 'omar' } : r));
    form.rows.push({ id: 9, key: 'new', value: '1' });
    expect(patchOf(object, form)).toEqual({ cacheControl: null, contentLanguage: 'ar', metadata: { team: null, owner: 'omar', new: '1' } });
  });

  it('finds what the server would refuse', () => {
    const form = formOf(object);
    form.fields.contentType = 'text/plain\r\nX: y';
    form.rows.push({ id: 7, key: 'owner', value: 'dup' }, { id: 8, key: '', value: 'v' }, { id: 9, key: 'firebaseStorageDownloadTokens', value: 't' });
    const p = problemsOf(form, { headerFieldBytes: 1024, metadataBytes: 8192 });
    expect(p.fields).toEqual({ contentType: 'chars' });
    expect(p.rows).toEqual({ 7: 'duplicate', 8: 'empty', 9: 'reserved' });
    expect(problemsOf(formOf(object), { headerFieldBytes: 4, metadataBytes: 5 })).toMatchObject({ fields: { contentType: 'long', cacheControl: 'long' }, tooLarge: true });
    expect(hasProblems(problemsOf(formOf(object), { headerFieldBytes: 1024, metadataBytes: 8192 }))).toBe(false);
  });
});

describe('CORS', () => {
  it('reads gsutil’s cors.json, and the {cors} form', () => {
    const rules = [{ origin: ['https://a.example'], method: ['GET', 'HEAD'], responseHeader: ['Content-Type'], maxAgeSeconds: 60 }];
    expect(parseCors(JSON.stringify(rules))).toEqual({ rules });
    expect(parseCors(JSON.stringify({ cors: [{ origin: ['*'], method: ['GET'] }] }))).toEqual({ rules: [{ origin: ['*'], method: ['GET'], responseHeader: [], maxAgeSeconds: 0 }] });
    expect(allowsEveryOrigin([{ origin: ['*'], method: ['GET'], responseHeader: [], maxAgeSeconds: 0 }])).toBe(true);
  });

  it.each([
    ['{', { kind: 'json' }],
    ['{}', { kind: 'list' }],
    ['[1]', { kind: 'rule', rule: 1 }],
    ['[{"origin":["a"],"method":["GET"]},{"method":["GET"]}]', { kind: 'origin', rule: 2 }],
    ['[{"origin":["a b"],"method":["GET"]}]', { kind: 'originValue', rule: 1 }],
    ['[{"origin":["a"]}]', { kind: 'method', rule: 1 }],
    ['[{"origin":["a"],"method":["G T"]}]', { kind: 'methodValue', rule: 1 }],
    ['[{"origin":["a"],"method":["GET"],"responseHeader":["a:b"]}]', { kind: 'header', rule: 1 }],
    ['[{"origin":["a"],"method":["GET"],"maxAgeSeconds":-1}]', { kind: 'maxAge', rule: 1 }],
    ['[{"origin":["a"],"method":["GET"],"methods":[]}]', { kind: 'unknown', rule: 1, field: 'methods' }],
  ])('refuses %s', (text, problem) => {
    expect(parseCors(text)).toEqual({ problem });
  });
});
