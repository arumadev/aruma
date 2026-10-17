'use client';

import { useRouter } from 'next/navigation';
import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { ApiError, setCsrfToken, setUnauthenticatedHandler } from '@/lib/api/client';
import * as api from '@/lib/api/console';
import type { Account, Locale, Login, Me } from '@/lib/api/types';
import { useLocale } from '@/lib/i18n/LocaleProvider';
import { routes } from '@/lib/routes';

interface SessionContextValue {
  /** The signed-in account; undefined until known, null when signed out. */
  account: Account | null | undefined;
  /** Loads the session from GET /me if it is not known yet. */
  ensure: () => void;
  /** Why the last GET /me failed, other than a missing session. */
  error: unknown;
  /** Records a session from a sign-in, setup or GET /me response. */
  signedIn: (session: Login | Me) => void;
  signOut: () => Promise<void>;
  setAccount: (account: Account) => void;
  /** Switches the language and saves it on the account when signed in. Never rejects. */
  changeLocale: (locale: Locale) => Promise<void>;
}

const SessionContext = createContext<SessionContextValue | null>(null);

export function SessionProvider({ children }: { children: ReactNode }) {
  const router = useRouter();
  const { locale, setLocale, chosenThisVisit } = useLocale();
  const [account, setAccountState] = useState<Account | null>();
  const [error, setError] = useState<unknown>(null);
  const loading = useRef(false);
  const localeRef = useRef(locale);
  useEffect(() => {
    localeRef.current = locale;
  }, [locale]);

  // A session that ended (expired, revoked, signed out elsewhere) sends the user to sign in
  // and back to the page they were on.
  useEffect(() => {
    setUnauthenticatedHandler(() => {
      setAccountState(null);
      const here = `${window.location.pathname}${window.location.search}`;
      router.replace(here === routes.home ? routes.login : routes.loginThen(here));
    });
    return () => setUnauthenticatedHandler(null);
  }, [router]);

  // The account's language follows the user's latest explicit choice; otherwise the account wins.
  const adopt = useCallback(
    (acc: Account) => {
      setAccountState(acc);
      if (acc.locale === localeRef.current) return;
      if (chosenThisVisit()) {
        api.updateMe({ locale: localeRef.current }).then(({ account: updated }) => setAccountState(updated), () => undefined);
      } else {
        setLocale(acc.locale);
      }
    },
    [chosenThisVisit, setLocale],
  );

  const signedIn = useCallback(
    (session: Login | Me) => {
      setCsrfToken(session.csrfToken ?? null);
      setError(null);
      adopt(session.account);
    },
    [adopt],
  );

  const ensure = useCallback(() => {
    if (account || loading.current) return;
    loading.current = true;
    setError(null);
    api
      .getMe()
      .then(signedIn)
      .catch((err: unknown) => {
        // A 401 is handled by the unauthenticated handler.
        if (!(err instanceof ApiError && err.code === 401)) setError(err);
      })
      .finally(() => {
        loading.current = false;
      });
  }, [account, signedIn]);

  const signOut = useCallback(async () => {
    try {
      await api.logout();
    } finally {
      setCsrfToken(null);
      setAccountState(null);
      router.replace(routes.login);
    }
  }, [router]);

  const changeLocale = useCallback(
    async (next: Locale) => {
      setLocale(next, { chosen: true });
      if (!account) return;
      try {
        const { account: updated } = await api.updateMe({ locale: next });
        setAccountState(updated);
      } catch {
        // Saving is best effort: the switch still applies on this device.
      }
    },
    [account, setLocale],
  );

  const value = useMemo<SessionContextValue>(
    () => ({ account, ensure, error, signedIn, signOut, setAccount: setAccountState, changeLocale }),
    [account, ensure, error, signedIn, signOut, changeLocale],
  );
  return <SessionContext value={value}>{children}</SessionContext>;
}

export function useSession(): SessionContextValue {
  const value = useContext(SessionContext);
  if (!value) throw new Error('useSession must be used inside SessionProvider');
  return value;
}
