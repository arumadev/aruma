import type { ReactNode } from 'react';
import { StorageSection } from '@/features/storage/StorageSection';

export default function StorageLayout({ children }: { children: ReactNode }) {
  return <StorageSection>{children}</StorageSection>;
}
