// The rules editor's helpers: the two starting templates, whether a document opens the whole
// database to everyone, and the errors the server reports with their positions.
import { ApiError } from '@/lib/api/client';
import type { RuleError } from '@/lib/api/types';

/** A new database's rules (ADR-020): only admin and service credentials reach the data. */
export const LOCKED_RULES = `{
  "rules": {
    ".read": false,
    ".write": false
  }
}
`;

/** Firebase's test mode: anyone with the database URL reads and writes everything. */
export const TEST_MODE_RULES = `{
  "rules": {
    ".read": true,
    ".write": true
  }
}
`;

const RULES_ERRORS = 'type.aruma.dev/rtdb.RulesErrors';

/** The errors of a refused rules document, in document order; empty for any other failure. */
export function ruleErrors(error: unknown): RuleError[] {
  if (!(error instanceof ApiError)) return [];
  for (const d of error.details) {
    if (d && typeof d === 'object' && (d as { '@type'?: string })['@type'] === RULES_ERRORS) {
      return ((d as { errors?: RuleError[] }).errors ?? []).filter((e) => Number.isInteger(e.line) && Number.isInteger(e.column));
    }
  }
  return [];
}

/** Removes // and /* *\/ comments outside strings; rules documents may hold them. */
export function stripComments(text: string): string {
  let out = '';
  for (let i = 0; i < text.length; i++) {
    const c = text[i];
    if (c === '"') {
      const start = i;
      for (i++; i < text.length && text[i] !== '"'; i++) {
        if (text[i] === '\\') i++;
      }
      out += text.slice(start, i + 1);
    } else if (c === '/' && text[i + 1] === '/') {
      while (i < text.length && text[i] !== '\n') i++;
      out += '\n';
    } else if (c === '/' && text[i + 1] === '*') {
      const end = text.indexOf('*/', i + 2);
      i = end < 0 ? text.length : end + 1;
      out += ' ';
    } else {
      out += c;
    }
  }
  return out;
}

const isTrue = (v: unknown) => v === true || (typeof v === 'string' && v.trim() === 'true');

/**
 * Which accesses a document grants everyone on the whole database: a .read or .write of true
 * at the top. Nested rules are not looked at, nor documents that do not parse.
 */
export function openAccess(text: string): { read: boolean; write: boolean } {
  try {
    const doc = JSON.parse(stripComments(text)) as { rules?: Record<string, unknown> };
    const rules = doc?.rules;
    if (!rules || typeof rules !== 'object') return { read: false, write: false };
    return { read: isTrue(rules['.read']), write: isTrue(rules['.write']) };
  } catch {
    return { read: false, write: false };
  }
}

/** The character offset of a 1-based line and column in text. */
export function offsetOf(text: string, line: number, column: number): number {
  let offset = 0;
  for (let l = 1; l < line; l++) {
    const next = text.indexOf('\n', offset);
    if (next < 0) return text.length;
    offset = next + 1;
  }
  return Math.min(offset + Math.max(column - 1, 0), text.length);
}
