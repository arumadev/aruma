// Typed calls for every console API route the dashboard uses (docs/api/console.md).
import { downloadFile, request, requestEmpty, requestText } from './client';
import type {
  Account,
  ApiKey,
  App,
  AuditEntry,
  AuthConfig,
  AuthConfigUpdate,
  AuthUser,
  AuthUserUpdate,
  EmailTemplate,
  EmailTemplateType,
  FederatedProvider,
  FederatedProviderUpdate,
  FlutterSelection,
  Locale,
  Login,
  Mailbox,
  Me,
  NewApp,
  NewAuthUser,
  Page,
  Project,
  ProjectIdCheck,
  Restrictions,
  ServiceAccount,
  ServiceAccountKey,
  Session,
  ShaCertificate,
  SigningKey,
  SigningKeyPurpose,
  SmtpSettings,
  SmtpUpdate,
  Status,
  Token,
} from './types';

const seg = encodeURIComponent;
const project = (p: string) => `/projects/${seg(p)}`;
const app = (p: string, a: string) => `${project(p)}/apps/${seg(a)}`;
const key = (p: string, k: string) => `${project(p)}/apiKeys/${seg(k)}`;
const sa = (p: string, s: string) => `${project(p)}/serviceAccounts/${seg(s)}`;

export interface PageQuery {
  pageSize?: number;
  pageToken?: string;
  showDeleted?: boolean;
}

export interface Credentials {
  email: string;
  password: string;
}

export interface NewAccount extends Credentials {
  displayName?: string;
  locale?: Locale;
}

// Bootstrap and authentication
export const getStatus = (signal?: AbortSignal) => request<Status>('/status', { signal });
export const setup = (body: NewAccount & { setupCode: string }) => request<Login>('/setup', { method: 'POST', body });
export const login = (body: Credentials) => request<Login>('/auth:login', { method: 'POST', body });
export const signup = (body: NewAccount) => request<Login>('/auth:signup', { method: 'POST', body });
export const logout = () => requestEmpty('/auth:logout', { method: 'POST' });

// The signed-in account
export const getMe = (signal?: AbortSignal) => request<Me>('/me', { signal });
export const updateMe = (body: { displayName?: string; locale?: Locale }) =>
  request<{ account: Account }>('/me', { method: 'PATCH', body });
export const changePassword = (body: { currentPassword: string; newPassword: string }) =>
  requestEmpty('/me:changePassword', { method: 'POST', body });
export const listSessions = (signal?: AbortSignal) => request<{ sessions: Session[] }>('/me/sessions', { signal });
export const revokeSession = (id: string) => requestEmpty(`/me/sessions/${seg(id)}`, { method: 'DELETE' });
export const listTokens = (signal?: AbortSignal) => request<{ tokens: Token[] }>('/me/tokens', { signal });
/** Creating a token re-checks the account password. */
export const createToken = (body: { name: string; expireDays: number; currentPassword: string }) =>
  request<Token>('/me/tokens', { method: 'POST', body });
export const revokeToken = (id: string) => requestEmpty(`/me/tokens/${seg(id)}`, { method: 'DELETE' });

// Projects
export const listProjects = (query: PageQuery, signal?: AbortSignal) =>
  request<{ projects: Project[] } & Page>('/projects', { query: { ...query }, signal });
export const createProject = (body: { projectId: string; displayName: string }) =>
  request<Project>('/projects', { method: 'POST', body });
export const checkProjectId = (projectId: string, signal?: AbortSignal) =>
  request<ProjectIdCheck>('/projects:checkId', { query: { projectId }, signal });
export const suggestProjectId = (displayName: string, signal?: AbortSignal) =>
  request<{ projectId: string }>('/projects:suggestId', { query: { displayName }, signal });
export const getProject = (p: string, signal?: AbortSignal) => request<Project>(project(p), { signal });
export const updateProject = (p: string, body: { displayName?: string; authDomain?: string; etag?: string }) =>
  request<Project>(project(p), { method: 'PATCH', body });
