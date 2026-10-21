import type { DataChild, JsonValue, RuleError } from '../src/lib/api/types';

// The Realtime Database part of the mock console API: /projects/{project}/database/…
// (docs/api/console.md). One in-memory tree and rules document per project, fresh per test.

type Tree = { [key: string]: JsonValue };

export interface DatabaseStore {
  data: Tree;
  rules: string;
  calls: { method: string; path: string; query: string; body: unknown }[];
}

const LOCKED = '{"rules": {".read": false, ".write": false}}';

export function seedDatabase(): DatabaseStore {
  const messages: Tree = {};
  for (let i = 0; i < 60; i++) {
    messages[`-NkA${String(i).padStart(3, '0')}`] = { from: i % 2 ? 'amal' : 'omar', text: `رسالة ${i + 1}`, at: 1760000000000 + i * 60000 };
  }
  return {
    data: {
      config: { motd: 'مرحبًا بكم', version: 2, maintenance: false },
      messages,
      rooms: {
        general: { title: 'عام', members: 3, open: true },
        random: { title: 'Random', members: 1, open: false },
      },
    },
    rules: LOCKED,
    calls: [],
  };
}

type Reply = { status: number; body?: unknown; headers?: Record<string, string>; raw?: string };
const ok = (body: unknown): Reply => ({ status: 200, body });
const empty = (): Reply => ({ status: 204 });
const fail = (status: number, rpc: string, message: string, details?: unknown[]): Reply => ({
  status,
  body: { error: { code: status, status: rpc, message, ...(details && { details }) } },
});
const notFound = () => fail(404, 'NOT_FOUND', 'Requested entity was not found.');

const keysOf = (path: string) => path.split('/').filter(Boolean);
const isObject = (v: JsonValue | undefined): v is Tree => v !== null && typeof v === 'object' && !Array.isArray(v);

// Integer keys first by value, then the rest by code unit, as clients order them.
const INT = /^-?(0|[1-9]\d{0,9})$/;
function compareKeys(a: string, b: string): number {
  const ai = INT.test(a);
  const bi = INT.test(b);
  if (ai && bi) return Number(a) - Number(b);
  if (ai !== bi) return ai ? -1 : 1;
  return a < b ? -1 : a > b ? 1 : 0;
}

function get(root: Tree, keys: string[]): JsonValue | undefined {
  let node: JsonValue | undefined = root;
  for (const k of keys) {
    if (!isObject(node)) return undefined;
    node = node[k];
  }
  return node;
}

function set(root: Tree, keys: string[], value: JsonValue | null): Tree {
  if (keys.length === 0) return isObject(value) ? value : {};
  const [first, ...rest] = keys as [string, ...string[]];
  const child = root[first];
  const next = rest.length === 0 ? value : set(isObject(child) ? { ...child } : {}, rest, value);
  const out = { ...root };
  if (next === null || (isObject(next) && Object.keys(next).length === 0)) delete out[first];
  else out[first] = next;
  return out;
}

const FORBIDDEN = /[.#$[\]/\u0000-\u001f\u007f]/;

function validKeys(v: JsonValue): boolean {
  if (!isObject(v)) return true;
  return Object.entries(v).every(([k, c]) => (k === '.sv' || (k !== '' && !FORBIDDEN.test(k))) && validKeys(c));
}

function lineColumn(text: string, offset: number): { line: number; column: number } {
  const before = text.slice(0, offset).split('\n');
  return { line: before.length, column: (before.at(-1)?.length ?? 0) + 1 };
}

/** A stand-in for rules.Parse: JSON errors, and expressions that end in an operator. */
function checkRules(text: string): RuleError[] {
  const stripped = text.replace(/\/\/.*$/gm, '');
  try {
    JSON.parse(stripped);
  } catch (err) {
    const at = /position (\d+)/.exec(String(err));
    return [{ ...lineColumn(text, at ? Number(at[1]) : 0), message: 'Unexpected token.' }];
  }
  const errors: RuleError[] = [];
  for (const m of text.matchAll(/"(\.read|\.write|\.validate)"\s*:\s*"([^"]*)"/g)) {
    if (/(==|!=|&&|\|\||[<>+\-!])\s*$/.test(m[2] ?? '')) {
      const offset = (m.index ?? 0) + m[0].lastIndexOf('"' + m[2]) + 1 + (m[2]?.length ?? 0);
      errors.push({ ...lineColumn(text, offset), path: `rules/${m[1]}`, message: 'Unexpected end of expression.' });
    }
  }
  return errors;
}

