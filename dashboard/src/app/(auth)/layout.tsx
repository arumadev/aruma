import type { ReactNode } from 'react';
import { AuthFrame } from '@/features/session/AuthFrame';

export default function AuthLayout({ children }: { children: ReactNode }) {
  return <AuthFrame>{children}</AuthFrame>;
}
