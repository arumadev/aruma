// Every dashboard URL. The export is static, so ids travel in the query string.

const withQuery = (path: string, query: Record<string, string>) => `${path}?${new URLSearchParams(query).toString()}`;

export const routes = {
  home: '/',
  setup: '/setup/',
  login: '/login/',
  signup: '/signup/',
  projects: '/projects/',
  account: '/account/',
  limits: '/limits/',
  loginThen: (next: string) => withQuery('/login/', { next }),
  project: (id: string) => withQuery('/project/', { id }),
  newApp: (id: string) => withQuery('/project/apps/new/', { id }),
  app: (id: string, app: string) => withQuery('/project/app/', { id, app }),
  keys: (id: string) => withQuery('/project/keys/', { id }),
  serviceAccounts: (id: string) => withQuery('/project/service-accounts/', { id }),
  settings: (id: string) => withQuery('/project/settings/', { id }),
  audit: (id: string) => withQuery('/project/audit/', { id }),
  authUsers: (id: string) => withQuery('/project/auth/', { id }),
  authUser: (id: string, uid: string) => withQuery('/project/auth/user/', { id, uid }),
  authProviders: (id: string) => withQuery('/project/auth/providers/', { id }),
  authTemplates: (id: string) => withQuery('/project/auth/templates/', { id }),
  authMailbox: (id: string) => withQuery('/project/auth/mailbox/', { id }),
  authSettings: (id: string) => withQuery('/project/auth/settings/', { id }),
  database: (id: string) => withQuery('/project/database/', { id }),
  databaseRules: (id: string) => withQuery('/project/database/rules/', { id }),
  /** A folder of the bucket: "" for the root, else a prefix ending in "/". */
  storage: (id: string, prefix = '') => withQuery('/project/storage/', prefix ? { id, prefix } : { id }),
  storageObject: (id: string, name: string) => withQuery('/project/storage/object/', { id, name }),
  storageRules: (id: string) => withQuery('/project/storage/rules/', { id }),
  storageUsage: (id: string) => withQuery('/project/storage/usage/', { id }),
  storageCors: (id: string) => withQuery('/project/storage/cors/', { id }),
  /** The data browser at a path below the documents root: "" (the root), a collection or a document. */
  documents: (id: string, path = '') => withQuery('/project/documents/', path ? { id, path } : { id }),
  documentsRules: (id: string) => withQuery('/project/documents/rules/', { id }),
  documentsIndexes: (id: string) => withQuery('/project/documents/indexes/', { id }),
  documentsUsage: (id: string) => withQuery('/project/documents/usage/', { id }),
} as const;
