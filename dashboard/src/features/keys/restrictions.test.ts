import { describe, expect, it } from 'vitest';
import { fromDraft, kindOf, restrictionCount, toDraft, validateDraft } from './restrictions';

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

  it('clears restrictions for "none"', () => {
    expect(fromDraft({ kind: 'none', lines: 'ignored', android: [] })).toEqual({});
  });

  it('finds what the server would reject', () => {
    expect(validateDraft({ kind: 'none', lines: '', android: [] })).toBeNull();
    expect(validateDraft({ kind: 'browser', lines: '  ', android: [] })).toBe('empty');
    expect(validateDraft({ kind: 'ios', lines: 'not-a-bundle', android: [] })).toBe('bundleId');
    expect(validateDraft({ kind: 'android', lines: '', android: [] })).toBe('empty');
    expect(validateDraft({ kind: 'android', lines: '', android: [{ packageName: 'app', sha1Fingerprint: SHA1 }] })).toBe('packageName');
    expect(validateDraft({ kind: 'android', lines: '', android: [{ packageName: 'com.example.app', sha1Fingerprint: 'a'.repeat(64) }] })).toBe(
      'sha1',
    );
    expect(validateDraft({ kind: 'android', lines: '', android: [{ packageName: 'com.example.app', sha1Fingerprint: SHA1 }] })).toBeNull();
  });
});
