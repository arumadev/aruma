'use client';

import { useCallback, useEffect, useState } from 'react';
import * as api from '../api';

const WATCH_MS = 1000;

/**
 * A field change is queued: the server answers with an operation and applies it in the build job,
 * so a list read at once can still show the field as it was. This watches the operations a write
 * returned and calls onSettled when one is done (or no longer listed), so the list is read again
 * once the change is in. Returns the function to hand each operation to.
 */
export function useOperationWatch(projectId: string, onSettled: () => void): (op: api.Operation) => void {
  const [pending, setPending] = useState<string[]>([]);

  useEffect(() => {
    if (pending.length === 0) return;
    let stopped = false;
    const timer = setInterval(() => {
      api.listOperations(projectId).then(
        ({ operations }) => {
          if (stopped) return;
          const left = pending.filter((name) => {
            const op = operations.find((o) => o.name === name);
            return op !== undefined && !op.done;
          });
          if (left.length < pending.length) {
            setPending(left);
            onSettled();
          }
        },
        () => undefined,
      );
    }, WATCH_MS);
    return () => {
      stopped = true;
      clearInterval(timer);
    };
  }, [pending, projectId, onSettled]);

  return useCallback((op: api.Operation) => {
    if (!op.done) setPending((p) => [...p, op.name]);
  }, []);
}
