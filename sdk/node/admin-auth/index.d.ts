// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

import type { App } from 'firebase-admin/app';
import type {
  CreateRequest,
  DecodedIdToken,
  DeleteUsersResult,
  GetUsersResult,
  ListUsersResult,
  SessionCookieOptions,
  UpdateRequest,
  UserIdentifier,
  UserRecord,
} from 'firebase-admin/auth';

/** A service account key file as Aruma issues it, or its path. */
export type ServiceAccountKey = string | {
  project_id?: string;
  client_email: string;
  private_key: string;
  private_key_id?: string;
  token_uri?: string;
};

export interface ArumaAuthOptions {
  /**
   * The Aruma server's public URL, such as https://aruma.example.com. Defaults to ARUMA_URL,
   * then to the origin of the key file's token_uri.
   */
  url?: string;
  /** The service account key. Defaults to the app's cert() credential, then GOOGLE_APPLICATION_CREDENTIALS. */
  credential?: ServiceAccountKey;
  /** Start even though FIREBASE_AUTH_EMULATOR_HOST is set. Never honoured when NODE_ENV is production. */
  allowEmulatorHost?: boolean;
  /** A fetch implementation, for proxies and tests. */
  fetch?: typeof fetch;
}

/** The subset of firebase-admin's BaseAuth that Aruma answers, with the same signatures. */
export declare class ArumaAuth {
  private constructor();
  readonly app: App;
  /** The project the instance acts on. */
  readonly projectId: string;
  /** The Aruma server's public URL. */
  readonly url: string;
  verifyIdToken(idToken: string, checkRevoked?: boolean): Promise<DecodedIdToken>;
  verifySessionCookie(sessionCookie: string, checkRevoked?: boolean): Promise<DecodedIdToken>;
  createSessionCookie(idToken: string, sessionCookieOptions: SessionCookieOptions): Promise<string>;
  createCustomToken(uid: string, developerClaims?: object): Promise<string>;
  createUser(properties: CreateRequest): Promise<UserRecord>;
  getUser(uid: string): Promise<UserRecord>;
  getUserByEmail(email: string): Promise<UserRecord>;
  getUsers(identifiers: UserIdentifier[]): Promise<GetUsersResult>;
  updateUser(uid: string, properties: UpdateRequest): Promise<UserRecord>;
  deleteUser(uid: string): Promise<void>;
  deleteUsers(uids: string[]): Promise<DeleteUsersResult>;
  listUsers(maxResults?: number, pageToken?: string): Promise<ListUsersResult>;
  setCustomUserClaims(uid: string, customUserClaims: object | null): Promise<void>;
  revokeRefreshTokens(uid: string): Promise<void>;
}

/** Returns Aruma Auth for the app, or the default app. */
export declare function getAuth(app?: App, options?: ArumaAuthOptions): ArumaAuth;