export class MockDatabase {
  private readonly stores = new Map<string, DatabaseStore>();

  store(projectId: string): DatabaseStore {
    let s = this.stores.get(projectId);
    if (!s) {
      s = seedDatabase();
      this.stores.set(projectId, s);
    }
    return s;
  }

  dispatch(projectId: string, method: string, sub: string[], q: URLSearchParams, body: Record<string, unknown>): Reply {
    const s = this.store(projectId);
    const [kind] = sub;
    s.calls.push({ method, path: `/${sub.join('/')}`, query: q.toString(), body });
    if (kind === undefined) {
      const rows = JSON.stringify(s.data).split(/[,{]/).length;
      return ok({
        name: `${projectId}-default-rtdb`,
        databaseURL: `https://${projectId}-default-rtdb.rtdb.console.example.com`,
        enabled: true,
        devOpenRules: false,
        writeCount: 12,
        size: { rows, bytes: JSON.stringify(s.data).length, capped: false },
        limits: { keyBytes: 768, pathBytes: 768, depth: 32, stringBytes: 10485760, writeBytes: 1048576, exportBytes: 16777216 },
      });
    }
    if (kind === 'rules') {
      if (method === 'GET') return ok({ rules: s.rules });
      const rules = String(body.rules ?? '');
      const errors = checkRules(rules);
      if (errors.length > 0) {
        const first = errors[0]!;
        return fail(400, 'INVALID_ARGUMENT', `The rules are not valid: ${first.line}:${first.column}: ${first.message}`, [
          { '@type': 'type.googleapis.com/google.rpc.BadRequest', fieldViolations: [{ field: 'rules', description: first.message }] },
          { '@type': 'type.aruma.dev/rtdb.RulesErrors', errors },
        ]);
      }
      s.rules = rules;
      return ok({ rules });
    }
    const keys = keysOf(q.get('path') ?? '/');
    if (kind === 'data:export') {
      return { status: 200, raw: JSON.stringify(get(s.data, keys) ?? null), headers: { 'Content-Type': 'application/json', 'Content-Disposition': 'attachment; filename=export.json' } };
    }
    if (kind !== 'data') return notFound();
    if (keys.some((k) => FORBIDDEN.test(k))) return fail(400, 'INVALID_ARGUMENT', 'Invalid token in path');
    if (method === 'GET') {
      const node = get(s.data, keys);
      if (node !== undefined && !isObject(node)) {
        return ok({ path: `/${keys.join('/')}`, exists: true, value: node, children: [], nextPageToken: '' });
      }
      const all = isObject(node) ? Object.keys(node).sort(compareKeys) : [];
      const size = Number(q.get('pageSize') ?? 100);
      const after = q.get('pageToken') ? atob(q.get('pageToken')!) : '';
      const rest = after ? all.filter((k) => compareKeys(k, after) > 0) : all;
      const page = rest.slice(0, size);
      const children: DataChild[] = page.map((key) => {
        const v = (node as Tree)[key];
        return isObject(v) ? { key, hasChildren: true } : { key, value: v as string | number | boolean };
      });
      return ok({ path: `/${keys.join('/')}`, exists: all.length > 0, children, nextPageToken: rest.length > size ? btoa(page.at(-1)!) : '' });
    }
    if (method === 'DELETE') {
      s.data = set(s.data, keys, null);
      return empty();
    }
    const value = body.value as JsonValue;
    if (!validKeys(value)) return fail(400, 'INVALID_ARGUMENT', "Invalid data; Key value can't be empty or contain $ # [ ] / or .");
    if (method === 'PUT') {
      s.data = set(s.data, keys, value);
      return empty();
    }
    if (method === 'PATCH' && isObject(value)) {
      for (const [rel, v] of Object.entries(value)) s.data = set(s.data, [...keys, ...keysOf(rel)], v);
      return empty();
    }
    return notFound();
  }
}
