import { describe, expect, it } from 'vitest';
import { ApiError } from '@/lib/api/client';
import { LOCKED_RULES, offsetOf, openAccess, ruleErrors, stripComments, TEST_MODE_RULES } from './rulesText';
import { checkKey, checkValue, childPath, DEFAULT_LIMITS, formatValueInput, parentPath, parseValueInput, pathKeys } from './validation';

describe('keys', () => {
  it.each([
    ['', 'empty'],
    ['a.b', 'chars'],
    ['a#b', 'chars'],
    ['a$b', 'chars'],
    ['a[0]', 'chars'],
    ['a/b', 'chars'],
    ['tab\there', 'chars'],
    ['del\u007f', 'chars'],
    ['k'.repeat(769), 'long'],
    // 768 bytes: Arabic letters are two bytes each.
    ['م'.repeat(385), 'long'],
    ['م'.repeat(384), null],
    ['-NkVz8_x', null],
    ['with space', null],
    ['سلام', null],
  ])('checkKey(%j) is %s', (key, problem) => {
    expect(checkKey(key)).toBe(problem);
  });
});

describe('paths', () => {
  it('joins and splits paths from the root', () => {
    expect(pathKeys('/')).toEqual([]);
    expect(pathKeys('/users/amal')).toEqual(['users', 'amal']);
    expect(childPath('/', 'users')).toBe('/users');
    expect(childPath('/users', 'amal')).toBe('/users/amal');
    expect(parentPath('/users/amal')).toBe('/users');
    expect(parentPath('/users')).toBe('/');
    expect(parentPath('/')).toBe('/');
  });
});

describe('values', () => {
  it('reads text as the Firebase console does', () => {
    expect(parseValueInput('hello')).toEqual({ value: 'hello', problem: null });
    expect(parseValueInput('42')).toEqual({ value: 42, problem: null });
    expect(parseValueInput('true')).toEqual({ value: true, problem: null });
    expect(parseValueInput('"42"')).toEqual({ value: '42', problem: null });
    expect(parseValueInput('{"a": 1}')).toEqual({ value: { a: 1 }, problem: null });
    expect(parseValueInput('  ')).toEqual({ problem: 'empty' });
    expect(parseValueInput('{"a": ')).toEqual({ problem: 'json' });
    expect(parseValueInput('[1, 2')).toEqual({ problem: 'json' });
  });

  it('shows a value so that it reads back the same', () => {
    for (const v of ['hello', '42', 'true', '"q"', ' padded ', '', 42, false]) {
      const text = formatValueInput(v);
      const back = parseValueInput(text);
      expect(back.problem === 'empty' ? '' : back.value).toEqual(v);
    }
    expect(formatValueInput('hello')).toBe('hello');
    expect(formatValueInput('42')).toBe('"42"');
  });

  it('checks keys, sizes and depth inside a value', () => {
    expect(checkValue({ a: { b: 1 } }, ['x'])).toEqual({ problem: null });
    expect(checkValue(null, ['x'])).toEqual({ problem: 'null' });
    expect(checkValue({ ok: 1, 'bad.key': 2 }, [])).toEqual({ problem: 'key', key: 'bad.key' });
    expect(checkValue({ '': 1 }, [])).toEqual({ problem: 'key', key: '' });
    // The export format's own keys are allowed.
    expect(checkValue({ '.value': 'x', '.priority': 1 }, ['a'])).toEqual({ problem: null });
    expect(checkValue({ at: { '.sv': 'timestamp' } }, ['a'])).toEqual({ problem: null });
    expect(checkValue('x'.repeat(11), [], { ...DEFAULT_LIMITS, stringBytes: 10 })).toEqual({ problem: 'string' });
    let deep: Record<string, unknown> = { leaf: 1 };
    // 33 keys from the root; 32 is the most.
    for (let i = 0; i < 32; i++) deep = { k: deep };
    expect(checkValue(deep as never, [])).toEqual({ problem: 'depth' });
    expect(checkValue(deep as never, [], { ...DEFAULT_LIMITS, depth: 40 })).toEqual({ problem: null });
    expect(checkValue({ [`${'k'.repeat(400)}`]: { [`${'k'.repeat(400)}`]: 1 } }, [])).toEqual({ problem: 'path' });
    expect(checkValue('x'.repeat(2000), [], { ...DEFAULT_LIMITS, writeBytes: 1000 })).toEqual({ problem: 'size' });
  });
});

describe('rules text', () => {
  it('strips comments outside strings', () => {
    expect(stripComments('{"a": "//not", /* x */ "b": 1} // tail')).toBe('{"a": "//not",   "b": 1} \n');
    expect(stripComments('"a\\"//b"')).toBe('"a\\"//b"');
  });

  it('finds rules that open the whole database', () => {
    expect(openAccess(LOCKED_RULES)).toEqual({ read: false, write: false });
    expect(openAccess(TEST_MODE_RULES)).toEqual({ read: true, write: true });
    expect(openAccess('{\n // public profiles\n "rules": {".read": "true", ".write": "auth != null"}}')).toEqual({ read: true, write: false });
    expect(openAccess('{"rules": {"public": {".read": true}}}')).toEqual({ read: false, write: false });
    expect(openAccess('not json')).toEqual({ read: false, write: false });
  });

  it('reads the errors of a refused save', () => {
    const detail = { '@type': 'type.aruma.dev/rtdb.RulesErrors', errors: [{ line: 3, column: 14, path: 'rules/.read', message: 'Unexpected end.' }] };
    expect(ruleErrors(new ApiError(400, 'INVALID_ARGUMENT', 'x', [], undefined, [detail]))).toEqual(detail.errors);
    expect(ruleErrors(new ApiError(400, 'INVALID_ARGUMENT', 'x'))).toEqual([]);
    expect(ruleErrors(new Error('x'))).toEqual([]);
  });

  it('turns a line and column into an offset', () => {
    const text = 'ab\ncde\nf';
    expect(offsetOf(text, 1, 1)).toBe(0);
    expect(offsetOf(text, 2, 3)).toBe(5);
    expect(offsetOf(text, 3, 1)).toBe(7);
    expect(offsetOf(text, 9, 1)).toBe(text.length);
  });
});
