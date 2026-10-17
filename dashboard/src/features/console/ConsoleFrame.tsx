'use client';

import { Suspense, useEffect, type ReactNode } from 'react';
import { useTranslations } from 'use-intl';
import { AppShell, ShellPlaceholder } from '@/components/shell/AppShell';
import { Banner } from '@/components/ui/Banner';
import { Button } from '@/components/ui/Button';
import { useErrorText } from '@/lib/errors';
import { useSession } from '@/lib/session/SessionProvider';

/**
 * Every signed-in page. The session is loaded once; without one, the API client redirects to
 * sign-in. Ids come from the query string, read under a Suspense boundary as the static export
 * requires.
 */
export function ConsoleFrame({ children }: { children: ReactNode }) {
  const t = useTranslations('common');
  const errorText = useErrorText();
  const { account, ensure, error } = useSession();
  useEffect(() => {
    if (!account) ensure();
  }, [account, ensure]);

  if (!account) {
    return (
      <ShellPlaceholder>
        {Boolean(error) && (
          <Banner
            tone="error"
            action={
              <Button variant="text" onPress={ensure}>
                {t('retry')}
              </Button>
            }
          >
            {errorText.message(error)}
          </Banner>
        )}
      </ShellPlaceholder>
    );
  }
  return (
    <Suspense fallback={<ShellPlaceholder />}>
      <AppShell>{children}</AppShell>
    </Suspense>
  );
}
