import { describe, expect, it, vi } from 'vitest';
import type { IssuedSession, UploadRequest } from './api';
import { backoff, BASE_CHUNK, MAX_CHUNK, MAX_FAILURES, resumableUpload, sessionTarget, UploadError } from './upload';

const ORIGIN = 'http://127.0.0.1:9360';

/** The bytes a session kept, in the parts they came in. */
class Kept {
  private parts: Uint8Array[] = [];
  length = 0;

  push(part: Uint8Array) {
    this.parts.push(part);
    this.length += part.length;
  }

  bytes(): Uint8Array {
    const out = new Uint8Array(this.length);
    let at = 0;
    for (const p of this.parts) {
      out.set(p, at);
      at += p.length;
    }
    return out;
  }
}

// jsdom's Blob has no arrayBuffer(); FileReader reads it.
function readBlob(blob: Blob): Promise<ArrayBuffer> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(reader.result as ArrayBuffer);
    reader.onerror = () => reject(reader.error);
    reader.readAsArrayBuffer(blob);
  });
}
const NOW = Date.parse('2026-10-24T10:00:00Z');

/**
 * A /v0 session as the server keeps it (ADR-022 §6.1): chunks at the offset it has, a gap
 * answered 400 with active and the size kept, a query, a finalize that answers the object.
 * `fail` makes the next requests fail as listed.
 */
class FakeSession {
  received = new Kept();
  requests: { command: string; offset?: string; size: number; credentials?: RequestCredentials; csrf?: string; url: string }[] = [];
  fail: (number | 'network' | 'cut' | undefined)[] = [];
  cancelled = false;
  known = new Set(['s1']);

  readonly size: number;

  constructor(size: number) {
    this.size = size;
  }

  fetch = vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}): Promise<Response> => {
    const url = String(input);
    const headers = init.headers as Record<string, string>;
    const body = init.body ? new Uint8Array(await readBlob(init.body as Blob)) : new Uint8Array();
    const command = headers['X-Goog-Upload-Command'] ?? '';
    this.requests.push({ command, offset: headers['X-Goog-Upload-Offset'], size: body.length, credentials: init.credentials, csrf: headers['X-Aruma-CSRF'], url });
    const id = new URL(url, ORIGIN).searchParams.get('upload_id') ?? '';
    if (!this.known.has(id)) return new Response('{"error":{"code":404,"message":"Not Found."}}', { status: 404 });
    const status = (s: string, code = 200, extra: Record<string, string> = {}, text: string | null = null) =>
      new Response(text, { status: code, headers: { 'X-Goog-Upload-Status': s, 'X-Goog-Upload-Size-Received': String(this.received.length), ...extra } });
    if (command === 'cancel') {
      this.cancelled = true;
      return status('cancelled');
    }
    if (command === 'query') {
      const f = this.fail.shift();
      if (f === 'network') throw new TypeError('Failed to fetch');
      if (typeof f === 'number') return status('active', f);
      return this.received.length === this.size && this.finished ? status('final', 200, {}, '{"name":"done"}') : status('active');
    }
    const f = this.fail.shift();
    if (f === 'network') throw new TypeError('Failed to fetch');
    const offset = Number(headers['X-Goog-Upload-Offset']);
    if (f === 'cut') {
      // Half the chunk arrived before the connection broke.
      if (offset === this.received.length) this.received.push(body.slice(0, body.length >> 1));
      throw new TypeError('Failed to fetch');
    }
    if (typeof f === 'number') return status('active', f);
    if (offset !== this.received.length) return status('active', 400);
    this.received.push(body);
    if (command.includes('finalize')) {
      if (this.received.length !== this.size) return status('final', 400);
      this.finished = true;
      return status('final', 200, {}, '{"name":"done"}');
    }
    return status('active');
  });

  finished = false;
}

/** A session issued at `at` on the server's clock, which its Date header says. */
function session(id = 's1', at = NOW): IssuedSession {
  return {
    uploadUrl: `${ORIGIN}/v0/b/chat-app.example.com/o?name=big.bin&upload_id=${id}&upload_protocol=resumable`,
    bucket: 'chat-app.example.com',
    name: 'big.bin',
    size: 0,
    startBy: new Date(at + 10 * 60_000).toISOString(),
    expireTime: new Date(at + 60 * 60_000).toISOString(),
    serverTime: at,
  };
}

const bytes = (n: number) => new Blob([Uint8Array.from({ length: n }, (_, i) => i % 251)]);

function run(server: FakeSession, file: Blob, opts: { issue?: (req: UploadRequest) => Promise<IssuedSession>; now?: () => number; signal?: AbortSignal } = {}) {
  const progress: number[] = [];
  const issue = vi.fn(opts.issue ?? (async () => session()));
  const sleep = vi.fn(async () => undefined);
  const promise = resumableUpload({
    name: 'big.bin',
    file,
    contentType: 'application/octet-stream',
    signal: opts.signal ?? new AbortController().signal,
    onProgress: (n) => progress.push(n),
    issue: (req) => issue(req),
    fetch: server.fetch as unknown as typeof fetch,
    now: opts.now ?? (() => NOW),
    sleep,
    origin: ORIGIN,
  });
  return { promise, progress, issue, sleep };
}

