import { createHash } from 'node:crypto';
import type { Page, Route } from '@playwright/test';
import type { CorsRule, RuleIssue, StoredObject } from '../src/features/storage/api';

// The Cloud Storage part of the mock console API (/projects/{project}/storage/…), and the /v0
// upload session URLs the dashboard's uploads send their chunks to (ADR-022 §6.1, §15). One
// bucket per project, fresh per test.

// An 8×8 checkered PNG, so a preview shows a real image.
export const PNG = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAgAAAAICAIAAABLbSncAAAAGUlEQVR4nGN4kKVln3UCk2TAKgokGQalDgA3BFyhYLlH1gAAAABJRU5ErkJggg==', 'base64');

interface Stored {
  meta: StoredObject;
  bytes: Buffer;
}

interface Session {
  id: string;
  name: string;
  size: number;
  contentType?: string;
  parts: Buffer[];
  received: number;
  state: 'active' | 'final' | 'cancelled';
}

export interface V0Request {
  command: string;
  offset?: string;
  size: number;
  cookie?: string;
  csrf?: string;
  url: string;
}

export interface StorageStore {
  objects: Map<string, Stored>;
  rules: { published: boolean; source: string; updateTime?: string };
  cors: CorsRule[];
  sessions: Map<string, Session>;
  /** Every request to a /v0 session URL, with the headers the browser sent. */
  v0: V0Request[];
  /** The chunk requests (1-based, across sessions) that meet an unavailable server, once each. */
  failChunks: number[];
  chunks: number;
  issued: { name: string; size: number; contentType?: string }[];
}

type Reply = { status: number; body?: unknown; headers?: Record<string, string>; raw?: Buffer | string };
const ok = (body: unknown): Reply => ({ status: 200, body });
const fail = (status: number, rpc: string, message: string, details?: unknown[]): Reply => ({
  status,
  body: { error: { code: status, status: rpc, message, ...(details && { details }) } },
});
const notFound = () => fail(404, 'NOT_FOUND', 'No such object.');

const TIME = '2026-10-18T08:30:00.000Z';
let counter = 0;

function make(bucket: string, name: string, bytes: Buffer, extra: Partial<StoredObject> = {}): Stored {
  counter++;
  const token = `00000000-0000-4000-8000-${String(counter).padStart(12, '0')}`;
  return {
    bytes,
    meta: {
      name,
      bucket,
      size: bytes.length,
      metadata: {},
      downloadTokens: [token],
      downloadUrls: [],
      generation: String(1760777400000000 + counter),
      metageneration: '1',
      md5Hash: createHash('md5').update(bytes).digest('base64'),
      crc32c: 'AAAAAA==',
      timeCreated: TIME,
      updated: TIME,
      ...extra,
    },
  };
}

/** A stand-in for rules.Compile: an operator with nothing after it is an error, `if true` a warning. */
function checkRules(source: string): RuleIssue[] {
  const issues: RuleIssue[] = [];
  const lines = source.split('\n');
  let offset = 0;
  lines.forEach((line, i) => {
    const err = /(==|!=|&&|\|\|)\s*;/.exec(line);
    if (err) {
      const col = err.index + err[1]!.length + 1;
      issues.push({ description: "mismatched input ';' expecting an expression", severity: 'ERROR', sourcePosition: { fileName: 'storage.rules', line: i + 1, column: col, currentOffset: offset + col - 1, endOffset: offset + col } });
    }
    const warn = /if true/.exec(line);
    if (warn) {
      issues.push({ description: 'This condition is always true.', severity: 'WARNING', sourcePosition: { fileName: 'storage.rules', line: i + 1, column: warn.index + 4, currentOffset: offset + warn.index + 3, endOffset: offset + warn.index + 7 } });
    }
    offset += [...line].length + 1;
  });
  return issues;
}

export class MockStorage {
  private readonly stores = new Map<string, StorageStore>();
  private origin = '';

