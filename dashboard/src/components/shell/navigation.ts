import type { IconName } from '@/components/ui/Icon';
import { routes } from '@/lib/routes';

export interface NavItem {
  id: string;
  href: string;
  icon: IconName;
  /** Key in the "nav" message namespace. */
  label: 'projects' | 'overview' | 'keys' | 'serviceAccounts' | 'audit' | 'settings' | 'account' | 'limits';
  /** Pathnames that highlight this item. */
  paths: string[];
}

/** Destinations: the project's pages inside a project, the account-wide ones elsewhere. */
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
    { id: 'keys', href: routes.keys(projectId), icon: 'key', label: 'keys', paths: ['/project/keys/'] },
    {
      id: 'serviceAccounts',
      href: routes.serviceAccounts(projectId),
      icon: 'team',
      label: 'serviceAccounts',
      paths: ['/project/service-accounts/'],
    },
    { id: 'audit', href: routes.audit(projectId), icon: 'history', label: 'audit', paths: ['/project/audit/'] },
    { id: 'settings', href: routes.settings(projectId), icon: 'settings', label: 'settings', paths: ['/project/settings/'] },
  ];
}

/** Normalizes a pathname to the trailing-slash form the export uses. */
export const normalizePath = (pathname: string) => (pathname.endsWith('/') ? pathname : `${pathname}/`);
