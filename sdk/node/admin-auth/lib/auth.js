// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

import { getAuth as getFirebaseAuth, UserRecord, FirebaseAuthError, AuthErrorCode } from 'firebase-admin/auth';
import { AdminClient } from './client.js';
import { emulatorGuard, findKey, resolveURL } from './config.js';
import { authError, argumentError } from './errors.js';
import { Verifier, ID_TOKEN, SESSION_COOKIE } from './verifier.js';

// Names the official SDK refuses as developer claims (RESERVED_CLAIMS in firebase-admin-node).
const reservedClaims = new Set(['acr', 'amr', 'at_hash', 'aud', 'auth_time', 'azp', 'cnf', 'c_hash', 'exp', 'iat',
  'iss', 'jti', 'nbf', 'nonce', 'sub', 'firebase']);

const minCookieMs = 5 * 60 * 1000;
const maxCookieMs = 14 * 24 * 60 * 60 * 1000;

/** Builds the official UserRecord from an Identity Toolkit user, as firebase-admin does. */
function userRecord(json) {
  return Reflect.construct(UserRecord, [json]);
}

let selfTested = false;

// selfTest fails loudly if a firebase-admin release changes the two constructors the library
// relies on, instead of returning wrong types later.
function selfTest() {
  if (selfTested) {
    return;
  }
  const err = new FirebaseAuthError({ code: AuthErrorCode.ID_TOKEN_EXPIRED, message: 'self-test' });
  const user = userRecord({ localId: 'self-test', validSince: '1', passwordHash: 'UkVEQUNURUQ=' });
  if (err.code !== 'auth/id-token-expired' || !(user instanceof UserRecord) || user.uid !== 'self-test' ||
    user.passwordHash !== undefined || user.tokensValidAfterTime !== new Date(1000).toUTCString()) {
    throw new Error('@aruma/admin-auth: this firebase-admin version is not supported (need ^14)');
  }
  selfTested = true;
}

function checkUID(uid) {
  if (typeof uid !== 'string' || uid === '' || uid.length > 128) {
    throw authError(AuthErrorCode.INVALID_UID, 'The uid must be a non-empty string with at most 128 characters.');
  }
}

function checkEmail(email) {
  if (typeof email !== 'string' || !/^[^@]+@[^@]+$/.test(email)) {
    throw authError(AuthErrorCode.INVALID_EMAIL, 'The email address is improperly formatted.');
  }
}

/** Aruma Auth for a developer's server: the BaseAuth methods, answered by Aruma. */
export class ArumaAuth {
  /**
   * @param {import('firebase-admin/app').App} app
   * @param {import('../index.js').ArumaAuthOptions} options
   */
  constructor(app, options = {}) {
    selfTest();
    emulatorGuard(options);
    const key = findKey(app, options);
    const projectId = app.options.projectId ?? key.projectId;
    if (!projectId) {
      throw argumentError('No project id: set projectId in the app options or use a key file that has project_id.');
    }
    const url = resolveURL(options, key);
    this.app = app;
    this.projectId = projectId;
    this.url = url;
    this.client = new AdminClient({ url, projectId, key, fetch: options.fetch });
    this.idTokens = new Verifier({ url, projectId, kind: ID_TOKEN, fetch: options.fetch });
    this.cookies = new Verifier({ url, projectId, kind: SESSION_COOKIE, fetch: options.fetch });
  }

  /** Verifies an Aruma ID token; with checkRevoked, also that its user is live and its session not revoked. */
  async verifyIdToken(idToken, checkRevoked = false) {
    const decoded = await this.idTokens.verify(idToken);
    return checkRevoked ? this.checkRevoked(decoded, AuthErrorCode.ID_TOKEN_REVOKED) : decoded;
  }

  /** Verifies an Aruma session cookie, as verifyIdToken. */
  async verifySessionCookie(sessionCookie, checkRevoked = false) {
    const decoded = await this.cookies.verify(sessionCookie);
    return checkRevoked ? this.checkRevoked(decoded, AuthErrorCode.SESSION_COOKIE_REVOKED) : decoded;
  }

  // checkRevoked compares auth_time with tokensValidAfterTime, as firebase-admin for Node does.
  async checkRevoked(decoded, revokedCode) {
    const user = await this.getUser(decoded.sub);
    if (user.disabled) {
      throw authError(AuthErrorCode.USER_DISABLED, 'The user record is disabled.');
    }
    if (user.tokensValidAfterTime && decoded.auth_time * 1000 < Date.parse(user.tokensValidAfterTime)) {
      throw authError(revokedCode, revokedCode === AuthErrorCode.ID_TOKEN_REVOKED
        ? 'The Firebase ID token has been revoked.' : 'The Firebase session cookie has been revoked.');
    }
    return decoded;
  }

