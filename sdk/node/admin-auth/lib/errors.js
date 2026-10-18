// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

// Errors are the official FirebaseAuthError with the official codes, so code written against
// firebase-admin (error.code === 'auth/id-token-expired') keeps working unchanged.

import { FirebaseAuthError, AuthErrorCode } from 'firebase-admin/auth';

/**
 * Returns a FirebaseAuthError with code `auth/<code>`.
 * @param {string} code an AuthErrorCode value, such as 'id-token-expired'
 * @param {string} message
 */
export function authError(code, message) {
  return new FirebaseAuthError({ code, message });
}

export const argumentError = (message) => authError(AuthErrorCode.INVALID_ARGUMENT, message);

// The Identity Toolkit codes Aruma answers with, mapped as firebase-admin maps them
// (AUTH_SERVER_TO_CLIENT_CODE in firebase-admin-node src/auth/error.ts). Unknown codes become
// internal-error, as there.
const serverCodes = {
  CLAIMS_TOO_LARGE: AuthErrorCode.CLAIMS_TOO_LARGE,
  DUPLICATE_EMAIL: AuthErrorCode.EMAIL_ALREADY_EXISTS,
  DUPLICATE_LOCAL_ID: AuthErrorCode.UID_ALREADY_EXISTS,
  EMAIL_EXISTS: AuthErrorCode.EMAIL_ALREADY_EXISTS,
  EMAIL_NOT_FOUND: AuthErrorCode.EMAIL_NOT_FOUND,
  FORBIDDEN_CLAIM: AuthErrorCode.FORBIDDEN_CLAIM,
  INSUFFICIENT_PERMISSION: AuthErrorCode.INSUFFICIENT_PERMISSION,
  INVALID_CLAIMS: AuthErrorCode.INVALID_CLAIMS,
  INVALID_DISPLAY_NAME: AuthErrorCode.INVALID_DISPLAY_NAME,
  INVALID_DURATION: AuthErrorCode.INVALID_SESSION_COOKIE_DURATION,
  INVALID_EMAIL: AuthErrorCode.INVALID_EMAIL,
  INVALID_ID_TOKEN: AuthErrorCode.INVALID_ID_TOKEN,
  INVALID_LOCAL_ID: AuthErrorCode.INVALID_UID,
  INVALID_PAGE_SELECTION: AuthErrorCode.INVALID_PAGE_TOKEN,
  INVALID_PHOTO_URL: AuthErrorCode.INVALID_PHOTO_URL,
  MISSING_LOCAL_ID: AuthErrorCode.MISSING_UID,
  OPERATION_NOT_ALLOWED: AuthErrorCode.OPERATION_NOT_ALLOWED,
  PASSWORD_DOES_NOT_MEET_REQUIREMENTS: AuthErrorCode.INVALID_PASSWORD,
  PERMISSION_DENIED: AuthErrorCode.INSUFFICIENT_PERMISSION,
  TOKEN_EXPIRED: AuthErrorCode.ID_TOKEN_EXPIRED,
  TOO_MANY_IDENTIFIERS: AuthErrorCode.MAXIMUM_USER_COUNT_EXCEEDED,
  USER_DISABLED: AuthErrorCode.USER_DISABLED,
  USER_NOT_FOUND: AuthErrorCode.USER_NOT_FOUND,
  WEAK_PASSWORD: AuthErrorCode.INVALID_PASSWORD,
};

/**
 * Turns an Aruma error response into a FirebaseAuthError. Identity Toolkit errors carry
 * "CODE" or "CODE : detail" as their message; credential failures are google.rpc.Status.
 * @param {number} status
 * @param {any} body the parsed response, if any
 */
export function fromResponse(status, body) {
  const message = typeof body?.error?.message === 'string' ? body.error.message : '';
  const sep = message.indexOf(':');
  const serverCode = (sep === -1 ? message : message.slice(0, sep)).trim();
  const detail = sep === -1 ? '' : message.slice(sep + 1).trim();
  if (serverCodes[serverCode]) {
    return authError(serverCodes[serverCode], detail || `Aruma Auth answered ${serverCode}.`);
  }
  if (status === 401) {
    return authError(AuthErrorCode.INVALID_CREDENTIAL, 'Aruma refused the service account credential.');
  }
  if (status === 403) {
    return authError(AuthErrorCode.INSUFFICIENT_PERMISSION, 'The credential may not act on this project.');
  }
  return authError(AuthErrorCode.INTERNAL_ERROR, `Aruma Auth answered HTTP ${status}${message ? `: ${message}` : ''}.`);
}
