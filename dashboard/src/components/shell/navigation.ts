import type { IconName } from '@/components/ui/Icon';
import { routes } from '@/lib/routes';

export interface NavItem {
  id: string;
  href: string;
  icon: IconName;
  /** Key in the "nav" message namespace. */
  label: 'projects' | 'overview' | 'auth' | 'documents' | 'database' | 'storage' | 'keys' | 'serviceAccounts' | 'audit' | 'settings' | 'account' | 'limits';
  /** Pathnames that highlight this item. */
  paths: string[];
  /** Key in the "nav" message namespace of the heading this item is listed under. */
  group?: 'groupBuild' | 'groupProject';
}

// The Auth section's pages, whose tabs share one destination.
const AUTH_PATHS = [
  '/project/auth/',
  '/project/auth/user/',
  '/project/auth/providers/',
  '/project/auth/templates/',
  '/project/auth/mailbox/',
  '/project/auth/settings/',
];

/**
 * Destinations: the project's pages inside a project, the account-wide ones elsewhere. Inside a
 * project the first item leads back to the list of projects.
 */
export function navItems(projectId: string | null): NavItem[] {
  const projects: NavItem = { id: 'projects', href: routes.projects, icon: 'folder', label: 'projects', paths: ['/projects/'] };
  if (!projectId) {
    return [
      projects,
      { id: 'account', href: routes.account, icon: 'person', label: 'account', paths: ['/account/'] },
      { id: 'limits', href: routes.limits, icon: 'speed', label: 'limits', paths: ['/limits/'] },
    ];
  }
  return [
    projects,
    {
      id: 'overview',
      href: routes.project(projectId),
      icon: 'home',
      label: 'overview',
      paths: ['/project/', '/project/app/', '/project/apps/new/'],
    },
    {
      id: 'auth',
      href: routes.authUsers(projectId),
      icon: 'shield',
      label: 'auth',
      paths: AUTH_PATHS,
      group: 'groupBuild',
    },
    {
      id: 'documents',
      href: routes.documents(projectId),
      icon: 'description',
      label: 'documents',
      paths: ['/project/documents/', '/project/documents/rules/', '/project/documents/indexes/', '/project/documents/usage/'],
      group: 'groupBuild',
    },
    {
      id: 'database',
      href: routes.database(projectId),
      icon: 'bolt',
      label: 'database',
      paths: ['/project/database/', '/project/database/rules/'],
      group: 'groupBuild',
    },
    {
      id: 'storage',
      href: routes.storage(projectId),
      icon: 'cloud',
      label: 'storage',
      paths: ['/project/storage/', '/project/storage/object/', '/project/storage/rules/', '/project/storage/usage/', '/project/storage/cors/'],
      group: 'groupBuild',
    },
    { id: 'keys', href: routes.keys(projectId), icon: 'key', label: 'keys', paths: ['/project/keys/'], group: 'groupProject' },
    {
      id: 'serviceAccounts',
      href: routes.serviceAccounts(projectId),
      icon: 'team',
      label: 'serviceAccounts',
      paths: ['/project/service-accounts/'],
      group: 'groupProject',
    },
    { id: 'audit', href: routes.audit(projectId), icon: 'history', label: 'audit', paths: ['/project/audit/'], group: 'groupProject' },
    {
      id: 'settings',
      href: routes.settings(projectId),
      icon: 'settings',
      label: 'settings',
      paths: ['/project/settings/'],
      group: 'groupProject',
    },
  ];
}

/** Normalizes a pathname to the trailing-slash form the export uses. */
export const normalizePath = (pathname: string) => (pathname.endsWith('/') ? pathname : `${pathname}/`);