describe('resumableUpload', () => {
  it('sends growing chunks to the session URL without the cookie, and finalizes with the last', async () => {
    const size = 5 * 1024 * 1024 + 123;
    const server = new FakeSession(size);
    const { promise, progress, issue } = run(server, bytes(size));
    await expect(promise).resolves.toEqual({ name: 'done' });
    expect(issue).toHaveBeenCalledWith({ name: 'big.bin', size, contentType: 'application/octet-stream' });
    expect(server.requests.map((r) => r.size)).toEqual([BASE_CHUNK, 2 * BASE_CHUNK, 4 * BASE_CHUNK, 8 * BASE_CHUNK, size - 15 * BASE_CHUNK]);
    expect(server.requests.map((r) => r.command)).toEqual(['upload', 'upload', 'upload', 'upload', 'upload, finalize']);
    expect(server.requests.map((r) => r.offset)).toEqual(['0', String(BASE_CHUNK), String(3 * BASE_CHUNK), String(7 * BASE_CHUNK), String(15 * BASE_CHUNK)]);
    expect(server.requests.every((r) => r.credentials === 'omit' && r.csrf === undefined)).toBe(true);
    expect(server.requests.every((r) => r.url.startsWith(`${ORIGIN}/v0/b/`))).toBe(true);
    expect(progress).toEqual([BASE_CHUNK, 3 * BASE_CHUNK, 7 * BASE_CHUNK, 15 * BASE_CHUNK]);
    expect(server.received.length).toBe(size);
  });

  it('stops doubling at the largest chunk', async () => {
    const size = 50 * 1024 * 1024;
    const server = new FakeSession(size);
    await run(server, new Blob([new Uint8Array(size)])).promise;
    expect(server.requests.map((r) => r.size / BASE_CHUNK)).toEqual([1, 2, 4, 8, 16, 32, 64, 64, 9]);
    expect(MAX_CHUNK).toBe(64 * BASE_CHUNK);
  }, 20_000);

  it('sends an empty file as one finalize', async () => {
    const server = new FakeSession(0);
    await expect(run(server, bytes(0)).promise).resolves.toEqual({ name: 'done' });
    expect(server.requests).toMatchObject([{ command: 'upload, finalize', offset: '0', size: 0 }]);
  });

  it('resumes from what the server kept after a cut connection', async () => {
    const size = 3 * BASE_CHUNK;
    const server = new FakeSession(size);
    // The second chunk breaks halfway through.
    server.fail = [undefined, 'cut'];
    const { promise, progress, sleep } = run(server, bytes(size));
    await expect(promise).resolves.toEqual({ name: 'done' });
    expect(sleep).toHaveBeenCalledWith(backoff(1), expect.anything());
    const query = server.requests.findIndex((r) => r.command === 'query');
    expect(query).toBe(2);
    // After the query the upload goes on from the half chunk the server kept, with the base size.
    const kept = BASE_CHUNK + BASE_CHUNK;
    expect(progress).toContain(kept);
    expect(server.requests[query + 1]).toMatchObject({ offset: String(kept), size: BASE_CHUNK });
    expect(server.received.bytes()).toEqual(new Uint8Array(await readBlob(bytes(size))));
  });

  it('retries a 503 and gives up after too many failures', async () => {
    const size = BASE_CHUNK;
    const server = new FakeSession(size);
    server.fail = [503, 503];
    await expect(run(server, bytes(size)).promise).resolves.toEqual({ name: 'done' });

    const down = new FakeSession(size);
    down.fail = Array.from({ length: 20 }, () => 'network' as const);
    const { promise, sleep } = run(down, bytes(size));
    await expect(promise).rejects.toMatchObject({ kind: 'failed' });
    expect(sleep).toHaveBeenCalledTimes(MAX_FAILURES);
  });

  it('follows the size the server reports on a gap', async () => {
    const size = 2 * BASE_CHUNK;
    const server = new FakeSession(size);
    // Bytes the server got from an earlier request the client never heard back from.
    server.received.push(new Uint8Array(await readBlob(bytes(1000))));
    const { promise, progress } = run(server, bytes(size));
    await expect(promise).resolves.toEqual({ name: 'done' });
    expect(server.requests[0]).toMatchObject({ offset: '0' });
    expect(progress[0]).toBe(1000);
    expect(server.requests[1]).toMatchObject({ offset: '1000' });
  });

  it('stops on a refusal that ends the session', async () => {
    const server = new FakeSession(BASE_CHUNK);
    server.fetch.mockImplementationOnce(async () => new Response('{}', { status: 402, headers: { 'X-Goog-Upload-Status': 'final' } }));
    const { promise, sleep } = run(server, bytes(BASE_CHUNK));
    const err = await promise.catch((e: unknown) => e);
    expect(err).toBeInstanceOf(UploadError);
    expect(err).toMatchObject({ kind: 'refused', status: 402 });
    expect(sleep).not.toHaveBeenCalled();
  });

  it('issues a new session when the first expired before its first byte', async () => {
    const server = new FakeSession(BASE_CHUNK);
    server.known = new Set(['s2']);
    const sessions = [session('s1'), session('s2')];
    const { promise, issue } = run(server, bytes(BASE_CHUNK), { issue: async () => sessions.shift()! });
    await expect(promise).resolves.toEqual({ name: 'done' });
    expect(issue).toHaveBeenCalledTimes(2);

    // Past startBy (11 minutes after the session arrived, on this clock, which runs two hours
    // behind the server's), before any byte: a new session first, without trying the old one.
    const late = new FakeSession(BASE_CHUNK);
    late.known = new Set(['s2']);
    const skew = -2 * 3_600_000;
    let clock = NOW + skew;
    const again = [session('s1', NOW), session('s2', NOW + 11 * 60_000)];
    const second = run(late, bytes(BASE_CHUNK), {
      issue: async () => again.shift()!,
      now: () => {
        const t = clock;
        clock = NOW + skew + 11 * 60_000;
        return t;
      },
    });
    await expect(second.promise).resolves.toEqual({ name: 'done' });
    expect(late.requests.every((r) => r.url.includes('upload_id=s2'))).toBe(true);
  });

  it.each([
    ['an hour ahead', 61 * 60_000],
    ['a day ahead', 24 * 3_600_000],
    ['an hour behind', -61 * 60_000],
  ])('uploads with a browser clock %s of the server’s', async (_, skew) => {
    // The deadlines are the server's, as durations from when its answer arrived: a skewed
    // browser clock neither expires a live session nor issues needless new ones.
    const server = new FakeSession(4 * BASE_CHUNK);
    let t = NOW + skew;
    const { promise, issue } = run(server, bytes(4 * BASE_CHUNK), { now: () => (t += 1000) });
    await expect(promise).resolves.toEqual({ name: 'done' });
    expect(issue).toHaveBeenCalledTimes(1);
  });

  it('without the server’s clock, lets the server decide when a session expired', async () => {
    // No Date header: no hint at all. The empty session the server dropped answers 404, and
    // another is issued.
    const server = new FakeSession(BASE_CHUNK);
    server.known = new Set(['s2']);
    const sessions = [{ ...session('s1'), serverTime: undefined }, { ...session('s2'), serverTime: undefined }];
    const { promise, issue } = run(server, bytes(BASE_CHUNK), { issue: async () => sessions.shift()!, now: () => NOW + 30 * 24 * 3_600_000 });
    await expect(promise).resolves.toEqual({ name: 'done' });
    expect(issue).toHaveBeenCalledTimes(2);
    expect(server.requests.map((r) => r.url.includes('upload_id=s1'))).toEqual([true, false]);
  });

  it('fails as expired on a lost session with bytes', async () => {
    // A session that had bytes and is gone cannot be resumed, nor replaced.
    const lost = new FakeSession(3 * BASE_CHUNK);
    const real = lost.fetch.getMockImplementation()!;
    lost.fetch.mockImplementation(async (input, init) => {
      const res = await real(input, init);
      lost.known.clear();
      return res;
    });
    const { promise, issue } = run(lost, bytes(3 * BASE_CHUNK));
    await expect(promise).rejects.toMatchObject({ kind: 'expired', status: 404 });
    expect(issue).toHaveBeenCalledTimes(1);
  });

  it('cancels the session when aborted', async () => {
    const server = new FakeSession(4 * BASE_CHUNK);
    const controller = new AbortController();
    const real = server.fetch.getMockImplementation()!;
    server.fetch.mockImplementation(async (input, init) => {
      const res = await real(input, init);
      if (server.requests.length === 2) controller.abort();
      return res;
    });
    const { promise } = run(server, bytes(4 * BASE_CHUNK), { signal: controller.signal });
    await expect(promise).rejects.toMatchObject({ kind: 'cancelled' });
    await vi.waitFor(() => expect(server.cancelled).toBe(true));
    expect(server.requests.at(-1)).toMatchObject({ command: 'cancel', credentials: 'omit' });
  });
});

describe('sessionTarget', () => {
  it('keeps a URL on this origin, and sends one on another name of the server to the same path here', () => {
    const url = 'http://127.0.0.1:9360/v0/b/a.example.com/o?name=x&upload_id=u&upload_protocol=resumable';
    expect(sessionTarget(url, 'http://127.0.0.1:9360')).toBe(url);
    expect(sessionTarget(url, 'https://console.example.com')).toBe('/v0/b/a.example.com/o?name=x&upload_id=u&upload_protocol=resumable');
  });
});
