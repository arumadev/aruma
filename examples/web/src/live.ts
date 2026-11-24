import { useEffect, useState } from 'react';

export type Subscribe<T> = (next: (value: T) => void, fail: (error: unknown) => void) => () => void;

export interface Live<T> {
  /** undefined while the first value is on its way. */
  value: T | undefined;
  error: unknown;
}

// useLive keeps a listener open while `key` stays the same, and closes it on unmount or when the
// key changes (then the value is undefined again until the new listener answers). A null key
// opens nothing.
export function useLive<T>(key: string | null, subscribe: Subscribe<T>): Live<T> {
  const [state, setState] = useState<{ key: string | null; value?: T; error?: unknown }>({ key: null });
  useEffect(() => {
    if (key === null) return;
    return subscribe(
      (value) => setState({ key, value, error: null }),
      (error) => setState({ key, error }),
    );
    // The key names the subscription: a new subscribe function for the same key is the same listener.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);
  if (key === null || state.key !== key) return { value: undefined, error: null };
  return { value: state.value, error: state.error ?? null };
}

// The same for a one-shot promise, such as a download link.
export function fromPromise<T>(get: () => Promise<T>): Subscribe<T> {
  return (next, fail) => {
    let live = true;
    get().then(
      (v) => live && next(v),
      (e) => live && fail(e),
    );
    return () => {
      live = false;
    };
  };
}