export const deleteProject = (p: string, confirmProjectId: string) =>
  request<Project>(`${project(p)}:delete`, { method: 'POST', body: { confirmProjectId } });
export const restoreProject = (p: string) => request<Project>(`${project(p)}:restore`, { method: 'POST' });
export const listAudit = (p: string, query: PageQuery, signal?: AbortSignal) =>
  request<{ entries: AuditEntry[] } & Page>(`${project(p)}/auditLog`, { query: { ...query }, signal });

// Apps and config files
export const listApps = (p: string, query: PageQuery, signal?: AbortSignal) =>
  request<{ apps: App[] } & Page>(`${project(p)}/apps`, { query: { ...query }, signal });
export const createApp = (p: string, body: NewApp) => request<App>(`${project(p)}/apps`, { method: 'POST', body });
export const getApp = (p: string, a: string, signal?: AbortSignal) => request<App>(app(p, a), { signal });
export const updateApp = (p: string, a: string, body: { displayName: string; etag?: string }) =>
  request<App>(app(p, a), { method: 'PATCH', body });
export const removeApp = (p: string, a: string, etag?: string) =>
  request<App>(`${app(p, a)}:remove`, { method: 'POST', body: { etag } });
export const undeleteApp = (p: string, a: string) => request<App>(`${app(p, a)}:undelete`, { method: 'POST' });
export const downloadAppConfig = (p: string, a: string) => downloadFile(`${app(p, a)}/config`);
export const getWebSnippet = (p: string, a: string, signal?: AbortSignal) =>
  requestText(`${app(p, a)}/config`, { query: { format: 'snippet' }, signal });
export const listSha = (p: string, a: string, signal?: AbortSignal) =>
  request<{ certificates: ShaCertificate[] }>(`${app(p, a)}/sha`, { signal });
export const addSha = (p: string, a: string, shaHash: string) =>
  request<ShaCertificate>(`${app(p, a)}/sha`, { method: 'POST', body: { shaHash } });
export const deleteSha = (p: string, a: string, id: string) => requestEmpty(`${app(p, a)}/sha/${seg(id)}`, { method: 'DELETE' });
export const downloadFlutterOptions = (p: string, selection: FlutterSelection) =>
  downloadFile(`${project(p)}/flutterOptions`, { query: { ...selection } });

// API keys
export const listKeys = (p: string, signal?: AbortSignal) => request<{ keys: ApiKey[] }>(`${project(p)}/apiKeys`, { signal });
export const createKey = (p: string, body: { displayName?: string; restrictions?: Restrictions }) =>
  request<ApiKey>(`${project(p)}/apiKeys`, { method: 'POST', body });
export const updateKey = (p: string, k: string, body: { displayName?: string; restrictions?: Restrictions; etag?: string }) =>
  request<ApiKey>(key(p, k), { method: 'PATCH', body });
export const rotateKey = (p: string, k: string) => request<ApiKey>(`${key(p, k)}:rotate`, { method: 'POST' });
export const setKeyEnabled = (p: string, k: string, enabled: boolean) =>
  request<ApiKey>(`${key(p, k)}:${enabled ? 'enable' : 'disable'}`, { method: 'POST' });

// Service accounts
export const listServiceAccounts = (p: string, signal?: AbortSignal) =>
  request<{ serviceAccounts: ServiceAccount[] }>(`${project(p)}/serviceAccounts`, { signal });
export const createServiceAccount = (p: string, body: { accountId: string; displayName?: string }) =>
  request<ServiceAccount>(`${project(p)}/serviceAccounts`, { method: 'POST', body });
export const deleteServiceAccount = (p: string, s: string) => requestEmpty(sa(p, s), { method: 'DELETE' });
export const listServiceAccountKeys = (p: string, s: string, signal?: AbortSignal) =>
  request<{ keys: ServiceAccountKey[] }>(`${sa(p, s)}/keys`, { signal });
