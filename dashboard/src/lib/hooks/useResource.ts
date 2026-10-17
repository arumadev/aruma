'use client';

import { useCallback, useEffect, useState } from 'react';

export interface Resource<T> {
  data: T | undefined;
  error: unknown;
  loading: boolean;
  /** Fetches again, keeping the current data visible meanwhile. */
  reload: () => void;
  /** Replaces the data locally, after a write that returned the new value. */
  setData: (update: T | ((current: T | undefined) => T)) => void;
}

type Dep = string | number | boolean | null | undefined;

interface Settled<T> {
  key: string;
  generation: number;
  data?: T;
  error?: unknown;
}

/**
 * Loads data on the client whenever `deps` change. Stale responses are aborted and ignored, so
 * a fast navigation never shows the previous project's data. Pass `enabled: false` to wait.
 */
export function useResource<T>(load: (signal: AbortSignal) => Promise<T>, deps: readonly Dep[], enabled = true): Resource<T> {
  const key = JSON.stringify(deps);
  const [generation, setGeneration] = useState(0);
  const [settled, setSettled] = useState<Settled<T> | null>(null);

  useEffect(() => {
    if (!enabled) return;
    const controller = new AbortController();
    load(controller.signal).then(
      (data) => {
        if (!controller.signal.aborted) setSettled({ key, generation, data });
      },
      (error: unknown) => {
        if (!controller.signal.aborted) setSettled((prev) => ({ key, generation, error, data: prev?.key === key ? prev.data : undefined }));
      },
    );
    return () => controller.abort();
    // `load` is recreated each render; `key` stands for everything it closes over.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, generation, enabled]);

  const current = settled?.key === key ? settled : null;
  const reload = useCallback(() => setGeneration((g) => g + 1), []);
  const setData = useCallback(
    (update: T | ((value: T | undefined) => T)) =>
      setSettled((prev) => {
        if (!prev) return prev;
        const data = typeof update === 'function' ? (update as (value: T | undefined) => T)(prev.data) : update;
        return { ...prev, data, error: undefined };
      }),
    [],
  );

  return {
    data: current?.data,
    error: current?.generation === generation ? current.error : undefined,
    loading: enabled && current?.generation !== generation,
    reload,
    setData,
  };
}
