import { Suspense } from 'react';
import { LoginScreen } from '@/features/session/LoginScreen';

export default function LoginPage() {
  return (
    <Suspense>
      <LoginScreen />
    </Suspense>
  );
}
