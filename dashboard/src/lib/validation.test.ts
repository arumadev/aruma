import { describe, expect, it } from 'vitest';
import { BUNDLE_ID, PACKAGE_NAME, PROJECT_ID, formatSha, normalizeSha, safeNextPath } from './validation';

describe('PROJECT_ID', () => {
  it.each(['chat-app', 'a12345', 'abcdefghijklmnopqrstuvwxyz1234'])('accepts %s', (id) => expect(PROJECT_ID.test(id)).toBe(true));
  it.each(['chat', 'Chat-app', '1chat-app', 'chat-app-', 'chat_app', 'abcdefghijklmnopqrstuvwxyz12345'])('rejects %s', (id) =>
    expect(PROJECT_ID.test(id)).toBe(false),
  );
});

describe('store identifiers', () => {
  it('match the server rules', () => {
    expect(PACKAGE_NAME.test('com.example.app')).toBe(true);
    expect(PACKAGE_NAME.test('com.example')).toBe(true);
    expect(PACKAGE_NAME.test('example')).toBe(false);
    expect(PACKAGE_NAME.test('com.1example')).toBe(false);
    expect(BUNDLE_ID.test('com.example.my-app')).toBe(true);
    expect(BUNDLE_ID.test('com')).toBe(false);
  });
});

describe('SHA fingerprints', () => {
  const sha1 = 'AB:CD:EF:01:23:45:67:89:AB:CD:EF:01:23:45:67:89:AB:CD:EF:01';

  it('normalizes colons, spaces and case', () => {
    expect(normalizeSha(sha1)).toBe('abcdef0123456789abcdef0123456789abcdef01');
    expect(normalizeSha(' ab cd ef 01 23 45 67 89 ab cd ef 01 23 45 67 89 ab cd ef 01 ')).toHaveLength(40);
    expect(normalizeSha('a'.repeat(64))).toHaveLength(64);
  });

  it('rejects other lengths and non-hex input', () => {
    expect(normalizeSha('abc')).toBeNull();
    expect(normalizeSha('g'.repeat(40))).toBeNull();
    expect(normalizeSha('a'.repeat(50))).toBeNull();
  });

  it('formats as keytool prints it', () => {
    expect(formatSha('abcdef0123456789abcdef0123456789abcdef01')).toBe(sha1);
  });
});

describe('safeNextPath', () => {
  it('accepts same-site paths', () => {
    expect(safeNextPath('/project/keys/?id=chat-app')).toBe('/project/keys/?id=chat-app');
  });
  it.each([null, '', 'https://evil.example', '//evil.example', '/\\evil.example', 'projects'])('rejects %s', (next) =>
    expect(safeNextPath(next)).toBeNull(),
  );
});
