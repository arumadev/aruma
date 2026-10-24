// The dashboard's uploads (ADR-022 §15): a small console request issues a resumable session
// under the owner's account, then the bytes go to that session's /v0 URL with the client
// protocol (X-Goog-Upload-*), in chunks that grow while they succeed, as the Firebase web SDK
// sends them. The chunks carry no cookie and no CSRF token: the URL is the session's
// credential, and the session cookie never rides a request of gigabytes.
import type { UploadRequest, UploadSession } from './api';

/** The first chunk, and the size a chunk falls back to after a failure: the web SDK's 256 KiB. */
export const BASE_CHUNK = 256 * 1024;
/** The largest chunk; the web SDK stops doubling at 32 MiB, a tab holds less in memory here. */
export const MAX_CHUNK = 16 * 1024 * 1024;
/** Failed requests in a row before an upload gives up. */
export const MAX_FAILURES = 6;

const COMMAND = 'X-Goog-Upload-Command';
const OFFSET = 'X-Goog-Upload-Offset';
const STATUS = 'X-Goog-Upload-Status';
const RECEIVED = 'X-Goog-Upload-Size-Received';

/**
 * Why an upload stopped. `expired`: its session ran out (an hour, or 10 minutes without a
 * byte). `refused`: the server ended the session, such as for the quota or the size limit
 * (`status` is the HTTP status). `failed`: the server stayed unreachable or unavailable.
 */
export class UploadError extends Error {
  readonly kind: 'expired' | 'refused' | 'failed' | 'cancelled';
  readonly status: number;

  constructor(kind: UploadError['kind'], status = 0) {
    super(`upload ${kind}${status ? ` (${status})` : ''}`);
    this.name = 'UploadError';
    this.kind = kind;
    this.status = status;
  }
}

export interface UploadOptions {
  name: string;
  file: Blob;
  contentType?: string;
  signal: AbortSignal;
  /** Bytes the server has kept so far. */
  onProgress: (sent: number) => void;
  /** Called while a failed request waits to be retried. */
  onRetry?: (failures: number) => void;
  /** Issues a session; the console API's POST …/storage/uploads. */
  issue: (req: UploadRequest, signal: AbortSignal) => Promise<UploadSession>;
  fetch?: typeof fetch;
  now?: () => number;
  sleep?: (ms: number, signal: AbortSignal) => Promise<void>;
  origin?: string;
}

/**
 * Where to send a session's requests: its URL as issued when it is on this page's origin, else
 * the same path here. The server builds the URL from public_url, which may name this server
 * otherwise than the address bar does, and the page may only connect to its own origin.
 */
export function sessionTarget(uploadUrl: string, origin: string): string {
  const url = new URL(uploadUrl, origin);
  return url.origin === origin ? url.href : `${url.pathname}${url.search}`;
}

/** The wait before retry number `failures` (1-based): 1, 2, 4, 8… seconds, at most 16. */
export const backoff = (failures: number) => Math.min(1000 * 2 ** (failures - 1), 16000);

function defaultSleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    const timer = setTimeout(done, ms);
    signal.addEventListener('abort', done, { once: true });
    function done() {
      clearTimeout(timer);
      signal.removeEventListener('abort', done);
      resolve();
    }
  });
}

/** A retry is worth it for what passes: the network, a timeout, the rate, the server's load. */
const retryable = (status: number) => status === 0 || status === 408 || status === 429 || status >= 500;

/**
 * Uploads a file through a session the dashboard issues, and resolves with the object resource
 * /v0 answers the finalize with. It resumes from what the server kept after any failure that
 * passes, issues a new session if the first one expired before its first byte, and cancels the
 * session when `signal` aborts.
 */
