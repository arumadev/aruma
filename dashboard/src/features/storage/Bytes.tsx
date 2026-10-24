'use client';

import { useFormatter } from 'use-intl';
import { byteSize } from './paths';

/** A size in bytes, in the reader's language and the largest unit that keeps it at least 1. */
export function useBytes(): (bytes: number) => string {
  const format = useFormatter();
  return (bytes) => {
    const { value, unit } = byteSize(bytes);
    return format.number(value, { style: 'unit', unit, unitDisplay: 'short', maximumFractionDigits: unit === 'byte' ? 0 : 1 });
  };
}

export function Bytes({ value }: { value: number }) {
  const bytes = useBytes();
  return <bdi>{bytes(value)}</bdi>;
}
