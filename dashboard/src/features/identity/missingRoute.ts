import { ApiError } from '@/lib/api/client';
import { authCode } from '@/lib/errors';

/**
 * Whether a call failed because the server does not have the route yet (the email and provider
 * routes arrive separately). An unknown path is a 404; an unknown custom method on a user, such
 * as `users/{uid}:sendPasswordReset`, reaches the user route and is refused as UNIMPLEMENTED.
 * A missing user is a real 404 and does not count.
 */
export function isMissingRoute(error: unknown): boolean {
  if (!(error instanceof ApiError)) return false;
  return (error.code === 404 && authCode(error) !== 'USER_NOT_FOUND') || error.status === 'UNIMPLEMENTED';
}
