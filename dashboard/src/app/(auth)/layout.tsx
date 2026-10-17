import type { ReactNode } from 'react';
import { AuthFrame } from '@/features/auth/AuthFrame';

export default function AuthLayout({ children }: { children: ReactNode }) {
  return <AuthFrame>{children}</AuthFrame>;
}
