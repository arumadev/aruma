// The Document Database part of the console API: /projects/{project}/documents/… (ADR-030).
// Documents and collections are named by ?path=, relative to the documents root ("users/u1",
// "users/u1/posts"), never by a path segment, so any id reaches the server as written. Values
// travel in Firestore's REST form ({"stringValue": …}), so every type is shown and edited exactly.
import { request } from '@/lib/api/client';

export interface DocumentsLimits {
  /** A collection's page of documents. */
  pageSize: number;
  /** A page of collection ids (the Firebase console's listCollectionIds pageSize, 300). */
  collectionsPageSize: number;
  maxPageSize: number;
  documentBytes: number;
  fieldDepth: number;
  requestBytes: number;
  /** The most documents one recursive delete request removes; the dashboard repeats it until done. */
  recursiveDeleteDocs: number;
}

/** The project's database, and whether this server serves the Document Database at all. */
export interface DocumentsInfo {
  enabled: boolean;
  databaseId: string;
  name: string;
  limits?: DocumentsLimits;
}

/** A Firestore value in its REST form: exactly one of these keys is set. */
export type Value =
  | { nullValue: null }
  | { booleanValue: boolean }
  /** An int64, as a decimal string so it keeps all 64 bits. */
  | { integerValue: string }
  /** A double; NaN and the infinities travel as the strings "NaN", "Infinity", "-Infinity". */
  | { doubleValue: number | string }
  /** RFC 3339, in UTC, up to nanoseconds. */
  | { timestampValue: string }
  | { stringValue: string }
  /** Base64. */
  | { bytesValue: string }
  /** A full document name, projects/{p}/databases/{d}/documents/…. */
  | { referenceValue: string }
  | { geoPointValue: { latitude?: number; longitude?: number } }
  | { arrayValue: { values?: Value[] } }
  | { mapValue: { fields?: Record<string, Value> } };

export type Fields = Record<string, Value>;

/** A document as the dashboard reads it: the REST Document with its path, its id and missing. */
export interface Doc {
  name: string;
  /** The path below the documents root, "users/u1". */
  path: string;
  id: string;
  /** A document that does not exist but has subcollections: listed by id alone, in italics. */
  missing: boolean;
  fields?: Fields;
  createTime?: string;
  updateTime?: string;
}

export interface CollectionPage {
  collectionIds: string[];
  nextPageToken?: string;
}

export interface DocumentPage {
  documents: Doc[];
  nextPageToken?: string;
}

/** A write's body. */
export interface DocumentWrite {
  /** A new document's id; empty, the server makes one (20 characters, as the SDKs). */
  documentId?: string;
  fields?: Fields;
  /** The field paths an edit sets (backquoted where they must be); a path named and absent from fields is deleted. */
  updateMask?: string[];
  /** The edit's precondition: the document must (not) exist. */
  exists?: boolean;
  /** The edit's precondition instead: the updateTime the editor read, so an edit over another's fails. */
  updateTime?: string;
}

export interface RecursiveDeleteResult {
  deleted: number;
  /** False while documents remain: the dashboard repeats the request. */
  done: boolean;
}

export interface RuleIssue {
  sourcePosition?: { fileName: string; line: number; column: number; currentOffset: number; endOffset: number };
  description: string;
  severity: 'ERROR' | 'WARNING' | 'SEVERITY_UNSPECIFIED';
}

export interface PublishedRules {
  /** False while nothing was ever published (the locked default runs). */
  published: boolean;
  source: string;
  rulesetName?: string;
  updateTime?: string;
  /** The warnings of a source just published. */
  issues?: RuleIssue[];
}

export interface RulesValidation {
  valid: boolean;
  issues: RuleIssue[];
}

export interface RulesetEntry {
  name: string;
  createTime: string;
  /** The ruleset the release serves now. */
  current: boolean;
}

export interface RulesetPage {
  rulesets: RulesetEntry[];
  nextPageToken?: string;
}

export interface Ruleset {
  name: string;
  createTime: string;
  source: string;
}

export type QueryScope = 'COLLECTION' | 'COLLECTION_GROUP';
export type IndexOrder = 'ASCENDING' | 'DESCENDING';

/** One field of an index: an order, or arrayConfig CONTAINS. */
export interface IndexField {
  fieldPath?: string;
  order?: IndexOrder;
  arrayConfig?: 'CONTAINS';
}

/** An Admin API Index in its REST form. */
export interface Index {
  name?: string;
  queryScope: QueryScope;
  apiScope?: string;
  fields: IndexField[];
  state?: 'STATE_UNSPECIFIED' | 'CREATING' | 'READY' | 'NEEDS_REPAIR';
}

export interface IndexConfig {
  indexes?: Index[];
  usesAncestorConfig?: boolean;
  ancestorField?: string;
  reverting?: boolean;
}

export interface TtlConfig {
  state?: 'STATE_UNSPECIFIED' | 'CREATING' | 'ACTIVE' | 'NEEDS_REPAIR';
}

/** An Admin API Field: projects/{p}/databases/(default)/collectionGroups/{g}/fields/{f}. */
export interface Field {
  name: string;
  indexConfig?: IndexConfig;
  ttlConfig?: TtlConfig;
}

/** A long-running operation of an index build or a field change. */
export interface Operation {
  name: string;
  done?: boolean;
  metadata?: Record<string, unknown> & { '@type'?: string; state?: string; startTime?: string; endTime?: string };
  error?: { code: number; message: string };
  response?: Record<string, unknown>;
}

/** What the server counts for the project's database; available is false while it counts nothing. */
export interface DocumentsUsage {
  available: boolean;
  counters: Record<string, number>;
  note?: string;
}

