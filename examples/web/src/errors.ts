import type { MessageKey, Translate } from './i18n';

// An error the app raises itself, with a code of the same shape as the SDKs' own.
export class AppError extends Error {
  constructor(
    public code: string,
    public vars?: Record<string, string | number>,
  ) {
    super(code);
  }
}

// The SDKs' error codes, mapped to a sentence a user understands. The Realtime Database reports
// a rules denial with the code PERMISSION_DENIED (and "permission_denied" in its message).
const byCode: Record<string, MessageKey> = {
  'auth/invalid-credential': 'errors.wrongPassword',
  'auth/invalid-login-credentials': 'errors.wrongPassword',
  'auth/wrong-password': 'errors.wrongPassword',
  'auth/user-not-found': 'errors.wrongPassword',
  'auth/weak-password': 'errors.weakPassword',
  'auth/password-does-not-meet-requirements': 'errors.weakPassword',
  'auth/email-already-in-use': 'errors.emailInUse',
  'auth/invalid-email': 'errors.invalidEmail',
  'auth/missing-email': 'errors.invalidEmail',
  'auth/missing-password': 'errors.missingPassword',
  'auth/too-many-requests': 'errors.tooManyRequests',
  'auth/network-request-failed': 'errors.network',
  'auth/operation-not-allowed': 'errors.guestDisabled',
  'auth/admin-restricted-operation': 'errors.guestDisabled',
  'permission-denied': 'errors.permissionDenied',
  PERMISSION_DENIED: 'errors.permissionDenied',
  'storage/unauthorized': 'errors.permissionDenied',
  'storage/unauthenticated': 'errors.permissionDenied',
  unavailable: 'errors.unavailable',
  'storage/retry-limit-exceeded': 'errors.network',
  'not-found': 'errors.notFound',
  'storage/object-not-found': 'errors.notFound',
  'failed-precondition': 'errors.index',
  'storage/quota-exceeded': 'errors.quota',
  'storage/canceled': 'errors.canceled',
  'app/file-too-big': 'errors.fileTooBig',
  'app/not-image': 'errors.notImage',
  'app/name-length': 'errors.nameLength',
  'app/title-length': 'errors.titleLength',
  'app/message-length': 'errors.messageLength',
  'app/team-not-found': 'errors.teamNotFound',
};

export function errorCode(err: unknown): string {
  if (err && typeof err === 'object' && 'code' in err && typeof err.code === 'string') return err.code;
  if (err instanceof Error && /permission[_ ]denied/i.test(err.message)) return 'PERMISSION_DENIED';
  return 'unknown';
}

export function errorMessage(t: Translate, err: unknown): string {
  const code = errorCode(err);
  const key = byCode[code];
  // A failed-precondition that is not about an index (a transaction's) is not worth a hint.
  if (code === 'failed-precondition' && !(err instanceof Error && /index/i.test(err.message))) {
    return t('errors.generic', { code });
  }
  if (key) return t(key, err instanceof AppError ? err.vars : undefined);
  return t('errors.generic', { code });
}

export function isPermissionDenied(err: unknown): boolean {
  const code = errorCode(err);
  return code === 'permission-denied' || code === 'PERMISSION_DENIED' || code === 'storage/unauthorized';
}
