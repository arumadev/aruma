'use client';

import { useCallback, useMemo } from 'react';
import { useTranslations } from 'use-intl';
import { ApiError } from '@/lib/api/client';

// Fields whose server-side validation message has a localized equivalent.
const FIELDS = [
  'accountId',
  'authDomain',
  'bundleId',
  'confirmProjectId',
  'currentPassword',
  'displayName',
  'email',
  'expireDays',
  'name',
  'newPassword',
  'packageName',
  'password',
  'projectId',
  'restrictions',
  'shaHash',
] as const;
type KnownField = (typeof FIELDS)[number];
const isKnown = (field: string): field is KnownField => (FIELDS as readonly string[]).includes(field);

export interface ErrorText {
  /** A localized sentence for any failure. */
  message: (error: unknown) => string;
  /** The localized problem with one form field, if the server reported one. */
  field: (error: unknown, name: string) => string | undefined;
}

/** Turns API errors into localized text. Server messages are English, so they are not shown. */
export function useErrorText(): ErrorText {
  const t = useTranslations('errors');

  const fieldText = useCallback(
    (name: string): string | undefined => {
      const base = name.split('.')[0] ?? name;
      return isKnown(base) ? t(`field.${base}`) : undefined;
    },
    [t],
  );

  const field = useCallback(
    (error: unknown, name: string) => {
      if (!(error instanceof ApiError)) return undefined;
      const violation = error.fieldViolations.find((v) => v.field === name || v.field.startsWith(`${name}.`));
      return violation ? (fieldText(violation.field) ?? t('invalid')) : undefined;
    },
    [fieldText, t],
  );

  const message = useCallback(
    (error: unknown): string => {
      if (!(error instanceof ApiError)) return t('unknown');
      switch (error.status) {
        case 'UNAVAILABLE':
          return error.code === 0 ? t('network') : t('unavailable');
        case 'INVALID_ARGUMENT': {
          const first = error.fieldViolations[0];
          return (first && fieldText(first.field)) ?? t('invalid');
        }
        case 'ALREADY_EXISTS':
          return t('alreadyExists');
        case 'ABORTED':
          return t('stale');
        case 'NOT_FOUND':
          return t('notFound');
        case 'PERMISSION_DENIED':
          return t('permissionDenied');
        case 'FAILED_PRECONDITION':
          return t('failedPrecondition');
        case 'RESOURCE_EXHAUSTED':
          return error.retryAfter ? t('tooManyRetry', { seconds: error.retryAfter }) : t('tooMany');
        case 'UNAUTHENTICATED':
          return t('unauthenticated');
        default:
          return t('unknown');
      }
    },
    [fieldText, t],
  );

  return useMemo(() => ({ message, field }), [message, field]);
}
