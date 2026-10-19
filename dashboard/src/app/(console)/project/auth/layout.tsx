import type { ReactNode } from 'react';
import { AuthSection } from '@/features/auth/AuthSection';

export default function AuthLayout({ children }: { children: ReactNode }) {
  return <AuthSection>{children}</AuthSection>;
}
