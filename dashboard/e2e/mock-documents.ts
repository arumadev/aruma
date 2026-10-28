import type { Doc, Fields, Value } from '../src/features/documents/api';

// The Document Database part of the mock console API (/projects/{project}/documents/…, ADR-030):
// documents by path in memory, collections derived from them, missing documents (ids with
// subcollections and no document) listed in place, CreateDocument's ALREADY_EXISTS, an edit's
// update mask and updateTime precondition, and a recursive delete that takes several steps.
// One database per project, fresh per test.

interface Stored {
  fields: Fields;
  createTime: string;
  updateTime: string;
}

export interface DocumentsStore {
  docs: Map<string, Stored>;
  /** A collection's page of documents. */
  pageSize: number;
  /** The documents one recursive delete step removes. */
  deleteStep: number;
  /** Bumps a document's updateTime, as another writer would. */
  touch: (path: string) => void;
}

type Reply = { status: number; body?: unknown };
const ok = (body: unknown): Reply => ({ status: 200, body });
const fail = (status: number, rpc: string, message: string): Reply => ({ status, body: { error: { code: status, status: rpc, message } } });

const DB = (p: string) => `projects/${p}/databases/(default)`;

let clock = 0;
const nextTime = () => new Date(Date.UTC(2026, 9, 18, 8, 0, 0) + ++clock * 1000).toISOString().replace('.000Z', '.123456Z');

function seed(projectId: string): Map<string, Stored> {
  const docs = new Map<string, Stored>();
  const put = (path: string, fields: Fields) => {
    const t = nextTime();
    docs.set(path, { fields, createTime: t, updateTime: t });
  };
  put('users/alice', {
    name: { stringValue: 'Alice' },
    age: { integerValue: '9223372036854775807' },
    score: { doubleValue: 'NaN' },
    ratio: { doubleValue: 1.5 },
    active: { booleanValue: true },
    nothing: { nullValue: null },
    joined: { timestampValue: '2026-10-17T14:56:25.123456789Z' },
    home: { geoPointValue: { latitude: 24.5, longitude: 0 } },
    friend: { referenceValue: `${DB(projectId)}/documents/users/bob` },
    tags: { arrayValue: { values: [{ stringValue: 'a' }, { integerValue: '2' }] } },
    address: { mapValue: { fields: { city: { stringValue: 'الرياض' }, zip: { stringValue: '12345' } } } },
    avatar: { bytesValue: 'AAECAw==' },
  });
  put('users/bob', { name: { stringValue: 'Bob' } });
  put('users/ghost/posts/p1', { title: { stringValue: 'hi' } });
  put('users/ghost/posts/p2', { title: { stringValue: 'there' } });
  put('rooms/r1', { title: { stringValue: 'مرحبا 👋 café 你好' } });
  return docs;
}

const ids = (path: string) => (path === '' ? [] : path.split('/'));

/** A field path of an update mask, `a`.`b` or a.b, as its segments. */
function parseFieldPath(s: string): string[] {
  const out: string[] = [];
  let i = 0;
  while (i < s.length) {
    let seg = '';
    if (s[i] === '`') {
      i++;
      while (i < s.length && s[i] !== '`') {
        if (s[i] === '\\') i++;
        seg += s[i++];
      }
      i++;
    } else {
      while (i < s.length && s[i] !== '.') seg += s[i++];
    }
    out.push(seg);
    if (s[i] === '.') i++;
  }
  return out;
}

function getAt(fields: Fields, path: string[]): Value | undefined {
  let cur: Value | undefined = { mapValue: { fields } };
  for (const p of path) cur = cur && 'mapValue' in cur ? cur.mapValue.fields?.[p] : undefined;
  return cur;
}

function setAt(fields: Fields, path: string[], v: Value | undefined): void {
  const [first, ...rest] = path;
  if (first === undefined) return;
  if (rest.length === 0) {
    if (v === undefined) delete fields[first];
    else fields[first] = v;
    return;
  }
  let child = fields[first];
  if (!child || !('mapValue' in child)) {
    if (v === undefined) return;
    child = { mapValue: { fields: {} } };
    fields[first] = child;
  }
  child.mapValue.fields ??= {};
  setAt(child.mapValue.fields, rest, v);
}

export class MockDocuments {
  private stores = new Map<string, DocumentsStore>();

  store(projectId: string): DocumentsStore {
    let s = this.stores.get(projectId);
    if (!s) {
      const docs = seed(projectId);
      s = {
        docs,
        pageSize: 50,
        deleteStep: 5000,
        touch: (path) => {
          const d = docs.get(path);
          if (d) d.updateTime = nextTime();
        },
      };
      this.stores.set(projectId, s);
    }
    return s;
  }

