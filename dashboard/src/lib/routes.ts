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
} as const;
