import { act, renderHook, waitFor } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { useResource } from './useResource';

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((r) => (resolve = r));
  return { promise, resolve };
}

describe('useResource', () => {
  it('loads, then reloads while keeping the data visible', async () => {
    let n = 0;
    const { result } = renderHook(() => useResource(async () => ++n, []));
    expect(result.current).toMatchObject({ loading: true, data: undefined });
    await waitFor(() => expect(result.current).toMatchObject({ loading: false, data: 1 }));
    act(() => result.current.reload());
    expect(result.current).toMatchObject({ loading: true, data: 1 });
    await waitFor(() => expect(result.current).toMatchObject({ loading: false, data: 2 }));
  });

  it("never shows the previous key's data and ignores its late answer", async () => {
    const answers: Record<string, ReturnType<typeof deferred<string>>> = { a: deferred(), b: deferred() };
    const { result, rerender } = renderHook(({ id }) => useResource(() => answers[id]!.promise, [id]), { initialProps: { id: 'a' } });
    rerender({ id: 'b' });
    await act(async () => answers.a!.resolve('project a'));
    expect(result.current).toMatchObject({ loading: true, data: undefined });
    await act(async () => answers.b!.resolve('project b'));
    expect(result.current).toMatchObject({ loading: false, data: 'project b' });
  });

  it('reports errors and waits while disabled', async () => {
    const { result, rerender } = renderHook(({ enabled }) => useResource(() => Promise.reject(new Error('boom')), [], enabled), {
      initialProps: { enabled: false },
    });
    expect(result.current.loading).toBe(false);
    rerender({ enabled: true });
    await waitFor(() => expect(result.current.error).toBeInstanceOf(Error));
  });
});
