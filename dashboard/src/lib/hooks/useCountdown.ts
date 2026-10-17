'use client';

import { useEffect, useState } from 'react';

/** Counts whole seconds down to zero. Call `start(seconds)` to begin. */
export function useCountdown(): [remaining: number, start: (seconds: number) => void] {
  const [deadline, setDeadline] = useState<number | null>(null);
  const [remaining, setRemaining] = useState(0);

  useEffect(() => {
    if (deadline === null) return;
    const tick = () => {
      const left = Math.max(0, Math.ceil((deadline - Date.now()) / 1000));
      setRemaining(left);
      if (left === 0) setDeadline(null);
    };
    tick();
    const timer = setInterval(tick, 1000);
    return () => clearInterval(timer);
  }, [deadline]);

  return [remaining, (seconds: number) => setDeadline(Date.now() + seconds * 1000)];
}