const docs = (p: string) => `/projects/${encodeURIComponent(p)}/documents`;

export const getInfo = (p: string, signal?: AbortSignal) => request<DocumentsInfo>(docs(p), { signal });

// The data browser.

/** The collection ids under a document, or under the root when path is "". */
export const listCollections = (p: string, path: string, query: { pageSize?: number; pageToken?: string } = {}, signal?: AbortSignal) =>
  request<CollectionPage>(`${docs(p)}/collections`, { query: { path, ...query }, signal });

/** A page of a collection, in id order, missing documents (with subcollections) included. */
export const listDocuments = (
  p: string,
  path: string,
  query: { pageSize?: number; pageToken?: string; fields?: boolean } = {},
  signal?: AbortSignal,
) =>
  request<DocumentPage>(`${docs(p)}/documents`, {
    query: { path, pageSize: query.pageSize, pageToken: query.pageToken, fields: query.fields === false ? 'false' : undefined },
    signal,
  });

export const getDocument = (p: string, path: string, signal?: AbortSignal) => request<Doc>(`${docs(p)}/document`, { query: { path }, signal });

/** Creates a document in the collection at path; one that exists already is ALREADY_EXISTS. */
export const createDocument = (p: string, collectionPath: string, body: { documentId?: string; fields: Fields }) =>
  request<Doc>(`${docs(p)}/documents`, { method: 'POST', query: { path: collectionPath }, body });

export const updateDocument = (p: string, path: string, body: DocumentWrite) =>
  request<Doc>(`${docs(p)}/document`, { method: 'PATCH', query: { path }, body });

/** Deletes a document, not its subcollections. */
export const deleteDocument = (p: string, path: string) => request<object>(`${docs(p)}/document`, { method: 'DELETE', query: { path } });

/** Deletes up to limits.recursiveDeleteDocs documents at and under a document or collection. */
export const recursiveDeleteStep = (p: string, path: string) =>
  request<RecursiveDeleteResult>(`${docs(p)}/documents:recursiveDelete`, { method: 'POST', body: { path } });

// Rules.

export const getRules = (p: string, signal?: AbortSignal) => request<PublishedRules>(`${docs(p)}/rules`, { signal });

export const publishRules = (p: string, source: string) => request<PublishedRules>(`${docs(p)}/rules`, { method: 'PUT', body: { source } });

export const validateRules = (p: string, source: string, signal?: AbortSignal) =>
  request<RulesValidation>(`${docs(p)}/rules:validate`, { method: 'POST', body: { source }, signal });

export const listRulesets = (p: string, query: { pageSize?: number; pageToken?: string } = {}, signal?: AbortSignal) =>
  request<RulesetPage>(`${docs(p)}/rulesets`, { query, signal });

/** A ruleset by its full name (projects/{p}/rulesets/{id}) or id. */
export const getRuleset = (p: string, name: string, signal?: AbortSignal) =>
  request<Ruleset>(`${docs(p)}/rulesets/${encodeURIComponent(name.split('/').pop() ?? name)}`, { signal });

// Indexes, exemptions and TTL.

export const listIndexes = (p: string, signal?: AbortSignal) => request<{ indexes: Index[] }>(`${docs(p)}/indexes`, { signal });

export const createIndex = (p: string, collectionGroup: string, index: Index) =>
  request<Operation>(`${docs(p)}/indexes`, { method: 'POST', body: { collectionGroup, index } });

/** Deletes a composite index by its name, …/collectionGroups/{group}/indexes/{id}. */
export function deleteIndex(p: string, name: string) {
  const m = /collectionGroups\/([^/]+)\/indexes\/([^/]+)$/.exec(name);
  const [, group, id] = m ?? [];
  if (!group || !id) throw new Error(`not an index name: ${name}`);
  return request<object>(`${docs(p)}/indexes/${encodeURIComponent(group)}/${encodeURIComponent(id)}`, { method: 'DELETE' });
}

/** The single-field exemptions (indexConfig.usesAncestorConfig=false), the __default__ ones among them. */
export const listExemptions = (p: string, signal?: AbortSignal) => request<{ fields: Field[] }>(`${docs(p)}/fields`, { signal });

/** Sets a field's own indexes: only the enabled ones are listed; none exempts it from indexing. */
export const setExemption = (p: string, group: string, field: string, indexConfig: { indexes: Index[] }) =>
  request<Operation>(`${docs(p)}/fields`, { method: 'PUT', query: { group, field }, body: { indexConfig } });

/** Returns a field's indexes to its ancestor's. */
export const clearExemption = (p: string, group: string, field: string) =>
  request<Operation>(`${docs(p)}/fields`, { method: 'DELETE', query: { group, field } });

export const listTtlPolicies = (p: string, signal?: AbortSignal) => request<{ fields: Field[] }>(`${docs(p)}/ttl`, { signal });

/** Makes field the TTL field of its collection group (one policy per group). */
export const setTtlPolicy = (p: string, group: string, field: string) =>
  request<Operation>(`${docs(p)}/ttl`, { method: 'PUT', query: { group, field }, body: {} });

export const clearTtlPolicy = (p: string, group: string, field: string) =>
  request<Operation>(`${docs(p)}/ttl`, { method: 'DELETE', query: { group, field } });

export const listOperations = (p: string, signal?: AbortSignal) => request<{ operations: Operation[] }>(`${docs(p)}/operations`, { signal });

// Usage.

export const getUsage = (p: string, signal?: AbortSignal) => request<DocumentsUsage>(`${docs(p)}/usage`, { signal });
