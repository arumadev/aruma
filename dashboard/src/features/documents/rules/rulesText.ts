// The Rules tab's helpers: the locked default, the issues the server reports, the Playground's
// request and the Authentication payload it shows, and the history's labels.
import { ApiError } from '@/lib/api/client';
import type { Fields, RuleIssue, SimulateAuth, SimulateMethod, Value } from '../api';

export { issueRange, openAccess, sortIssues } from '@/features/storage/rulesText';

/** What runs before anything is published: nothing is readable or writable by a client. */
export const LOCKED_RULES = `rules_version = '2';

service cloud.firestore {
  match /databases/{database}/documents {
    match /{document=**} {
      allow read, write: if false;
    }
  }
}
`;

const RULES_ISSUES = 'type.aruma.dev/documents.RulesIssues';

/** The issues of a source the server refused; empty for any other failure. */
export function refusedIssues(error: unknown): RuleIssue[] {
  if (!(error instanceof ApiError)) return [];
  for (const d of error.details) {
    if (d && typeof d === 'object' && (d as { '@type'?: string })['@type'] === RULES_ISSUES) {
      return (d as { issues?: RuleIssue[] }).issues ?? [];
    }
  }
  return [];
}

/** The Playground's simulation types; there is no list, as in the Firebase console. */
export const METHODS: SimulateMethod[] = ['get', 'create', 'update', 'delete'];

/** What the banner calls a method: a get is a "read". */
export const methodNoun = (m: SimulateMethod): 'read' | 'create' | 'update' | 'delete' => (m === 'get' ? 'read' : m);

export const PROVIDERS = ['google.com', 'password', 'phone', 'anonymous', 'apple.com', 'facebook.com', 'github.com', 'twitter.com', 'microsoft.com'];

/**
 * The document path a simulation names, below the documents root: ids separated by /, none
 * empty, and an even number of them, since a document sits in a collection.
 */
export function pathProblem(path: string): 'required' | 'empty' | 'odd' | undefined {
  if (path.trim() === '') return 'required';
  const parts = cleanPath(path).split('/');
  if (parts.some((p) => p === '')) return 'empty';
  if (parts.length % 2 !== 0) return 'odd';
  return undefined;
}

/** The path as the server takes it: trimmed of outer slashes. */
export const cleanPath = (path: string) => path.trim().replace(/^\/+|\/+$/g, '');

/** The request.auth the Playground shows as its "Authentication payload", as recorded. */
export function authPayload(auth: SimulateAuth, project: string): object {
  return {
    uid: auth.uid ?? '',
    token: {
      sub: auth.uid ?? '',
      aud: project,
      email: auth.email ?? '',
      email_verified: auth.emailVerified ?? false,
      phone_number: auth.phoneNumber ?? '',
      name: auth.name ?? '',
      firebase: { sign_in_provider: auth.provider || 'google.com' },
    },
  };
}

/** A JSON value in Firestore's REST form: whole numbers are integers, other numbers doubles. */
export function toValue(v: unknown): Value {
  if (v === null) return { nullValue: null };
  if (typeof v === 'boolean') return { booleanValue: v };
  if (typeof v === 'number') return Number.isSafeInteger(v) ? { integerValue: String(v) } : { doubleValue: v };
  if (typeof v === 'string') return { stringValue: v };
  if (Array.isArray(v)) return { arrayValue: { values: v.map(toValue) } };
  if (typeof v === 'object') return { mapValue: { fields: toFields(v as Record<string, unknown>) } };
  throw new Error('unsupported value');
}

export function toFields(o: Record<string, unknown>): Fields {
  return Object.fromEntries(Object.entries(o).map(([k, v]) => [k, toValue(v)]));
}

/** The data editor's text as fields: a JSON object, or a problem to show. */
export function parseData(text: string): { fields: Fields } | { problem: 'syntax' | 'object' } {
  const trimmed = text.trim();
  if (trimmed === '') return { fields: {} };
  let parsed: unknown;
  try {
    parsed = JSON.parse(trimmed);
  } catch {
    return { problem: 'syntax' };
  }
  if (parsed === null || typeof parsed !== 'object' || Array.isArray(parsed)) return { problem: 'object' };
  return { fields: toFields(parsed as Record<string, unknown>) };
}

export interface LineMark {
  severity: 'error' | 'warning';
  message: string;
}

/** What the gutter marks on each line: the error winning over a warning, messages joined. */
export function lineMarks(issues: RuleIssue[]): Map<number, LineMark> {
  const marks = new Map<number, LineMark>();
  for (const i of issues) {
    const line = i.sourcePosition?.line;
    if (!line) continue;
    const severity = i.severity === 'ERROR' ? 'error' : 'warning';
    const prev = marks.get(line);
    if (!prev) marks.set(line, { severity, message: i.description });
    else marks.set(line, { severity: prev.severity === 'error' ? 'error' : severity, message: `${prev.message}\n${i.description}` });
  }
  return marks;
}

/** The ruleset id, the last segment of projects/{p}/rulesets/{id}. */
export const rulesetId = (name: string) => name.split('/').pop() ?? name;
