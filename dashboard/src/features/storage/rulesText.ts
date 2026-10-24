// The Storage rules editor's helpers: the starting templates, the issues the server reports with
// their positions, and where those positions are in the editor's text.
import { ApiError } from '@/lib/api/client';
import type { RuleIssue } from './api';

/** A new project's rules, `firebase init storage`'s template: only admins reach the files. */
export const LOCKED_RULES = `rules_version = '2';

service firebase.storage {
  match /b/{bucket}/o {
    match /{allPaths=**} {
      allow read, write: if false;
    }
  }
}
`;

/** Every signed-in user reads and writes every file: a start for development. */
export const SIGNED_IN_RULES = `rules_version = '2';

service firebase.storage {
  match /b/{bucket}/o {
    match /{allPaths=**} {
      allow read, write: if request.auth != null;
    }
  }
}
`;

/** Each user reads and writes the files under users/<their uid>/, and nothing else. */
export const PER_USER_RULES = `rules_version = '2';

service firebase.storage {
  match /b/{bucket}/o {
    match /users/{uid}/{allPaths=**} {
      allow read, write: if request.auth != null && request.auth.uid == uid;
    }
  }
}
`;

/** The text without its // and /* *\/ comments, strings kept: comments are no rules. */
export function stripComments(text: string): string {
  return text.replace(/("(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*')|\/\/[^\n]*|\/\*[\s\S]*?(?:\*\/|$)/g, (m, str: string | undefined) => str ?? ' ');
}

/**
 * Whether a source lets anyone reach files without a condition: an `allow` with no `if`, or
 * with `if true`. A rough look at the text, for a warning; the server decides what it means.
 */
export function openAccess(text: string): boolean {
  return /\ballow\s+[a-z,\s]+(;|:\s*if\s+true\s*;)/.test(stripComments(text));
}

const RULES_ISSUES = 'type.aruma.dev/storage.RulesIssues';

/** The issues of a source the server refused to publish; empty for any other failure. */
export function refusedIssues(error: unknown): RuleIssue[] {
  if (!(error instanceof ApiError)) return [];
  for (const d of error.details) {
    if (d && typeof d === 'object' && (d as { '@type'?: string })['@type'] === RULES_ISSUES) {
      return (d as { issues?: RuleIssue[] }).issues ?? [];
    }
  }
  return [];
}

/** Errors first, then warnings, each in the order of the source. */
export function sortIssues(issues: RuleIssue[]): RuleIssue[] {
  const rank = (i: RuleIssue) => (i.severity === 'ERROR' ? 0 : 1);
  const at = (i: RuleIssue) => i.sourcePosition?.currentOffset ?? -1;
  return [...issues].sort((a, b) => rank(a) - rank(b) || at(a) - at(b));
}

/**
 * The editor's offset (UTF-16 code units, as a textarea counts) of a code point offset: the
 * server counts positions in code points.
 */
export function unitsOf(text: string, codePoints: number): number {
  let units = 0;
  let n = 0;
  for (const c of text) {
    if (n >= codePoints) break;
    units += c.length;
    n++;
  }
  return units;
}

/** The editor's offset of a 1-based line and code point column. */
export function offsetOf(text: string, line: number, column: number): number {
  let start = 0;
  for (let l = 1; l < line; l++) {
    const next = text.indexOf('\n', start);
    if (next < 0) return text.length;
    start = next + 1;
  }
  const end = text.indexOf('\n', start);
  const row = text.slice(start, end < 0 ? text.length : end);
  return start + unitsOf(row, Math.max(column - 1, 0));
}

/** The span an issue marks in the editor, from its position: [start, end) in UTF-16 units. */
export function issueRange(text: string, issue: RuleIssue): [number, number] | undefined {
  const p = issue.sourcePosition;
  if (!p) return undefined;
  const start = offsetOf(text, p.line, p.column);
  const end = p.endOffset > p.currentOffset ? start + (unitsOf(text, p.endOffset) - unitsOf(text, p.currentOffset)) : start;
  return [start, Math.min(end, text.length)];
}
