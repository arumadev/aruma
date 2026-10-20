// Client-side checks of keys and values, mirroring the server (internal/services/rtdb/core,
// path.go and node.go) and the SDKs' validation.ts, so the editor explains a problem before
// anything is sent. The server stays the authority.
import type { DatabaseLimits, JsonValue } from '@/lib/api/types';

/** The server's limits, used until the database's own have loaded. */
export const DEFAULT_LIMITS: DatabaseLimits = {
  keyBytes: 768,
  pathBytes: 768,
  depth: 32,
  stringBytes: 10 << 20,
  writeBytes: 1 << 20,
  exportBytes: 16 << 20,
};

// . # $ [ ] / and the ASCII control characters (validation.ts:45 in the SDK).
const FORBIDDEN = /[.#$[\]/\u0000-\u001f\u007f]/;

// Keys of the export format: a leaf with a priority, a priority, a server value.
const PSEUDO_KEYS = new Set(['.value', '.priority', '.sv']);

export const utf8Length = (s: string) => new TextEncoder().encode(s).length;

/** The keys of a /-separated path from the root: "/" is [], "/a/b" is ["a", "b"]. */
export const pathKeys = (path: string) => path.split('/').filter(Boolean);

/** The path of a child. */
export const childPath = (parent: string, key: string) => (parent === '/' ? `/${key}` : `${parent}/${key}`);

/** The path of a node's parent; the root is its own. */
export const parentPath = (path: string) => {
  const keys = pathKeys(path);
  return keys.length <= 1 ? '/' : `/${keys.slice(0, -1).join('/')}`;
};

export type KeyProblem = 'empty' | 'chars' | 'long';

export function checkKey(key: string, limits: DatabaseLimits = DEFAULT_LIMITS): KeyProblem | null {
  if (key === '') return 'empty';
  if (FORBIDDEN.test(key)) return 'chars';
  if (utf8Length(key) > limits.keyBytes) return 'long';
  return null;
}

/** What the user typed in a value field. */
export type ParsedInput = { value: JsonValue; problem: null } | { value?: undefined; problem: 'empty' | 'json' };

/**
 * Reads a value as the Firebase console does: JSON when it parses (a number, true, false, a
 * "quoted string", an object or array), any other text as a string. Text that starts like an
 * object or array but does not parse is a mistake, not a string.
 */
export function parseValueInput(text: string): ParsedInput {
  const trimmed = text.trim();
  if (trimmed === '') return { problem: 'empty' };
  try {
    return { value: JSON.parse(trimmed) as JsonValue, problem: null };
  } catch {
    if (trimmed.startsWith('{') || trimmed.startsWith('[')) return { problem: 'json' };
    return { value: text, problem: null };
  }
}

export type ValueProblem = 'null' | 'key' | 'string' | 'depth' | 'path' | 'size';

export interface ValueCheck {
  problem: ValueProblem | null;
  /** The key at fault, for 'key'. */
  key?: string;
}

/**
 * Checks a value about to be written at the path whose keys are `at`: its keys, the size of
 * its strings, its depth and the length of every path inside it, and the size of the request.
 */
export function checkValue(value: JsonValue, at: string[], limits: DatabaseLimits = DEFAULT_LIMITS): ValueCheck {
  if (value === null) return { problem: 'null' };
  const atBytes = at.reduce((n, k) => n + utf8Length(k), 0);
  let found: ValueCheck | null = null;

  const walk = (v: JsonValue, depth: number, bytes: number): void => {
    if (found) return;
    if (depth > limits.depth) {
      found = { problem: 'depth' };
      return;
    }
    if (bytes > limits.pathBytes) {
      found = { problem: 'path' };
      return;
    }
    if (typeof v === 'string') {
      if (utf8Length(v) > limits.stringBytes) found = { problem: 'string' };
      return;
    }
    if (v === null || typeof v !== 'object') return;
    const entries = Array.isArray(v) ? v.map((x, i) => [String(i), x] as const) : Object.entries(v);
    for (const [key, child] of entries) {
      if (PSEUDO_KEYS.has(key)) {
        // A server value's body ({".sv": "timestamp"}) is not data.
        if (key !== '.sv') walk(child, depth, bytes);
        continue;
      }
      if (checkKey(key, limits)) {
        found = { problem: 'key', key };
        return;
      }
      walk(child, depth + 1, bytes + utf8Length(key));
      if (found) return;
    }
  };
  walk(value, at.length, atBytes);
  if (found) return found;
  if (utf8Length(JSON.stringify({ value })) > limits.writeBytes) return { problem: 'size' };
  return { problem: null };
}

/** A leaf's value as the editor shows it: strings as typed, the rest as JSON. */
export function formatValueInput(value: JsonValue): string {
  if (typeof value === 'string') {
    // A string that would read back as something else ("5", "true", "\"x\"") keeps its quotes.
    return parseValueInput(value).value === value ? value : JSON.stringify(value);
  }
  return JSON.stringify(value);
}