export const generateServiceAccountKey = (p: string, s: string) => downloadFile(`${sa(p, s)}/keys`, { method: 'POST' });
export const deleteServiceAccountKey = (p: string, s: string, keyId: string) =>
  requestEmpty(`${sa(p, s)}/keys/${seg(keyId)}`, { method: 'DELETE' });

// Auth
const auth = (p: string) => `${project(p)}/auth`;
const authUser = (p: string, uid: string) => `${auth(p)}/users/${seg(uid)}`;

export interface AuthUserQuery {
  pageSize?: number;
  pageToken?: string;
  query?: string;
}

export const listAuthUsers = (p: string, query: AuthUserQuery, signal?: AbortSignal) =>
  request<{ users: AuthUser[]; nextPageToken: string; totalSize: number }>(`${auth(p)}/users`, { query: { ...query }, signal });
export const createAuthUser = (p: string, body: NewAuthUser) => request<AuthUser>(`${auth(p)}/users`, { method: 'POST', body });
export const getAuthUser = (p: string, uid: string, signal?: AbortSignal) => request<AuthUser>(authUser(p, uid), { signal });
export const updateAuthUser = (p: string, uid: string, body: AuthUserUpdate) =>
  request<AuthUser>(authUser(p, uid), { method: 'PATCH', body });
export const deleteAuthUser = (p: string, uid: string) => requestEmpty(authUser(p, uid), { method: 'DELETE' });
export const revokeAuthUserSessions = (p: string, uid: string) => request<AuthUser>(`${authUser(p, uid)}:revokeSessions`, { method: 'POST' });
export const sendPasswordReset = (p: string, uid: string) => requestEmpty(`${authUser(p, uid)}:sendPasswordReset`, { method: 'POST' });
export const getAuthConfig = (p: string, signal?: AbortSignal) => request<AuthConfig>(`${auth(p)}/config`, { signal });
export const updateAuthConfig = (p: string, body: AuthConfigUpdate) => request<AuthConfig>(`${auth(p)}/config`, { method: 'PATCH', body });
export const listSigningKeys = (p: string, signal?: AbortSignal) => request<{ keys: SigningKey[] }>(`${auth(p)}/signingKeys`, { signal });
export const rotateSigningKey = (p: string, purpose: SigningKeyPurpose, emergency: boolean) =>
  request<{ keys: SigningKey[] }>(`${auth(p)}/signingKeys:rotate`, { method: 'POST', body: { purpose, emergency } });
export const listFederatedProviders = (p: string, signal?: AbortSignal) =>
  request<{ providers: FederatedProvider[] }>(`${auth(p)}/providers`, { signal });
export const updateFederatedProvider = (p: string, providerId: string, body: FederatedProviderUpdate) =>
  request<FederatedProvider>(`${auth(p)}/providers/${seg(providerId)}`, { method: 'PATCH', body });
export const listEmailTemplates = (p: string, signal?: AbortSignal) => request<{ templates: EmailTemplate[] }>(`${auth(p)}/templates`, { signal });
export const saveEmailTemplate = (p: string, type: EmailTemplateType, language: Locale, body: { subject: string; body: string }) =>
  request<EmailTemplate>(`${auth(p)}/templates/${type}/${language}`, { method: 'PUT', body });
export const resetEmailTemplate = (p: string, type: EmailTemplateType, language: Locale) =>
  requestEmpty(`${auth(p)}/templates/${type}/${language}`, { method: 'DELETE' });
export const getSmtp = (p: string, signal?: AbortSignal) => request<SmtpSettings>(`${auth(p)}/smtp`, { signal });
export const updateSmtp = (p: string, body: SmtpUpdate) => request<SmtpSettings>(`${auth(p)}/smtp`, { method: 'PATCH', body });
export const testSmtp = (p: string, to: string) => requestEmpty(`${auth(p)}/smtp:test`, { method: 'POST', body: { to } });
export const getMailbox = (p: string, signal?: AbortSignal) => request<Mailbox>(`${auth(p)}/mailbox`, { signal });
export const clearMailbox = (p: string) => requestEmpty(`${auth(p)}/mailbox`, { method: 'DELETE' });
