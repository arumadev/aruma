import { Suspense } from 'react';
import { LoginScreen } from '@/features/auth/LoginScreen';

export default function LoginPage() {
  return (
    <Suspense>
      <LoginScreen />
    </Suspense>
  );
}
