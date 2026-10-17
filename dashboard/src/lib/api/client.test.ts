import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ApiError, CSRF_HEADER, filenameFromContentDisposition, request, requestEmpty, setCsrfToken, setUnauthenticatedHandler } from './client';

const json = (status: number, body: unknown, headers: Record<string, string> = {}) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json', ...headers } });

const rpcError = (code: number, status: string, details: unknown[] = []) => ({ error: { code, status, message: 'English text', details } });

let fetchMock: ReturnType<typeof vi.fn>;

beforeEach(() => {
  fetchMock = vi.fn();
  vi.stubGlobal('fetch', fetchMock);
});

afterEach(() => {
  vi.unstubAllGlobals();
  setCsrfToken(null);
  setUnauthenticatedHandler(null);
});

describe('request', () => {
  it('calls the same-origin console API with JSON', async () => {
    fetchMock.mockResolvedValue(json(200, { projectId: 'chat-app' }));
    await expect(request('/projects:checkId', { query: { projectId: 'chat-app', empty: '' } })).resolves.toEqual({ projectId: 'chat-app' });
    const [url, init] = fetchMock.mock.calls[0]!;
    expect(url).toBe('/console/v1/projects:checkId?projectId=chat-app');
    expect(init).toMatchObject({ method: 'GET', credentials: 'same-origin' });
  });

  it('sends the CSRF token on writes only', async () => {
    setCsrfToken('csrf-1');
    fetchMock.mockImplementation(() => Promise.resolve(json(200, {})));
    await request('/me');
    await request('/projects', { method: 'POST', body: { projectId: 'chat-app' } });
    expect(fetchMock.mock.calls[0]![1].headers[CSRF_HEADER]).toBeUndefined();
    expect(fetchMock.mock.calls[1]![1].headers).toMatchObject({ [CSRF_HEADER]: 'csrf-1', 'Content-Type': 'application/json' });
    expect(fetchMock.mock.calls[1]![1].body).toBe('{"projectId":"chat-app"}');
  });

  it('maps google.rpc.Status with field violations to ApiError', async () => {
    fetchMock.mockResolvedValue(
      json(400, rpcError(400, 'INVALID_ARGUMENT', [{ '@type': 'type.googleapis.com/google.rpc.BadRequest', fieldViolations: [{ field: 'projectId', description: 'bad' }] }])),
    );
    const error = await request('/projects', { method: 'POST', body: {} }).catch((e: unknown) => e);
    expect(error).toBeInstanceOf(ApiError);
    expect(error).toMatchObject({ code: 400, status: 'INVALID_ARGUMENT', fieldViolations: [{ field: 'projectId', description: 'bad' }] });
    expect((error as ApiError).violation('projectId')?.description).toBe('bad');
  });

  it('reads Retry-After from a 429', async () => {
    fetchMock.mockResolvedValue(json(429, rpcError(429, 'RESOURCE_EXHAUSTED'), { 'Retry-After': '31' }));
    await expect(request('/auth:login', { method: 'POST', body: {} })).rejects.toMatchObject({ status: 'RESOURCE_EXHAUSTED', retryAfter: 31 });
  });

  it('reports a network failure as UNAVAILABLE with code 0', async () => {
    fetchMock.mockRejectedValue(new TypeError('Failed to fetch'));
    await expect(request('/me')).rejects.toMatchObject({ code: 0, status: 'UNAVAILABLE' });
  });

  it('copes with a non-JSON error body', async () => {
    fetchMock.mockResolvedValue(new Response('<html>bad gateway</html>', { status: 502, statusText: 'Bad Gateway' }));
    await expect(request('/me')).rejects.toMatchObject({ code: 502, status: 'UNKNOWN' });
  });

  it('runs the unauthenticated handler on a 401 from a signed-in route', async () => {
    const handler = vi.fn();
    setUnauthenticatedHandler(handler);
    setCsrfToken('csrf-1');
    fetchMock.mockResolvedValue(json(401, rpcError(401, 'UNAUTHENTICATED')));
    await expect(requestEmpty('/me/tokens/x', { method: 'DELETE' })).rejects.toMatchObject({ code: 401 });
    expect(handler).toHaveBeenCalledOnce();
  });

  it('leaves a failed sign-in to the form', async () => {
    const handler = vi.fn();
    setUnauthenticatedHandler(handler);
    fetchMock.mockResolvedValue(json(401, rpcError(401, 'UNAUTHENTICATED')));
    await expect(request('/auth:login', { method: 'POST', body: {} })).rejects.toMatchObject({ code: 401 });
    expect(handler).not.toHaveBeenCalled();
  });
});

describe('filenameFromContentDisposition', () => {
  it.each([
    ['attachment; filename=google-services.json', 'google-services.json'],
    ['attachment; filename="GoogleService-Info.plist"', 'GoogleService-Info.plist'],
    ['attachment; filename="a \\"quoted\\" name.json"', 'a "quoted" name.json'],
    ["attachment; filename*=utf-8''%D9%85%D9%84%D9%81.json", 'ملف.json'],
    [null, null],
    ['inline', null],
  ])('%s → %s', (header, name) => {
    expect(filenameFromContentDisposition(header)).toBe(name);
  });
});