export async function resumableUpload(opts: UploadOptions): Promise<unknown> {
  const { file, signal } = opts;
  const send = opts.fetch ?? ((input, init) => fetch(input, init));
  const now = opts.now ?? Date.now;
  const sleep = opts.sleep ?? defaultSleep;
  const origin = opts.origin ?? window.location.origin;
  const size = file.size;
  const request: UploadRequest = { name: opts.name, size, ...(opts.contentType && { contentType: opts.contentType }) };

  let url = '';
  let issued = 0;
  const issue = async (): Promise<UploadSession> => {
    issued++;
    const session = await opts.issue(request, signal);
    url = sessionTarget(session.uploadUrl, origin);
    return session;
  };

  // One request on the session, without the cookie. Status 0 is a network failure.
  const post = async (headers: Record<string, string>, body?: Blob): Promise<{ status: number; state?: string; res?: Response }> => {
    try {
      const res = await send(url, { method: 'POST', headers, body, credentials: 'omit', cache: 'no-store', signal });
      return { status: res.status, state: res.headers.get(STATUS) ?? undefined, res };
    } catch {
      if (signal.aborted) throw new UploadError('cancelled');
      return { status: 0 };
    }
  };
  // The bytes the session kept, from a reply that says so.
  const receivedOf = (res: Response | undefined): number | undefined => {
    const n = Number(res?.headers.get(RECEIVED) ?? NaN);
    return Number.isSafeInteger(n) && n >= 0 && n <= size ? n : undefined;
  };

  try {
    let session = await issue();
    let offset = 0;
    let chunk = BASE_CHUNK;
    let failures = 0;

    // Counts a failure, and waits before the next request; too many in a row end the upload.
    const fail = async (status: number) => {
      failures++;
      if (failures > MAX_FAILURES) throw new UploadError('failed', status);
      opts.onRetry?.(failures);
      chunk = BASE_CHUNK;
      await sleep(backoff(failures), signal);
      if (signal.aborted) throw new UploadError('cancelled');
    };

    for (;;) {
      if (signal.aborted) throw new UploadError('cancelled');
      if (now() >= Date.parse(session.expireTime)) throw new UploadError('expired');
      // A session gets no byte after startBy: before the first one, take a new session instead.
      if (offset === 0 && now() >= Date.parse(session.startBy)) session = await issue();

      const end = Math.min(offset + chunk, size);
      const { status, state, res } = await post({ [COMMAND]: end === size ? 'upload, finalize' : 'upload', [OFFSET]: String(offset) }, file.slice(offset, end));

      if (res?.ok && state === 'final') return await res.json();
      if (res?.ok && state === 'active') {
        offset = receivedOf(res) ?? end;
        opts.onProgress(offset);
        failures = 0;
        chunk = Math.min(chunk * 2, MAX_CHUNK);
        continue;
      }
      if (state === 'final' || state === 'cancelled') throw new UploadError('refused', status);
      if (status === 404) {
        // The session is gone. One that never got a byte expired at startBy: issue another.
        if (offset === 0 && issued < 3) {
          session = await issue();
          continue;
        }
        throw new UploadError('expired', status);
      }
      const kept = status === 400 && state === 'active' ? receivedOf(res) : undefined;
      if (kept !== undefined) {
        // A gap: the server kept another length than this chunk assumed. Go on from it.
        await fail(status);
        offset = kept;
        opts.onProgress(offset);
        continue;
      }
      if (!retryable(status)) throw new UploadError('refused', status);

      // A failure that passes: wait, ask the session what it kept, and go on from there.
      await fail(status);
      for (;;) {
        const q = await post({ [COMMAND]: 'query' });
        if (q.res?.ok && q.state === 'final') return await q.res.json();
        const received = q.res?.ok && q.state === 'active' ? receivedOf(q.res) : undefined;
        if (received !== undefined) {
          offset = received;
          opts.onProgress(offset);
          break;
        }
        if (q.state === 'final' || q.state === 'cancelled') throw new UploadError('refused', q.status);
        if (q.status === 404) throw new UploadError('expired', q.status);
        if (!retryable(q.status)) throw new UploadError('refused', q.status);
        await fail(q.status);
      }
    }
  } catch (err) {
    if (!signal.aborted && !(err instanceof UploadError && err.kind === 'cancelled')) throw err;
    if (url) {
      // Best effort, after the abort: the session's bytes go now rather than when it expires.
      void send(url, { method: 'POST', headers: { [COMMAND]: 'cancel' }, credentials: 'omit', cache: 'no-store', keepalive: true }).catch(() => undefined);
    }
    throw new UploadError('cancelled');
  }
}
