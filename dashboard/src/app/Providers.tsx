'use client';

import { useRouter } from 'next/navigation';
import type { ReactNode } from 'react';
import { RouterProvider } from 'react-aria-components';
import { SnackbarProvider } from '@/components/ui/Snackbar';
import { LocaleProvider } from '@/lib/i18n/LocaleProvider';
import { SessionProvider } from '@/lib/session/SessionProvider';

declare module 'react-aria-components' {
  interface RouterConfig {
    routerOptions: NonNullable<Parameters<ReturnType<typeof useRouter>['push']>[1]>;
  }
}

/** Client-side context for every page: language, session, client navigation and snackbars. */
export function Providers({ children }: { children: ReactNode }) {
  const router = useRouter();
  return (
    <LocaleProvider>
      <RouterProvider navigate={(href, options) => router.push(href, options)}>
        <SessionProvider>
          <SnackbarProvider>{children}</SnackbarProvider>
        </SessionProvider>
      </RouterProvider>
    </LocaleProvider>
  );
}
