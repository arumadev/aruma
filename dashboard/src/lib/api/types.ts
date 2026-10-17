// Shapes of the console API (/console/v1/), mirroring docs/api/console.md.

export type Locale = 'ar' | 'en';

export interface Limits {
  servers: number;
  maxServiceAccountKeys: number;
  deletedRetentionDays: number;
}

export interface Status {
  setupRequired: boolean;
  signupOpen: boolean;
  version: string;
  limits: Limits;
}

export interface Account {
  id: string;
  email: string;
  displayName: string;
  locale: Locale;
  createTime: string;
}

export interface Login {
  account: Account;
  csrfToken: string;
  expireTime: string;
}

export interface Me {
  account: Account;
  csrfToken?: string;
}

export interface Session {
  id: string;
  current: boolean;
  ip?: string;
  userAgent?: string;
  createTime: string;
  lastSeenTime: string;
  expireTime: string;
}

export interface Token {
  id: string;
  name: string;
  prefix: string;
  createTime: string;
  lastUsedTime?: string;
  expireTime?: string;
  /** The plaintext token, present only in the response that created it. */
  token?: string;
}

export type ResourceState = 'ACTIVE' | 'DELETED';

export interface Project {
  name: string;
  projectId: string;
  projectNumber: string;
  displayName: string;
  state: ResourceState;
  etag: string;
  authDomain: string;
  storageBucket: string;
  createTime: string;
  updateTime: string;
  deleteTime?: string;
  expireTime?: string;
}

export interface Page {
  nextPageToken: string;
}

export interface ProjectIdCheck {
  projectId: string;
  valid: boolean;
  available: boolean;
}

export interface AuditEntry {
  id: string;
  time: string;
  actorType: 'account' | 'service_account';
  actorId?: string;
  action: string;
  target?: string;
  result: string;
  ip?: string;
  details: Record<string, unknown>;
}

export type Platform = 'WEB' | 'IOS' | 'ANDROID';

export interface App {
  name: string;
  appId: string;
  platform: Platform;
  displayName: string;
  packageName?: string;
  bundleId?: string;
  apiKeyId?: string;
  state: ResourceState;
  etag: string;
  createTime: string;
  updateTime: string;
  deleteTime?: string;
  expireTime?: string;
}

export interface NewApp {
  platform: Platform;
  displayName?: string;
  packageName?: string;
  bundleId?: string;
}

export interface ShaCertificate {
  name: string;
  id: string;
  shaHash: string;
  certType: 'SHA_1' | 'SHA_256';
}

export interface AndroidApplication {
  sha1Fingerprint: string;
  packageName: string;
}

export interface Restrictions {
  browserKeyRestrictions?: { allowedReferrers: string[] };
  androidKeyRestrictions?: { allowedApplications: AndroidApplication[] };
  iosKeyRestrictions?: { allowedBundleIds: string[] };
  apiTargets?: { service: string }[];
}

export interface ApiKey {
  name: string;
  uid: string;
  keyString: string;
  displayName: string;
  restrictions: Restrictions;
  state: 'ACTIVE' | 'DISABLED';
  platform?: Platform;
  rotatedFrom?: string;
  etag: string;
  createTime: string;
  updateTime: string;
}

export interface ServiceAccount {
  name: string;
  id: string;
  email: string;
  uniqueId: string;
  displayName: string;
  disabled: boolean;
  createTime: string;
}

export interface ServiceAccountKey {
  keyId: string;
  keyAlgorithm: string;
  validAfterTime: string;
  validBeforeTime?: string;
  disabled: boolean;
  createTime: string;
}

export interface FlutterSelection {
  web?: string;
  android?: string;
  ios?: string;
}
