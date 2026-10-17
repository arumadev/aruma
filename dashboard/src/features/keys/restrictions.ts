import type { AndroidApplication, Restrictions } from '@/lib/api/types';
import { BUNDLE_ID, normalizeSha, PACKAGE_NAME } from '@/lib/validation';

export type RestrictionKind = 'none' | 'browser' | 'android' | 'ios';

export interface RestrictionDraft {
  kind: RestrictionKind;
  /** Browser referrers or iOS bundle ids, one per line. */
  lines: string;
  android: AndroidApplication[];
}

export const kindOf = (r: Restrictions): RestrictionKind =>
  r.browserKeyRestrictions ? 'browser' : r.androidKeyRestrictions ? 'android' : r.iosKeyRestrictions ? 'ios' : 'none';

export function toDraft(r: Restrictions): RestrictionDraft {
  return {
    kind: kindOf(r),
    lines: (r.browserKeyRestrictions?.allowedReferrers ?? r.iosKeyRestrictions?.allowedBundleIds ?? []).join('\n'),
    android: r.androidKeyRestrictions?.allowedApplications.map((a) => ({ ...a })) ?? [],
  };
}

const splitLines = (text: string) =>
  text
    .split('\n')
    .map((l) => l.trim())
    .filter(Boolean);

export type DraftProblem = 'empty' | 'bundleId' | 'packageName' | 'sha1';

/** Finds the first problem the server would reject, so the form can say so before sending. */
export function validateDraft(draft: RestrictionDraft): DraftProblem | null {
  if (draft.kind === 'none') return null;
  if (draft.kind === 'android') {
    if (draft.android.length === 0) return 'empty';
    for (const app of draft.android) {
      if (!PACKAGE_NAME.test(app.packageName.trim())) return 'packageName';
      if (normalizeSha(app.sha1Fingerprint)?.length !== 40) return 'sha1';
    }
    return null;
  }
  const lines = splitLines(draft.lines);
  if (lines.length === 0) return 'empty';
  if (draft.kind === 'ios' && lines.some((l) => !BUNDLE_ID.test(l))) return 'bundleId';
  return null;
}

/** Builds the restrictions to send. API targets set elsewhere (Management API) are kept. */
export function fromDraft(draft: RestrictionDraft, previous: Restrictions = {}): Restrictions {
  const out: Restrictions = previous.apiTargets ? { apiTargets: previous.apiTargets } : {};
  switch (draft.kind) {
    case 'browser':
      out.browserKeyRestrictions = { allowedReferrers: splitLines(draft.lines) };
      break;
    case 'ios':
      out.iosKeyRestrictions = { allowedBundleIds: splitLines(draft.lines) };
      break;
    case 'android':
      out.androidKeyRestrictions = {
        allowedApplications: draft.android.map((a) => ({
          packageName: a.packageName.trim(),
          sha1Fingerprint: normalizeSha(a.sha1Fingerprint) ?? a.sha1Fingerprint.trim(),
        })),
      };
      break;
    case 'none':
      break;
  }
  return out;
}

/** How many entries a key's restriction lists. */
export function restrictionCount(r: Restrictions): number {
  return (
    r.browserKeyRestrictions?.allowedReferrers.length ??
    r.androidKeyRestrictions?.allowedApplications.length ??
    r.iosKeyRestrictions?.allowedBundleIds.length ??
    0
  );
}
