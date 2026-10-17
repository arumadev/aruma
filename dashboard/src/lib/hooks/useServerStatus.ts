'use client';

import * as api from '@/lib/api/console';
import type { Status } from '@/lib/api/types';
import { useResource, type Resource } from './useResource';

let cached: Promise<Status> | null = null;

/** The server's version and published limits, fetched once per page load. */
export function useServerStatus(): Resource<Status> {
  return useResource(() => {
    cached ??= api.getStatus().catch((err: unknown) => {
      cached = null;
      throw err;
    });
    return cached;
  }, []);
}
