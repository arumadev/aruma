import { describe, expect, it } from 'vitest';
import { checkClaims, formatClaims } from './claims';
import { isValidDomain, normalizeDomain } from './domains';
import { providerLabels } from './providers';
import { bodyText, fillVariables } from './templatePreview';

describe('checkClaims', () => {
  it('saves compact JSON and counts its UTF-8 bytes', () => {
    expect(checkClaims('{\n  "role": "admin"\n}')).toEqual({ compact: '{"role":"admin"}', bytes: 16, problem: null });
    expect(checkClaims('{"name": "أمل"}').bytes).toBe(17); // three 2-byte letters
    expect(checkClaims('   ')).toEqual({ compact: '', bytes: 0, problem: null });
  });

  it.each([
    ['{"role": ', 'json'],
    ['[1, 2]', 'object'],
    ['"admin"', 'object'],
    ['null', 'object'],
    ['{"sub": "x"}', 'reserved'],
    ['{"firebase": {}}', 'reserved'],
  ])('refuses %s (%s)', (text, problem) => {
    expect(checkClaims(text).problem).toBe(problem);
  });

  it('allows exactly 1000 bytes and refuses one more', () => {
    const at = (n: number) => `{"a":"${'x'.repeat(n - 8)}"}`;
    expect(checkClaims(at(1000))).toMatchObject({ bytes: 1000, problem: null });
    expect(checkClaims(at(1001))).toMatchObject({ bytes: 1001, problem: 'size' });
  });

  it('pretty-prints stored claims', () => {
    expect(formatClaims('{"tier": "gold"}')).toBe('{\n  "tier": "gold"\n}');
    expect(formatClaims(undefined)).toBe('');
  });
});

describe('authorized domains', () => {
  it.each(['localhost', 'chat.example.com', 'xn--mgbh0fb.example', '10.0.0.2', '::1'])('accepts %s', (d) => {
    expect(isValidDomain(normalizeDomain(d))).toBe(true);
  });

  it.each(['', 'https://chat.example.com', 'chat.example.com/', 'chat.example.com:8080', '*.example.com', '-a.example.com', 'a..b', '999.1.1.1'])(
    'refuses %s',
    (d) => {
      expect(isValidDomain(normalizeDomain(d))).toBe(false);
    },
  );

  it('normalizes case and spaces', () => {
    expect(normalizeDomain('  Chat.Example.COM ')).toBe('chat.example.com');
  });
});

describe('providerLabels', () => {
  const base = { uid: 'u', emailVerified: false, disabled: false, hasPassword: false, createTime: '', tokensValidAfterTime: '' };

  it('names linked providers and calls a user without any anonymous', () => {
    expect(providerLabels({ ...base, providers: [{ providerId: 'google.com' }, { providerId: 'password' }] })).toEqual(['google', 'password']);
    expect(providerLabels({ ...base, providers: [] })).toEqual(['anonymous']);
    expect(providerLabels({ ...base, email: 'a@example.com', providers: [] })).toEqual([]);
  });
});

describe('template preview', () => {
  const values = { '%APP_NAME%': 'دردشة', '%DISPLAY_NAME%': 'سارة', '%EMAIL%': 'a@example.com', '%NEW_EMAIL%': 'b@example.com', '%LINK%': 'https://x/' };

  it('fills every variable and leaves other text alone', () => {
    expect(fillVariables('%APP_NAME%: %LINK% %UNKNOWN% 100%', values)).toBe('دردشة: https://x/ %UNKNOWN% 100%');
  });

  it('reduces HTML to text without running or loading anything', () => {
    expect(bodyText('<p>Hello <b>%DISPLAY_NAME%</b></p><p><a href="%LINK%">Verify</a><br>Thanks</p><script>alert(1)</script><img src=x onerror=alert(2)>')).toBe(
      'Hello %DISPLAY_NAME%\nVerify\nThanks',
    );
    expect(bodyText('Plain\ntext')).toBe('Plain\ntext');
  });
});
