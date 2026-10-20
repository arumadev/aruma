import type { AndroidApplication, Restrictions } from '@/lib/api/types';
import { BUNDLE_ID, normalizeSha, PACKAGE_NAME } from '@/lib/validation';

export type RestrictionKind = 'none' | 'browser' | 'ip' | 'android' | 'ios';

export interface RestrictionDraft {
  kind: RestrictionKind;
  /** Websites, IP addresses or iOS bundle ids, one per line. */
  lines: string;
  android: AndroidApplication[];
}

export function kindOf(r: Restrictions): RestrictionKind {
  if (r.browserKeyRestrictions) return 'browser';
  if (r.serverKeyRestrictions) return 'ip';
  if (r.androidKeyRestrictions) return 'android';
  if (r.iosKeyRestrictions) return 'ios';
  return 'none';
}

export function toDraft(r: Restrictions): RestrictionDraft {
  const lines = r.browserKeyRestrictions?.allowedReferrers ?? r.serverKeyRestrictions?.allowedIps ?? r.iosKeyRestrictions?.allowedBundleIds ?? [];
  return {
    kind: kindOf(r),
    lines: lines.join('\n'),
    android: r.androidKeyRestrictions?.allowedApplications.map((a) => ({ ...a })) ?? [],
  };
}

const splitLines = (text: string) =>
  text
    .split('\n')
    .map((l) => l.trim())
    .filter(Boolean);

/**
 * Mirrors the server's website check (internal/tenancy/core/keycheck.go): an optional scheme, a
 * host that may start with "*." and an optional port, then an optional path that may end with
 * "*". A wildcard anywhere else is refused, as Google documents.
 */
export function validReferrer(pattern: string): boolean {
  let rest = pattern;
  const scheme = /^[A-Za-z][A-Za-z0-9+.-]*:\/\//.exec(rest);
  if (scheme) rest = rest.slice(scheme[0].length);
  const slash = rest.indexOf('/');
  const hostPort = slash < 0 ? rest : rest.slice(0, slash);
  const path = slash < 0 ? '' : rest.slice(slash);
  if (path.slice(0, -1).includes('*')) return false;
  const host = hostPort.startsWith('*.') ? hostPort.slice(2) : hostPort;
  return /^(\[[0-9A-Fa-f:.]+\]|[^\s:/*?#@[\]]+)(:\d+)?$/.test(host);
}

const IPV4 = /^(25[0-5]|2[0-4]\d|1?\d?\d)(\.(25[0-5]|2[0-4]\d|1?\d?\d)){3}$/;

/** An IPv4 or IPv6 address or a CIDR range of either. The server checks IPv6 fully. */
export function validIP(entry: string): boolean {
  const [addr = '', bits, extra] = entry.split('/');
  if (extra !== undefined) return false;
  const v4 = IPV4.test(addr);
  if (!v4 && !(addr.includes(':') && /^[0-9A-Fa-f:.]+$/.test(addr))) return false;
  return bits === undefined || (/^\d{1,3}$/.test(bits) && Number(bits) <= (v4 ? 32 : 128));
}

export type DraftProblem = 'empty' | 'referrer' | 'ip' | 'bundleId' | 'packageName' | 'sha1';

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
  if (draft.kind === 'browser' && !lines.every(validReferrer)) return 'referrer';
  if (draft.kind === 'ip' && !lines.every(validIP)) return 'ip';
  if (draft.kind === 'ios' && lines.some((l) => !BUNDLE_ID.test(l))) return 'bundleId';
  return null;
}

/** Builds the restrictions to send. API targets set elsewhere (the console API) are kept. */
export function fromDraft(draft: RestrictionDraft, previous: Restrictions = {}): Restrictions {
  const out: Restrictions = previous.apiTargets ? { apiTargets: previous.apiTargets } : {};
  switch (draft.kind) {
    case 'browser':
      out.browserKeyRestrictions = { allowedReferrers: splitLines(draft.lines) };
      break;
    case 'ip':
      out.serverKeyRestrictions = { allowedIps: splitLines(draft.lines) };
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
    r.serverKeyRestrictions?.allowedIps.length ??
    r.androidKeyRestrictions?.allowedApplications.length ??
    r.iosKeyRestrictions?.allowedBundleIds.length ??
    0
  );
}
