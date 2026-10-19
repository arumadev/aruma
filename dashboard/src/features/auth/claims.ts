// Client-side checks of a custom claims object, mirroring the server (internal/services/auth/core,
// validClaims) so the editor can explain a problem as the user types. The server stays the
// authority.

/** Firebase's limit on the serialized claims, in bytes. */
export const CLAIMS_MAX_BYTES = 1000;

// Names the ID token already uses; they may not be custom claims.
const RESERVED = new Set([
  'acr',
  'amr',
  'at_hash',
  'aud',
  'auth_time',
  'azp',
  'cnf',
  'c_hash',
  'exp',
  'iat',
  'iss',
  'jti',
  'nbf',
  'nonce',
  'sub',
  'firebase',
  'user_id',
]);

export type ClaimsProblem = 'json' | 'object' | 'reserved' | 'size';

export interface ClaimsCheck {
  /** The compact JSON that would be saved; empty clears the claims. */
  compact: string;
  /** UTF-8 bytes of the compact JSON, or of the raw text while it does not parse. */
  bytes: number;
  problem: ClaimsProblem | null;
  /** The first reserved name used. */
  reserved?: string;
}

const utf8 = (s: string) => new TextEncoder().encode(s).length;

export function checkClaims(text: string): ClaimsCheck {
  const trimmed = text.trim();
  if (trimmed === '') return { compact: '', bytes: 0, problem: null };
  let value: unknown;
  try {
    value = JSON.parse(trimmed);
  } catch {
    return { compact: trimmed, bytes: utf8(trimmed), problem: 'json' };
  }
  const compact = JSON.stringify(value);
  const bytes = utf8(compact);
  if (value === null || typeof value !== 'object' || Array.isArray(value)) return { compact, bytes, problem: 'object' };
  const reserved = Object.keys(value).find((k) => RESERVED.has(k));
  if (reserved) return { compact, bytes, problem: 'reserved', reserved };
  if (bytes > CLAIMS_MAX_BYTES) return { compact, bytes, problem: 'size' };
  return { compact, bytes, problem: null };
}

/** Pretty-prints stored claims for editing. */
export function formatClaims(stored: string | undefined): string {
  if (!stored) return '';
  try {
    return JSON.stringify(JSON.parse(stored), null, 2);
  } catch {
    return stored;
  }
}
