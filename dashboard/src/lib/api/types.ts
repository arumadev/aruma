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

// Auth: a project's users and sign-in settings (docs/api/console.md). The provider, email
// template, SMTP and mailbox routes come from the provider and email work; their shapes are
// noted in dashboard/INTEGRATION.md.

export interface AuthProviderInfo {
  providerId: string;
  email?: string;
}

export interface AuthUser {
  uid: string;
  email?: string;
  emailVerified: boolean;
  displayName?: string;
  photoUrl?: string;
  disabled: boolean;
  providers: AuthProviderInfo[];
  /** The custom claims object as JSON text. */
  customClaims?: string;
  hasPassword: boolean;
  createTime: string;
  lastSignInTime?: string;
  lastRefreshTime?: string;
  tokensValidAfterTime: string;
}

export interface NewAuthUser {
  uid?: string;
  email?: string;
  password?: string;
  displayName?: string;
}

export interface AuthUserUpdate {
  email?: string;
  password?: string;
  displayName?: string;
  photoUrl?: string;
  emailVerified?: boolean;
  disabled?: boolean;
  customClaims?: string;
}

/** Values no setting changes, shown for reference (seconds unless named otherwise). */
export interface AuthLimits {
  idTokenLifetimeSeconds: number;
  sessionCookieMaxSeconds: number;
  signingKeyRotationDays: number;
  requestsPerMinute: number;
  requestBurst: number;
  signUpsPerMinute: number;
  signUpBurst: number;
  passwordFailures: number;
  passwordFailureWindowSeconds: number;
  passwordBackoffMaxSeconds: number;
  /** How long each kind of email code stays valid, once the server reports it. */
  emailCodeLifetimeSeconds?: Partial<Record<EmailTemplateType, number>>;
}

export type PasswordEnforcement = 'OFF' | 'ENFORCE';

export interface AuthConfig {
  passwordEnabled: boolean;
  anonymousEnabled: boolean;
  clientSignupEnabled: boolean;
  enumerationProtection: boolean;
  authorizedDomains: string[];
  passwordMinLength: number;
  passwordRequireLowercase: boolean;
  passwordRequireUppercase: boolean;
  passwordRequireNumeric: boolean;
  passwordRequireSymbol: boolean;
  passwordEnforcement: PasswordEnforcement;
  appName: string;
  updateTime: string;
  limits: AuthLimits;
}

export type AuthConfigUpdate = Partial<Omit<AuthConfig, 'updateTime' | 'limits'>>;

export type SigningKeyPurpose = 'id_token' | 'session_cookie';

export interface SigningKey {
  kid: string;
  purpose: SigningKeyPurpose;
  state: 'next' | 'active' | 'retired';
  createTime: string;
  activateTime?: string;
  retireTime?: string;
}

/** A federated provider. Secrets are write-only: the server says only whether one is saved. */
export interface FederatedProvider {
  providerId: 'google.com' | 'apple.com';
  enabled: boolean;
  /** Where the provider sends users back; registered with Google or Apple. */
  redirectUri: string;
  clientId?: string;
  clientSecretSet?: boolean;
  servicesId?: string;
  teamId?: string;
  keyId?: string;
  privateKeySet?: boolean;
}

export interface FederatedProviderUpdate {
  enabled?: boolean;
  clientId?: string;
  clientSecret?: string;
  servicesId?: string;
  teamId?: string;
  keyId?: string;
  privateKey?: string;
}

export type EmailTemplateType = 'VERIFY_EMAIL' | 'PASSWORD_RESET' | 'VERIFY_AND_CHANGE_EMAIL' | 'RECOVER_EMAIL';

export interface EmailTemplate {
  type: EmailTemplateType;
  language: Locale;
  subject: string;
  body: string;
  /** False while the built-in default is in use. */
  customized: boolean;
}

export type SmtpSecurity = 'starttls' | 'tls' | 'none';

export interface SmtpSettings {
  host: string;
  port: number;
  username: string;
  passwordSet: boolean;
  security: SmtpSecurity;
  senderName: string;
  senderAddress: string;
  replyTo: string;
}

export type SmtpUpdate = Partial<Omit<SmtpSettings, 'passwordSet'>> & { password?: string };

export interface MailboxMessage {
  id: string;
  to: string;
  type?: EmailTemplateType;
  subject: string;
  /** The action link the email carries. */
  link?: string;
  createTime: string;
}

export interface Mailbox {
  enabled: boolean;
  messages: MailboxMessage[];
}
