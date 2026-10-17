'use client';

import { useFormatter } from 'use-intl';

/** A timestamp in the reader's language and time zone, machine-readable in `dateTime`. */
export function Time({ value, withTime = true }: { value: string; withTime?: boolean }) {
  const format = useFormatter();
  const date = new Date(value);
  return (
    <time dateTime={value}>{format.dateTime(date, withTime ? { dateStyle: 'medium', timeStyle: 'short' } : { dateStyle: 'medium' })}</time>
  );
}