  dispatch(projectId: string, method: string, sub: string[], q: URLSearchParams, body: Record<string, unknown>): Reply {
    const s = this.store(projectId);
    const root = `${DB(projectId)}/documents`;
    const route = `${method} ${sub[0] ?? ''}`;
    const path = (q.get('path') ?? '').replace(/^\/+|\/+$/g, '');
    const json = (p: string, d?: Stored): Doc => ({
      name: `${root}/${p}`,
      path: p,
      id: ids(p).at(-1) ?? '',
      missing: !d,
      ...(d && { fields: structuredClone(d.fields), createTime: d.createTime, updateTime: d.updateTime }),
    });

    if (route === 'GET ') {
      return ok({
        enabled: true,
        databaseId: '(default)',
        name: DB(projectId),
        limits: { pageSize: s.pageSize, collectionsPageSize: 300, maxPageSize: 1000, documentBytes: 1048576, fieldDepth: 20, requestBytes: 1048576, recursiveDeleteDocs: s.deleteStep },
      });
    }
    if (route === 'GET usage') return ok({ available: false, counters: {}, note: 'This server does not count reads, writes or deletes per project yet.' });

    if (route === 'GET collections') {
      const depth = ids(path).length;
      const found = new Set<string>();
      for (const p of s.docs.keys()) {
        const parts = ids(p);
        if (parts.length > depth && (path === '' || p.startsWith(`${path}/`))) found.add(parts[depth]!);
      }
      return ok({ collectionIds: [...found].sort() });
    }

    if (route === 'GET documents') {
      const depth = ids(path).length;
      const found = new Map<string, Stored | undefined>();
      for (const [p, d] of s.docs) {
        if (!p.startsWith(`${path}/`)) continue;
        const parts = ids(p);
        const docPath = parts.slice(0, depth + 1).join('/');
        if (parts.length === depth + 1) found.set(docPath, d);
        else if (!found.has(docPath)) found.set(docPath, s.docs.get(docPath));
      }
      const all = [...found.keys()].sort();
      const start = Number(q.get('pageToken') ?? 0);
      const size = Number(q.get('pageSize') ?? s.pageSize);
      const page = all.slice(start, start + size);
      const out = page.map((p) => {
        const j = json(p, found.get(p));
        if (q.get('fields') === 'false' && !j.missing) j.fields = {};
        return j;
      });
      return ok({ documents: out, ...(start + size < all.length && { nextPageToken: String(start + size) }) });
    }

    if (route === 'POST documents') {
      const id = String(body.documentId ?? '') || `auto${String(++clock).padStart(16, '0')}`;
      const p = `${path}/${id}`;
      if (s.docs.has(p)) return fail(409, 'ALREADY_EXISTS', `Document already exists: ${root}/${p}`);
      const t = nextTime();
      s.docs.set(p, { fields: (body.fields as Fields) ?? {}, createTime: t, updateTime: t });
      return ok(json(p, s.docs.get(p)));
    }

    if (route === 'POST documents:recursiveDelete') {
      const target = String(body.path ?? '');
      const under = [...s.docs.keys()].filter((p) => p === target || p.startsWith(`${target}/`)).sort();
      const now = under.slice(0, s.deleteStep);
      for (const p of now) s.docs.delete(p);
      return ok({ deleted: now.length, done: under.length <= s.deleteStep });
    }

    if (sub[0] === 'document') {
      const d = s.docs.get(path);
      if (method === 'GET') return d ? ok(json(path, d)) : fail(404, 'NOT_FOUND', `Document "${root}/${path}" not found.`);
      if (method === 'DELETE') {
        s.docs.delete(path);
        return ok({});
      }
      if (method === 'PATCH') {
        if (body.updateTime !== undefined && (!d || d.updateTime !== body.updateTime)) {
          return fail(400, 'FAILED_PRECONDITION', `the stored version (${d?.updateTime}) does not match the required base version (${String(body.updateTime)})`);
        }
        if (body.exists === true && !d) return fail(404, 'NOT_FOUND', `No document to update: ${root}/${path}`);
        const fields = (body.fields as Fields) ?? {};
        const next: Fields = d && body.updateMask ? structuredClone(d.fields) : {};
        if (body.updateMask) {
          for (const m of body.updateMask as string[]) {
            const segs = parseFieldPath(m);
            setAt(next, segs, getAt(fields, segs));
          }
        } else Object.assign(next, fields);
        const t = nextTime();
        s.docs.set(path, { fields: next, createTime: d?.createTime ?? t, updateTime: t });
        return ok(json(path, s.docs.get(path)));
      }
    }
    return fail(404, 'NOT_FOUND', 'No such route.');
  }
}
