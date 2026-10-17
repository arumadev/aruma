import ar from '@messages/ar.json';
import en from '@messages/en.json';
import type { Locale } from '@/lib/api/types';

export type { Locale };

export const DEFAULT_LOCALE: Locale = 'ar';
export const LOCALES: readonly Locale[] = ['ar', 'en'];

export const MESSAGES = { ar, en } as const;

export const directionOf = (locale: Locale): 'rtl' | 'ltr' => (locale === 'ar' ? 'rtl' : 'ltr');

export const isLocale = (value: unknown): value is Locale => value === 'ar' || value === 'en';

/**
 * The locale used to format dates and numbers. Arabic keeps Latin digits so that numbers read
 * the same as the identifiers, project numbers and code they sit next to.
 */
export const formattingLocale = (locale: Locale): string => (locale === 'ar' ? 'ar-u-nu-latn' : 'en');
