import { render, type RenderResult } from '@testing-library/react';
import type { ReactElement } from 'react';
import { vi } from 'vitest';
import { Providers } from '@/app/Providers';
import { router, searchParams } from './navigation';

export { router };

/** Renders inside the app's real providers, with Next's router mocked. */
export function renderApp(ui: ReactElement, { search = '' } = {}): RenderResult {
  searchParams.current = new URLSearchParams(search);
  return render(<Providers>{ui}</Providers>);
}

/** A fetch stub answering console API paths from a table; anything else is a 404. */
export function stubApi(routes: Record<string, (init: RequestInit) => Response | Promise<Response>>) {
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = new URL(String(input), 'http://localhost');
    const handler = routes[`${init.method ?? 'GET'} ${url.pathname}`];
    if (handler) return handler(init);
    return json(404, { error: { code: 404, status: 'NOT_FOUND', message: 'Not found.' } });
  });
  vi.stubGlobal('fetch', fetchMock);
  return fetchMock;
}

export const json = (status: number, body: unknown, headers: Record<string, string> = {}) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json', ...headers } });
