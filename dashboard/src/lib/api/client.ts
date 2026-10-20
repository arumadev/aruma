// The one fetch wrapper for the console API: same-origin JSON, the CSRF header on writes,
// google.rpc.Status errors mapped to ApiError, and a hook for expired sessions.

export const API_BASE = '/console/v1';

export const CSRF_HEADER = 'X-Aruma-CSRF';

export interface FieldViolation {
  field: string;
  description: string;
}

/** A failed console API call. `code` is the HTTP status (0 when the network failed). */
export class ApiError extends Error {
  readonly code: number;
  readonly status: string;
  readonly fieldViolations: FieldViolation[];
  /** Seconds from the Retry-After header of a 429. */
  readonly retryAfter?: number;
  /** Every detail of the status as sent, such as a rules document's errors with their positions. */
  readonly details: unknown[];

  constructor(code: number, status: string, message: string, fieldViolations: FieldViolation[] = [], retryAfter?: number, details: unknown[] = []) {
    super(message);
    this.name = 'ApiError';
    this.code = code;
    this.status = status;
    this.fieldViolations = fieldViolations;
    this.retryAfter = retryAfter;
    this.details = details;
  }

  /** The violation reported for `field`, if any. */
  violation(field: string): FieldViolation | undefined {
    return this.fieldViolations.find((v) => v.field === field);
  }
}

// Routes that answer 401 for a wrong password rather than a missing session.
const PUBLIC_PATHS = new Set(['/status', '/setup', '/auth:login', '/auth:signup']);

let csrfToken: string | null = null;
let onUnauthenticated: (() => void) | null = null;

/** Keeps the session's CSRF token in memory only; it is recovered from GET /me after a reload. */
export function setCsrfToken(token: string | null): void {
  csrfToken = token;
}

/** Registers what happens when a signed-in call finds the session gone. */
export function setUnauthenticatedHandler(handler: (() => void) | null): void {
  onUnauthenticated = handler;
}

export type Query = Record<string, string | number | boolean | undefined>;

export interface RequestOptions {
  method?: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE';
  query?: Query;
  body?: unknown;
  signal?: AbortSignal;
}

export function apiUrl(path: string, query?: Query): string {
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(query ?? {})) {
    if (value !== undefined && value !== '') params.set(key, String(value));
  }
  const qs = params.toString();
  return `${API_BASE}${path}${qs ? `?${qs}` : ''}`;
}

/** Sends a request and returns the raw response, throwing ApiError for any failure. */
export async function send(path: string, { method = 'GET', query, body, signal }: RequestOptions = {}): Promise<Response> {
  const headers: Record<string, string> = { Accept: 'application/json' };
  if (body !== undefined) headers['Content-Type'] = 'application/json';
  if (method !== 'GET' && csrfToken) headers[CSRF_HEADER] = csrfToken;

  let res: Response;
  try {
    res = await fetch(apiUrl(path, query), {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
      credentials: 'same-origin',
      cache: 'no-store',
      signal,
    });
  } catch (err) {
    if (signal?.aborted) throw err;
    throw new ApiError(0, 'UNAVAILABLE', 'Network request failed.');
  }
  if (res.ok) return res;

  const error = await toApiError(res);
  if (error.code === 401 && !PUBLIC_PATHS.has(path)) {
    csrfToken = null;
    onUnauthenticated?.();
  }
  throw error;
}

async function toApiError(res: Response): Promise<ApiError> {
  const retry = Number(res.headers.get('Retry-After'));
  const retryAfter = Number.isFinite(retry) && retry > 0 ? retry : undefined;
  try {
    const { error } = (await res.json()) as {
      error: { code: number; status: string; message: string; details?: { '@type'?: string; fieldViolations?: FieldViolation[] }[] };
    };
    const violations = (error.details ?? []).flatMap((d) => (d['@type']?.endsWith('google.rpc.BadRequest') ? (d.fieldViolations ?? []) : []));
    return new ApiError(res.status, error.status, error.message, violations, retryAfter, error.details ?? []);
  } catch {
    return new ApiError(res.status, 'UNKNOWN', res.statusText, [], retryAfter);
  }
}

/** A JSON request. */
export async function request<T>(path: string, options?: RequestOptions): Promise<T> {
  const res = await send(path, options);
  return (await res.json()) as T;
}

/** A request answered with 204 No Content. */
export async function requestEmpty(path: string, options?: RequestOptions): Promise<void> {
  await send(path, options);
}

/** A request answered with plain text, such as a config snippet. */
export async function requestText(path: string, options?: RequestOptions): Promise<string> {
  const res = await send(path, options);
  return res.text();
}

/**
 * Reads the file name of a download. Handles RFC 6266 `filename*` (RFC 5987 encoding), quoted
 * and bare `filename` parameters, as Go's mime.FormatMediaType emits them.
 */
export function filenameFromContentDisposition(header: string | null): string | null {
  if (!header) return null;
  const extended = /filename\*\s*=\s*([^']*)'[^']*'([^;]+)/i.exec(header);
  if (extended?.[2]) {
    try {
      return decodeURIComponent(extended[2].trim());
    } catch {
      // Fall through to the plain parameter.
    }
  }
  const quoted = /filename\s*=\s*"((?:\\.|[^"\\])*)"/i.exec(header);
  if (quoted?.[1] !== undefined) return quoted[1].replace(/\\(.)/g, '$1');
  const bare = /filename\s*=\s*([^;\s]+)/i.exec(header);
  return bare?.[1] ?? null;
}

/** Fetches a file the server sends as an attachment and hands it to the browser to save. */
export async function downloadFile(path: string, options?: RequestOptions & { fallbackName?: string }): Promise<string> {
  const res = await send(path, options);
  const name = filenameFromContentDisposition(res.headers.get('Content-Disposition')) ?? options?.fallbackName ?? 'download';
  saveBlob(await res.blob(), name);
  return name;
}

function saveBlob(blob: Blob, name: string): void {
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = name;
  a.rel = 'noopener';
  document.body.append(a);
  a.click();
  a.remove();
  // Revoke after the browser has started reading the blob.
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}
