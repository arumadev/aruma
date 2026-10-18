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

// Auth codes the console API keeps at the start of the message ("WEAK_PASSWORD : …").
export const AUTH_CODES = [
  'CLAIMS_TOO_LARGE',
  'DEV_MAILBOX_DISABLED',
  'DUPLICATE_LOCAL_ID',
  'EMAIL_EXISTS',
  'FORBIDDEN_CLAIM',
  'INVALID_CLAIMS',
  'INVALID_CONFIG',
  'INVALID_DISPLAY_NAME',
  'INVALID_EMAIL',
  'INVALID_LOCAL_ID',
  'INVALID_PHOTO_URL',
  'INVALID_SMTP_CONFIG',
  'INVALID_TEMPLATE',
  'MISSING_EMAIL',
  'PASSWORD_DOES_NOT_MEET_REQUIREMENTS',
  'SMTP_ERROR',
  'USER_NOT_FOUND',
  'WEAK_PASSWORD',
] as const;
type AuthCode = (typeof AUTH_CODES)[number];

/** The Auth code of a console API error, if it carries one. */
export function authCode(error: unknown): AuthCode | undefined {
  if (!(error instanceof ApiError)) return undefined;
  const code = error.message.split(' : ')[0];
  return (AUTH_CODES as readonly string[]).includes(code ?? '') ? (code as AuthCode) : undefined;
}
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
      const code = authCode(error);
      if (code) return t(`auth.${code}`);
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
