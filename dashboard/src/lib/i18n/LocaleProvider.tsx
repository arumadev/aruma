'use client';

import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useSyncExternalStore, type ReactNode } from 'react';
import { I18nProvider } from 'react-aria-components';
import { IntlProvider } from 'use-intl';
import { getLocaleSnapshot, getServerLocaleSnapshot, saveLocale, subscribeLocale } from './localeStore';
import { MESSAGES, directionOf, formattingLocale, type Locale } from './locales';

interface LocaleContextValue {
  locale: Locale;
  /** Switches the language without a reload. `chosen` marks a choice made by the user. */
  setLocale: (locale: Locale, options?: { chosen?: boolean }) => void;
  /** Whether the user picked the language during this visit, before or after signing in. */
  chosenThisVisit: () => boolean;
}

const LocaleContext = createContext<LocaleContextValue | null>(null);

const browserTimeZone = () => Intl.DateTimeFormat().resolvedOptions().timeZone;
const serverTimeZone = () => 'UTC';
const noSubscription = () => () => undefined;

/**
 * Holds the interface language. Pages are prerendered in Arabic (`lang="ar" dir="rtl"`); the
 * client restores the last language used on this device and keeps <html> lang and dir in step,
 * and the session provider aligns it with the account.
 */
export function LocaleProvider({ children }: { children: ReactNode }) {
  const locale = useSyncExternalStore(subscribeLocale, getLocaleSnapshot, getServerLocaleSnapshot);
  const timeZone = useSyncExternalStore(noSubscription, browserTimeZone, serverTimeZone);
  const chosen = useRef(false);

  useEffect(() => {
    const root = document.documentElement;
    root.lang = locale;
    root.dir = directionOf(locale);
  }, [locale]);

  const setLocale = useCallback((next: Locale, options?: { chosen?: boolean }) => {
    if (options?.chosen) chosen.current = true;
    saveLocale(next);
  }, []);

  const value = useMemo<LocaleContextValue>(() => ({ locale, setLocale, chosenThisVisit: () => chosen.current }), [locale, setLocale]);

  return (
    <LocaleContext value={value}>
      <IntlProvider locale={formattingLocale(locale)} messages={MESSAGES[locale]} timeZone={timeZone}>
        <I18nProvider locale={locale}>{children}</I18nProvider>
      </IntlProvider>
    </LocaleContext>
  );
}

export function useLocale(): LocaleContextValue {
  const value = useContext(LocaleContext);
  if (!value) throw new Error('useLocale must be used inside LocaleProvider');
  return value;
}
