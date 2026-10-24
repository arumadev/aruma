// The bucket's CORS rules as the editor holds them: the JSON gsutil and gcloud read from a
// cors.json file, a list of {origin, method, responseHeader, maxAgeSeconds} (ADR-022 §11).
import type { CorsRule } from './api';

export const CORS_EXAMPLE: CorsRule[] = [
  { origin: ['https://example.com'], method: ['GET'], responseHeader: ['Content-Type'], maxAgeSeconds: 3600 },
];

export const formatCors = (rules: CorsRule[]) => (rules.length === 0 ? '[]\n' : `${JSON.stringify(rules, null, 2)}\n`);

/** Why a text is not a list of rules: the first problem found, and in which rule (1-based). */
export type CorsProblem =
  | { kind: 'json' }
  | { kind: 'list' }
  | { kind: 'rule' | 'origin' | 'originValue' | 'method' | 'methodValue' | 'header' | 'maxAge' | 'unknown'; rule: number; field?: string };

// An HTTP token (RFC 9110 §5.6.2): a method or a header name, as the server checks them.
const TOKEN = /^[!#$%&'*+\-.^_`|~0-9A-Za-z]+$/;

const strings = (v: unknown): v is string[] => Array.isArray(v) && v.every((s) => typeof s === 'string');

/** Reads the editor's text: the rules, or the first problem in them. `{"cors": […]}` is read too. */
export function parseCors(text: string): { rules: CorsRule[] } | { problem: CorsProblem } {
  let doc: unknown;
  try {
    doc = JSON.parse(text);
  } catch {
    return { problem: { kind: 'json' } };
  }
  if (doc && typeof doc === 'object' && !Array.isArray(doc) && 'cors' in doc) doc = (doc as { cors: unknown }).cors;
  if (!Array.isArray(doc)) return { problem: { kind: 'list' } };
  const rules: CorsRule[] = [];
  for (const [i, item] of doc.entries()) {
    const rule = i + 1;
    if (!item || typeof item !== 'object' || Array.isArray(item)) return { problem: { kind: 'rule', rule } };
    const r = item as Record<string, unknown>;
    const extra = Object.keys(r).find((k) => !['origin', 'method', 'responseHeader', 'maxAgeSeconds'].includes(k));
    if (extra) return { problem: { kind: 'unknown', rule, field: extra } };
    if (!strings(r.origin) || r.origin.length === 0) return { problem: { kind: 'origin', rule } };
    // An origin is compared byte for byte, or is "*": no space, no comma, no control character.
    if (r.origin.some((o) => o === '' || /[\s,\u0000-\u001f\u007f]/.test(o))) return { problem: { kind: 'originValue', rule } };
    if (!strings(r.method) || r.method.length === 0) return { problem: { kind: 'method', rule } };
    if (r.method.some((m) => !TOKEN.test(m))) return { problem: { kind: 'methodValue', rule } };
    const headers = r.responseHeader ?? [];
    if (!strings(headers) || headers.some((h) => !TOKEN.test(h))) return { problem: { kind: 'header', rule } };
    const maxAge = r.maxAgeSeconds ?? 0;
    if (typeof maxAge !== 'number' || !Number.isInteger(maxAge) || maxAge < 0 || maxAge > 2147483647) return { problem: { kind: 'maxAge', rule } };
    rules.push({ origin: r.origin, method: r.method, responseHeader: headers, maxAgeSeconds: maxAge });
  }
  return { rules };
}

/** Whether a rule lets every site read the files: an origin of "*". */
export const allowsEveryOrigin = (rules: CorsRule[]) => rules.some((r) => r.origin.includes('*'));