  /** Exchanges a valid ID token for a session cookie lasting options.expiresIn milliseconds (5 minutes to 14 days). */
  async createSessionCookie(idToken, options) {
    if (typeof idToken !== 'string' || idToken === '') {
      throw authError(AuthErrorCode.INVALID_ID_TOKEN, 'The ID token must be a non-empty string.');
    }
    const ms = options?.expiresIn;
    if (typeof ms !== 'number' || !(ms >= minCookieMs && ms <= maxCookieMs)) {
      throw authError(AuthErrorCode.INVALID_SESSION_COOKIE_DURATION, 'The session cookie duration must be between 5 minutes and 2 weeks.');
    }
    const res = await this.client.call('POST', ':createSessionCookie', { idToken, validDuration: Math.floor(ms / 1000) });
    if (typeof res.sessionCookie !== 'string') {
      throw authError(AuthErrorCode.INTERNAL_ERROR, 'Aruma returned no session cookie.');
    }
    return res.sessionCookie;
  }

  /** The official createCustomToken, unchanged: Aruma accepts its tokens (ADR-004 §5). */
  createCustomToken(uid, developerClaims) {
    return getFirebaseAuth(this.app).createCustomToken(uid, developerClaims);
  }

  async getUser(uid) {
    checkUID(uid);
    return this.lookupOne({ localId: [uid] }, `There is no user record corresponding to the provided identifier.`);
  }

  async getUserByEmail(email) {
    checkEmail(email);
    return this.lookupOne({ email: [email] }, `There is no user record corresponding to the provided email.`);
  }

  async lookupOne(body, notFound) {
    const res = await this.client.call('POST', '/accounts:lookup', body);
    if (!res.users?.length) {
      throw authError(AuthErrorCode.USER_NOT_FOUND, notFound);
    }
    return userRecord(res.users[0]);
  }

  /** Looks up to 100 users by uid, email, phone number or provider uid; missing ones are listed in notFound. */
  async getUsers(identifiers) {
    if (!Array.isArray(identifiers)) {
      throw argumentError('`identifiers` parameter must be an array');
    }
    if (identifiers.length > 100) {
      throw authError(AuthErrorCode.MAXIMUM_USER_COUNT_EXCEEDED, '`identifiers` parameter must have <= 100 entries.');
    }
    const body = {};
    const add = (field, value) => { (body[field] ??= []).push(value); };
    for (const id of identifiers) {
      if (id && 'uid' in id) {
        checkUID(id.uid);
        add('localId', id.uid);
      } else if (id && 'email' in id) {
        checkEmail(id.email);
        add('email', id.email);
      } else if (id && 'phoneNumber' in id) {
        add('phoneNumber', id.phoneNumber);
      } else if (id && 'providerId' in id && 'providerUid' in id) {
        add('federatedUserId', { providerId: id.providerId, rawId: id.providerUid });
      } else {
        throw argumentError(`Unrecognized identifier: ${JSON.stringify(id)}`);
      }
    }
    const res = identifiers.length ? await this.client.call('POST', '/accounts:lookup', body) : {};
    const users = (res.users ?? []).map(userRecord);
    const matches = (id, u) => {
      if ('uid' in id) return id.uid === u.uid;
      if ('email' in id) return typeof u.email === 'string' && id.email.toLowerCase() === u.email.toLowerCase();
      if ('phoneNumber' in id) return id.phoneNumber === u.phoneNumber;
      return u.providerData.some((p) => p.providerId === id.providerId && p.uid === id.providerUid);
    };
    return { users, notFound: identifiers.filter((id) => !users.some((u) => matches(id, u))) };
  }

  /** Creates a user and returns its record. */
  async createUser(properties = {}) {
    const body = this.userRequest(properties, false);
    if (properties.uid !== undefined) {
      checkUID(properties.uid);
      body.localId = properties.uid;
    }
    const res = await this.client.call('POST', '/accounts', body);
    if (!res.localId) {
      throw authError(AuthErrorCode.INTERNAL_ERROR, 'Aruma returned no uid for the new user.');
    }
    return this.getUser(res.localId);
  }

  /** Updates a user and returns its record. null clears displayName, photoURL and phoneNumber. */
  async updateUser(uid, properties = {}) {
    checkUID(uid);
    const body = { localId: uid, ...this.userRequest(properties, true) };
    await this.client.call('POST', '/accounts:update', body);
    return this.getUser(uid);
  }

