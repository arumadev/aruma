// Client-side checks that mirror the server (internal/platform/ids and tenancy/core), so a
// form can explain a problem before submitting. The server stays the authority.

export const PROJECT_ID = /^[a-z][a-z0-9-]{4,28}[a-z0-9]$/;
export const SERVICE_ACCOUNT_ID = PROJECT_ID;
export const PACKAGE_NAME = /^[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z][A-Za-z0-9_]*)+$/;
export const BUNDLE_ID = /^[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)+$/;
export const PASSWORD_MIN = 8;
export const PASSWORD_MAX = 1024;
export const DISPLAY_NAME_MAX = 100;
export const TOKEN_MAX_DAYS = 366;

/** Strips colons and spaces and lower-cases a SHA fingerprint; null unless it is SHA-1 or SHA-256. */
export function normalizeSha(input: string): string | null {
  const hex = input.replace(/[\s:]/g, '').toLowerCase();
  if (!/^[0-9a-f]+$/.test(hex) || (hex.length !== 40 && hex.length !== 64)) return null;
  return hex;
}

/** Formats a hex fingerprint as colon-separated upper-case pairs, as keytool prints it. */
export function formatSha(hex: string): string {
  return (hex.toUpperCase().match(/.{1,2}/g) ?? []).join(':');
}

/** Only same-site absolute paths are accepted as a post-login destination. */
export function safeNextPath(next: string | null): string | null {
  if (!next || !next.startsWith('/') || next.startsWith('//') || next.includes('\\')) return null;
  return next;
}