  store(projectId: string): StorageStore {
    let s = this.stores.get(projectId);
    if (!s) {
      const bucket = `${projectId}.console.example.com`;
      const seeded = [
        make(bucket, 'photos/cat.png', PNG, { contentType: 'image/png', metadata: { owner: 'amal' } }),
        make(bucket, 'photos/2026/trip.jpg', Buffer.alloc(1500, 1), { contentType: 'image/jpeg' }),
        make(bucket, 'readme.txt', Buffer.from('مرحبًا\n'), { contentType: 'text/plain; charset=utf-8', cacheControl: 'public, max-age=60' }),
        make(bucket, 'reports/q3.pdf', Buffer.alloc(52_000, 2), { contentType: 'application/pdf', downloadTokens: [] }),
      ];
      s = {
        objects: new Map(seeded.map((o) => [o.meta.name, o])),
        rules: { published: false, source: '' },
        cors: [],
        sessions: new Map(),
        v0: [],
        failChunks: [],
        chunks: 0,
        issued: [],
      };
      this.stores.set(projectId, s);
    }
    return s;
  }

  /** The object as the console API answers it, with its download links. */
  private json(o: Stored): StoredObject {
    const m = o.meta;
    const base = `${this.origin}/v0/b/${encodeURIComponent(m.bucket)}/o/${encodeURIComponent(m.name)}?alt=media&token=`;
    return { ...m, downloadUrls: m.downloadTokens.map((t) => base + t) };
  }

  /** Answers the /v0 session URLs on this page's origin. */
  async install(page: Page, origin: string): Promise<void> {
    this.origin = origin;
    await page.route('**/v0/b/**', (route) => this.v0(route));
  }

  dispatch(projectId: string, method: string, sub: string[], q: URLSearchParams, body: Record<string, unknown>): Reply {
    const s = this.store(projectId);
    const bucket = `${projectId}.console.example.com`;
    const [kind] = sub;
    const name = q.get('name') ?? '';
    const route = `${method} ${kind ?? ''}`;
    if (route === 'GET ') {
      return ok({
        bucket,
        enabled: true,
        limits: { objectBytes: 5368709120, projectBytes: 1073741824, metadataBytes: 8192, headerFieldBytes: 1024, listMaxResults: 1000, uploadSessionSeconds: 3600, requestBytes: 1048576 },
      });
    }
    if (route === 'GET objects') {
      const prefix = q.get('prefix') ?? '';
      const prefixes = new Set<string>();
      const items: StoredObject[] = [];
      for (const o of [...s.objects.values()].sort((a, b) => (a.meta.name < b.meta.name ? -1 : 1))) {
        if (!o.meta.name.startsWith(prefix)) continue;
        const rest = o.meta.name.slice(prefix.length);
        const slash = rest.indexOf('/');
        if (slash >= 0) prefixes.add(prefix + rest.slice(0, slash + 1));
        else items.push(this.json(o));
      }
      return ok({ prefixes: [...prefixes], items });
    }
    const o = s.objects.get(name);
    if (kind === 'object' || kind?.startsWith('object:')) {
      if (!name) return fail(400, 'INVALID_ARGUMENT', "The object's name is required.");
      if (!o) return notFound();
    }
    switch (route) {
      case 'GET object':
        return ok(this.json(o!));
      case 'PATCH object': {
        for (const [k, v] of Object.entries(body)) {
          if (k === 'metadata') {
            const md = v as Record<string, string | null> | null;
            if (md === null) o!.meta.metadata = {};
            else for (const [mk, mv] of Object.entries(md)) if (mv === null) delete o!.meta.metadata[mk]; else o!.meta.metadata[mk] = mv;
          } else if (v === null) delete (o!.meta as unknown as Record<string, unknown>)[k];
          else (o!.meta as unknown as Record<string, unknown>)[k] = v;
        }
        o!.meta.metageneration = String(Number(o!.meta.metageneration) + 1);
        return ok(this.json(o!));
      }
      case 'DELETE object':
        s.objects.delete(name);
        return ok({});
      case 'GET object:download':
        return { status: 200, raw: o!.bytes, headers: { 'Content-Type': o!.meta.contentType ?? 'application/octet-stream', 'Content-Disposition': `attachment; filename*=utf-8''${encodeURIComponent(name.split('/').pop()!)}`, 'X-Content-Type-Options': 'nosniff' } };
      case 'POST object:createToken':
        o!.meta.downloadTokens.push(`11111111-0000-4000-8000-${String(++counter).padStart(12, '0')}`);
        return ok(this.json(o!));
      case 'POST object:deleteToken': {
        o!.meta.downloadTokens = o!.meta.downloadTokens.filter((t) => t !== body.token);
        if (o!.meta.downloadTokens.length === 0) o!.meta.downloadTokens.push(`22222222-0000-4000-8000-${String(++counter).padStart(12, '0')}`);
        return ok(this.json(o!));
      }
      case 'POST uploads': {
        if (typeof body.size !== 'number') return fail(400, 'INVALID_ARGUMENT', "The upload's size is required.");
        const id = `session-${++counter}`;
        const upName = String(body.name);
        s.issued.push({ name: upName, size: body.size, contentType: body.contentType as string | undefined });
        s.sessions.set(id, { id, name: upName, size: body.size, contentType: body.contentType as string | undefined, parts: [], received: 0, state: 'active' });
        return ok({
          uploadUrl: `${this.origin}/v0/b/${encodeURIComponent(bucket)}/o?name=${encodeURIComponent(upName)}&upload_id=${id}&upload_protocol=resumable`,
          bucket,
          name: upName,
          size: body.size,
          startBy: '2026-10-18T09:10:00.000Z',
          expireTime: '2026-10-18T10:00:00.000Z',
        });
      }
      case 'GET usage': {
        const used = [...s.objects.values()].reduce((n, x) => n + x.bytes.length, 0);
        return ok({ usedBytes: used, reservedBytes: 0, objects: s.objects.size, quotaBytes: 1073741824 });
      }
      case 'GET cors':
        return ok({ cors: s.cors });
      case 'PUT cors':
        s.cors = (body.cors as CorsRule[]) ?? [];
        return ok({ cors: s.cors });
      case 'GET rules':
        return ok(s.rules.published ? { ...s.rules, rulesetName: 'projects/chat-app/rulesets/r1' } : { published: false, source: '' });
      case 'POST rules:validate': {
        const issues = checkRules(String(body.source ?? ''));
        return ok({ valid: !issues.some((i) => i.severity === 'ERROR'), issues });
      }
      case 'PUT rules': {
        const source = String(body.source ?? '');
        const issues = checkRules(source);
        if (issues.some((i) => i.severity === 'ERROR')) {
          return fail(400, 'INVALID_ARGUMENT', 'The rules do not compile.', [{ '@type': 'type.aruma.dev/storage.RulesIssues', issues }]);
        }
        s.rules = { published: true, source, updateTime: '2026-10-18T09:00:00.000Z' };
        return ok({ ...s.rules, issues });
      }
    }
    return notFound();
  }

