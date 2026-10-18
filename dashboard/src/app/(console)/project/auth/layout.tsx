import type { ReactNode } from 'react';
import { AuthSection } from '@/features/identity/AuthSection';

export default function AuthLayout({ children }: { children: ReactNode }) {
  return <AuthSection>{children}</AuthSection>;
}
