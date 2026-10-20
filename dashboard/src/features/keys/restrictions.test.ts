import { describe, expect, it } from 'vitest';
import { fromDraft, kindOf, restrictionCount, toDraft, validateDraft, validIP, validReferrer } from './restrictions';

const SHA1 = 'AB:CD:EF:01:23:45:67:89:AB:CD:EF:01:23:45:67:89:AB:CD:EF:01';

describe('key restrictions', () => {
  it('round-trips browser referrers through the editor', () => {
    const r = { browserKeyRestrictions: { allowedReferrers: ['https://a.example/*', 'https://b.example/*'] } };
    const draft = toDraft(r);
    expect(draft).toMatchObject({ kind: 'browser', lines: 'https://a.example/*\nhttps://b.example/*' });
    expect(fromDraft(draft)).toEqual(r);
    expect(kindOf(r)).toBe('browser');
    expect(restrictionCount(r)).toBe(2);
  });

  it('drops blank lines and keeps API targets set elsewhere', () => {
    const previous = { apiTargets: [{ service: 'identitytoolkit.googleapis.com' }] };
    expect(fromDraft({ kind: 'ios', lines: ' com.example.app \n\n', android: [] }, previous)).toEqual({
      apiTargets: previous.apiTargets,
      iosKeyRestrictions: { allowedBundleIds: ['com.example.app'] },
    });
  });

  it('sends Android fingerprints as bare hex', () => {
    const out = fromDraft({ kind: 'android', lines: '', android: [{ packageName: 'com.example.app', sha1Fingerprint: SHA1 }] });
    expect(out.androidKeyRestrictions?.allowedApplications[0]).toEqual({
      packageName: 'com.example.app',
      sha1Fingerprint: 'abcdef0123456789abcdef0123456789abcdef01',
    });
  });

  it('round-trips IP addresses through the editor', () => {
    const r = { serverKeyRestrictions: { allowedIps: ['198.51.100.0/24', '2001:db8::1'] } };
    const draft = toDraft(r);
    expect(draft).toMatchObject({ kind: 'ip', lines: '198.51.100.0/24\n2001:db8::1' });
    expect(fromDraft(draft)).toEqual(r);
    expect(kindOf(r)).toBe('ip');
    expect(restrictionCount(r)).toBe(2);
  });

  it('accepts websites only in the forms Google documents', () => {
    for (const ok of ['example.com', 'example.com/*', '*.example.com/*', 'www.example.com/path', 'https://example.com:8080/app*', 'http://[::1]:3000/*']) {
      expect(validReferrer(ok), ok).toBe(true);
    }
    for (const bad of ['*', 'mysubdomain*.example.com', 'example.com/*/x', 'example.com:', 'https://', 'user@example.com']) {
      expect(validReferrer(bad), bad).toBe(false);
    }
  });

  it('accepts IPv4 and IPv6 addresses and CIDR ranges', () => {
    for (const ok of ['198.51.100.1', '198.51.100.0/24', '2001:db8::1', '2001:db8::/64']) expect(validIP(ok), ok).toBe(true);
    for (const bad of ['198.51.100.256', '10.0.0.0/33', 'example.com', '2001:db8::/129', '1.2.3.4/24/1']) expect(validIP(bad), bad).toBe(false);
  });

  it('clears restrictions for "none"', () => {
    expect(fromDraft({ kind: 'none', lines: 'ignored', android: [] })).toEqual({});
  });

  it('finds what the server would reject', () => {
    expect(validateDraft({ kind: 'none', lines: '', android: [] })).toBeNull();
    expect(validateDraft({ kind: 'browser', lines: '  ', android: [] })).toBe('empty');
    expect(validateDraft({ kind: 'browser', lines: 'example.com/*\nmy*.example.com', android: [] })).toBe('referrer');
    expect(validateDraft({ kind: 'ip', lines: '10.0.0.1\n10.0.0.0/40', android: [] })).toBe('ip');
    expect(validateDraft({ kind: 'ip', lines: '10.0.0.1', android: [] })).toBeNull();
    expect(validateDraft({ kind: 'ios', lines: 'not-a-bundle', android: [] })).toBe('bundleId');
    expect(validateDraft({ kind: 'android', lines: '', android: [] })).toBe('empty');
    expect(validateDraft({ kind: 'android', lines: '', android: [{ packageName: 'app', sha1Fingerprint: SHA1 }] })).toBe('packageName');
    expect(validateDraft({ kind: 'android', lines: '', android: [{ packageName: 'com.example.app', sha1Fingerprint: 'a'.repeat(64) }] })).toBe(
      'sha1',
    );
    expect(validateDraft({ kind: 'android', lines: '', android: [{ packageName: 'com.example.app', sha1Fingerprint: SHA1 }] })).toBeNull();
  });
});