  /** A /v0 session URL: chunks at the offset kept, query, cancel, and the finalize. */
  private async v0(route: Route): Promise<void> {
    const req = route.request();
    const url = new URL(req.url());
    const headers = await req.allHeaders();
    const body = req.postDataBuffer() ?? Buffer.alloc(0);
    const id = url.searchParams.get('upload_id') ?? '';
    const s = [...this.stores.values()].find((x) => x.sessions.has(id));
    const command = headers['x-goog-upload-command'] ?? '';
    s?.v0.push({ command, offset: headers['x-goog-upload-offset'], size: body.length, cookie: headers.cookie, csrf: headers['x-aruma-csrf'], url: req.url() });
    const session = s?.sessions.get(id);
    if (!s || !session) return route.fulfill({ status: 404, contentType: 'application/json', body: '{"error":{"code":404,"message":"Not Found."}}' });
    const reply = (state: string, status = 200, json?: unknown) =>
      route.fulfill({
        status,
        headers: { 'X-Goog-Upload-Status': state, 'X-Goog-Upload-Size-Received': String(session.received), ...(json !== undefined && { 'Content-Type': 'application/json' }) },
        body: json === undefined ? '' : JSON.stringify(json),
      });
    if (command === 'query') return reply(session.state === 'final' ? 'final' : 'active');
    if (command === 'cancel') {
      session.state = 'cancelled';
      return reply('cancelled');
    }
    const offset = Number(headers['x-goog-upload-offset']);
    s.chunks++;
    if (s.failChunks.includes(s.chunks)) return reply('active', 503);
    if (offset !== session.received) return reply('active', 400);
    session.parts.push(body);
    session.received += body.length;
    if (!command.includes('finalize')) return reply('active');
    if (session.received !== session.size) return reply('final', 400);
    session.state = 'final';
    const bucket = decodeURIComponent(url.pathname.split('/')[3] ?? '');
    const stored = make(bucket, session.name, Buffer.concat(session.parts), session.contentType ? { contentType: session.contentType } : {});
    s.objects.set(session.name, stored);
    return reply('final', 200, { name: session.name, bucket, size: String(stored.bytes.length) });
  }
}
