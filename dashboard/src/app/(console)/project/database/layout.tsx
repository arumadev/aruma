import type { ReactNode } from 'react';
import { DatabaseSection } from '@/features/database/DatabaseSection';

export default function DatabaseLayout({ children }: { children: ReactNode }) {
  return <DatabaseSection>{children}</DatabaseSection>;
}
