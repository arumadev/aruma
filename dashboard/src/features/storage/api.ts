// The Storage part of the console API: /projects/{project}/storage/… (ADR-022 §15). Objects are
// named by ?name=, folders by ?prefix=; the bytes of an upload never go through these routes,
// only the small request that issues its session (see upload.ts).
import { apiUrl, downloadFile, request, send } from '@/lib/api/client';

export interface StorageLimits {
  objectBytes: number;
  /** The project's quota, staging included; 0 is unlimited. */
  projectBytes: number;
  metadataBytes: number;
  headerFieldBytes: number;
  listMaxResults: number;
  uploadSessionSeconds: number;
  requestBytes: number;
}

/** The project's bucket, and whether this server serves Storage at all. */
export interface StorageInfo {
  bucket: string;
  enabled: boolean;
  limits?: StorageLimits;
}

/** The content fields a file is stored with, by their names on both Storage APIs. */
export const CONTENT_FIELDS = ['contentType', 'cacheControl', 'contentDisposition', 'contentEncoding', 'contentLanguage'] as const;
export type ContentField = (typeof CONTENT_FIELDS)[number];

export interface StoredObject extends Partial<Record<ContentField, string>> {
  name: string;
  bucket: string;
  size: number;
  metadata: Record<string, string>;
  downloadTokens: string[];
  /** One download link per token: whoever has one reads the file, whatever the rules say. */
  downloadUrls: string[];
  generation: string;
  metageneration: string;
  md5Hash: string;
  crc32c: string;
  timeCreated: string;
  updated: string;
}

/** One page of a folder: its subfolders (each ending in "/") and the files directly in it. */
export interface Listing {
  prefixes: string[];
  items: StoredObject[];
  nextPageToken?: string;
}

/** A metadata edit: a value sets a field, null removes it; in metadata, null removes a key. */
export type ObjectPatch = Partial<Record<ContentField, string | null>> & { metadata?: Record<string, string | null> | null };

export interface UploadSession {
  /** The session's /v0 URL. It is the session's only credential: it is never logged or kept. */
  uploadUrl: string;
  bucket: string;
  name: string;
  size: number;
  /** The first byte must arrive by then, or the session expires and another must be issued. */
  startBy: string;
  /** The upload must finish by then. */
  expireTime: string;
}

export interface UploadRequest extends Partial<Record<ContentField, string>> {
  name: string;
  size: number;
  metadata?: Record<string, string>;
}

export interface Usage {
  usedBytes: number;
  reservedBytes: number;
  objects: number;
  /** 0 is unlimited. */
  quotaBytes: number;
}

export interface CorsRule {
  origin: string[];
  method: string[];
  responseHeader: string[];
  maxAgeSeconds: number;
}

export interface RuleIssue {
  sourcePosition?: { fileName: string; line: number; column: number; currentOffset: number; endOffset: number };
  description: string;
  severity: 'ERROR' | 'WARNING' | 'SEVERITY_UNSPECIFIED';
}

export interface PublishedRules {
  /** False while the project runs the locked default: nothing was ever published. */
  published: boolean;
  source: string;
  rulesetName?: string;
  updateTime?: string;
  /** The warnings of a source just published. */
  issues?: RuleIssue[];
}

const storage = (p: string) => `/projects/${encodeURIComponent(p)}/storage`;

export const getStorage = (p: string, signal?: AbortSignal) => request<StorageInfo>(storage(p), { signal });

export const browse = (p: string, prefix: string, query: { pageSize?: number; pageToken?: string }, signal?: AbortSignal) =>
  request<Listing>(`${storage(p)}/objects`, { query: { prefix, ...query }, signal });

export const getObject = (p: string, name: string, signal?: AbortSignal) => request<StoredObject>(`${storage(p)}/object`, { query: { name }, signal });

export const updateObject = (p: string, name: string, patch: ObjectPatch) =>
  request<StoredObject>(`${storage(p)}/object`, { method: 'PATCH', query: { name }, body: patch });

export const deleteObject = (p: string, name: string) => request<object>(`${storage(p)}/object`, { method: 'DELETE', query: { name } });

export const createToken = (p: string, name: string) => request<StoredObject>(`${storage(p)}/object:createToken`, { method: 'POST', query: { name }, body: {} });

/** The token goes in the body, so it never reaches a URL or a request log. */
export const deleteToken = (p: string, name: string, token: string) =>
  request<StoredObject>(`${storage(p)}/object:deleteToken`, { method: 'POST', query: { name }, body: { token } });

/** The console's download: always an attachment, with the session cookie, on this origin. */
export const downloadPath = (p: string, name: string) => apiUrl(`${storage(p)}/object:download`, { name });

export const downloadObject = (p: string, name: string) =>
  downloadFile(`${storage(p)}/object:download`, { query: { name }, fallbackName: name.split('/').pop() || 'download' });

/** An issued session, with the server's clock when it answered (its Date header), if it said. */
export interface IssuedSession extends UploadSession {
  serverTime?: number;
}

export async function issueUpload(p: string, req: UploadRequest, signal?: AbortSignal): Promise<IssuedSession> {
  const res = await send(`${storage(p)}/uploads`, { method: 'POST', body: req, signal });
  const date = Date.parse(res.headers.get('Date') ?? '');
  const session = (await res.json()) as UploadSession;
  return Number.isFinite(date) ? { ...session, serverTime: date } : session;
}

export const getUsage = (p: string, signal?: AbortSignal) => request<Usage>(`${storage(p)}/usage`, { signal });

export const getCors = (p: string, signal?: AbortSignal) => request<{ cors: CorsRule[] }>(`${storage(p)}/cors`, { signal });

export const setCors = (p: string, cors: CorsRule[]) => request<{ cors: CorsRule[] }>(`${storage(p)}/cors`, { method: 'PUT', body: { cors } });

export const getRules = (p: string, signal?: AbortSignal) => request<PublishedRules>(`${storage(p)}/rules`, { signal });

export const publishRules = (p: string, source: string) => request<PublishedRules>(`${storage(p)}/rules`, { method: 'PUT', body: { source } });

export const validateRules = (p: string, source: string, signal?: AbortSignal) =>
  request<{ valid: boolean; issues: RuleIssue[] }>(`${storage(p)}/rules:validate`, { method: 'POST', body: { source }, signal });
