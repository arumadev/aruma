'use client';

import { Suspense, useEffect, useEffectEvent, type ReactNode } from 'react';
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
  // Ask the server once on arrival if no session is known: a fresh load, or Back into the
  // console after signing out. A session that ends while here is handled by whoever ended it
  // (sign-out or the 401 handler), which is already navigating to sign-in.
  const checkOnArrival = useEffectEvent(() => {
    if (!account) ensure();
  });
  useEffect(() => checkOnArrival(), []);

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
