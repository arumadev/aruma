'use client';

import { useRouter } from 'next/navigation';
import { useEffect, useState } from 'react';
import { useTranslations } from 'use-intl';
import { Banner } from '@/components/ui/Banner';
import { Button } from '@/components/ui/Button';
import { Skeleton } from '@/components/ui/Skeleton';
import { ApiError } from '@/lib/api/client';
import * as api from '@/lib/api/console';
import { useErrorText } from '@/lib/errors';
import { routes } from '@/lib/routes';
import { useSession } from '@/lib/session/SessionProvider';
import { AuthFrame } from './AuthFrame';

/**
 * `/`: sends the visitor where they belong: first-boot setup, sign-in, or their projects. A
 * missing session answers 401, which the API client turns into a redirect to sign-in.
 */
export function StartScreen() {
  const t = useTranslations('common');
  const errorText = useErrorText();
  const router = useRouter();
  const { signedIn } = useSession();
  const [error, setError] = useState<unknown>(null);
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    const controller = new AbortController();
    (async () => {
      const status = await api.getStatus(controller.signal);
      if (status.setupRequired) return router.replace(routes.setup);
      signedIn(await api.getMe(controller.signal));
      router.replace(routes.projects);
    })().catch((err: unknown) => {
      const handled = err instanceof ApiError && err.code === 401;
      if (!controller.signal.aborted && !handled) setError(err);
    });
    return () => controller.abort();
  }, [router, signedIn, attempt]);

  function retry() {
    setError(null);
    setAttempt((n) => n + 1);
  }

  return (
    <AuthFrame>
      {error ? (
        <Banner
          tone="error"
          action={
            <Button variant="text" onPress={retry}>
              {t('retry')}
            </Button>
          }
        >
          {errorText.message(error)}
        </Banner>
      ) : (
        <Skeleton lines={2} />
      )}
    </AuthFrame>
  );
}