  // userRequest maps CreateRequest/UpdateRequest to the Identity Toolkit fields.
  userRequest(p, update) {
    if (!p || typeof p !== 'object') {
      throw argumentError('The user properties must be an object.');
    }
    const body = {};
    for (const f of ['email', 'password', 'emailVerified', 'displayName', 'phoneNumber']) {
      if (p[f] !== undefined && p[f] !== null) {
        body[f] = p[f];
      }
    }
    if (p.photoURL !== undefined && p.photoURL !== null) {
      body.photoUrl = p.photoURL;
    }
    if (p.disabled !== undefined) {
      body[update ? 'disableUser' : 'disabled'] = p.disabled;
    }
    if (update) {
      const deleteAttribute = [];
      if (p.displayName === null) deleteAttribute.push('DISPLAY_NAME');
      if (p.photoURL === null) deleteAttribute.push('PHOTO_URL');
      if (deleteAttribute.length) body.deleteAttribute = deleteAttribute;
      const deleteProvider = [...(p.providersToUnlink ?? [])];
      if (p.phoneNumber === null) deleteProvider.push('phone');
      if (deleteProvider.length) body.deleteProvider = deleteProvider;
      if (p.providerToLink) body.linkProviderUserInfo = p.providerToLink;
    }
    if (p.multiFactor) {
      body[update ? 'mfa' : 'mfaInfo'] = p.multiFactor;
    }
    return body;
  }

  async deleteUser(uid) {
    checkUID(uid);
    await this.client.call('POST', '/accounts:delete', { localId: uid });
  }

  /** Deletes up to 1000 users; unknown uids count as deleted, as in firebase-admin. */
  async deleteUsers(uids) {
    if (!Array.isArray(uids)) {
      throw argumentError('`uids` parameter must be an array');
    }
    if (uids.length > 1000) {
      throw authError(AuthErrorCode.MAXIMUM_USER_COUNT_EXCEEDED, '`uids` parameter must have <= 1000 entries.');
    }
    uids.forEach(checkUID);
    const res = await this.client.call('POST', '/accounts:batchDelete', { localIds: uids, force: true });
    const errors = (res.errors ?? []).map((e) => ({
      index: e.index,
      error: authError(String(e.message).startsWith('NOT_DISABLED') ? AuthErrorCode.USER_NOT_DISABLED : AuthErrorCode.INTERNAL_ERROR, e.message),
    }));
    return { successCount: uids.length - errors.length, failureCount: errors.length, errors };
  }

  /** Lists users in pages of up to 1000, in a stable order. */
  async listUsers(maxResults = 1000, pageToken) {
    if (!Number.isInteger(maxResults) || maxResults < 1 || maxResults > 1000) {
      throw argumentError('Required "maxResults" must be a positive integer that does not exceed 1000.');
    }
    if (pageToken !== undefined && (typeof pageToken !== 'string' || pageToken === '')) {
      throw authError(AuthErrorCode.INVALID_PAGE_TOKEN, 'The page token must be a valid non-empty string.');
    }
    const q = new URLSearchParams({ maxResults: String(maxResults) });
    if (pageToken) q.set('nextPageToken', pageToken);
    const res = await this.client.call('GET', `/accounts:batchGet?${q}`);
    const result = { users: (res.users ?? []).map(userRecord) };
    if (res.nextPageToken) result.pageToken = res.nextPageToken;
    return result;
  }

  /** Replaces the user's custom claims; null clears them. They reach ID tokens at the next refresh. */
  async setCustomUserClaims(uid, customUserClaims) {
    checkUID(uid);
    const claims = customUserClaims ?? {};
    if (typeof claims !== 'object' || Array.isArray(claims)) {
      throw argumentError('CustomUserClaims argument must be an object or null.');
    }
    for (const k of Object.keys(claims)) {
      if (reservedClaims.has(k)) {
        throw authError(AuthErrorCode.FORBIDDEN_CLAIM, `Developer claim "${k}" is reserved and cannot be specified.`);
      }
    }
    const json = JSON.stringify(claims);
    if (json.length > 1000) {
      throw authError(AuthErrorCode.CLAIMS_TOO_LARGE, 'Developer claims payload should not exceed 1000 characters.');
    }
    await this.client.call('POST', '/accounts:update', { localId: uid, customAttributes: json });
  }

  /** Revokes every session of the user: refreshes fail and checkRevoked rejects older tokens. */
  async revokeRefreshTokens(uid) {
    checkUID(uid);
    await this.client.call('POST', '/accounts:update', { localId: uid, validSince: Math.floor(Date.now() / 1000) });
  }
}
