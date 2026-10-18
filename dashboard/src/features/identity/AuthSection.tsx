'use client';

import { usePathname } from 'next/navigation';
import { createContext, useContext, type ReactNode } from 'react';
import { useTranslations } from 'use-intl';
import { normalizePath } from '@/components/shell/navigation';
import { PageHeader } from '@/components/ui/PageHeader';
import { LinkTabs } from '@/components/ui/Tabs';
import { useProject } from '@/features/project/ProjectContext';
import * as api from '@/lib/api/console';
import type { Mailbox } from '@/lib/api/types';
import { useResource, type Resource } from '@/lib/hooks/useResource';
import { routes } from '@/lib/routes';

type Tab = 'users' | 'providers' | 'templates' | 'mailbox' | 'settings';

const TAB_BY_PATH: Record<string, Tab> = {
  '/project/auth/': 'users',
  '/project/auth/user/': 'users',
  '/project/auth/providers/': 'providers',
  '/project/auth/templates/': 'templates',
  '/project/auth/mailbox/': 'mailbox',
  '/project/auth/settings/': 'settings',
};

interface AuthSectionValue {
  /** The dev mailbox; a 404 or `enabled: false` means the server sends real email. */
  mailbox: Resource<Mailbox>;
}

const AuthSectionContext = createContext<AuthSectionValue | null>(null);

export function useAuthSection(): AuthSectionValue {
  const value = useContext(AuthSectionContext);
  if (!value) throw new Error('useAuthSection must be used inside AuthSection');
  return value;
}

/**
 * The project's Authentication pages: one heading and a tab per page. The Mailbox tab exists
 * only while the server keeps email in its dev mailbox.
 */
export function AuthSection({ children }: { children: ReactNode }) {
  const t = useTranslations('auth.section');
  const { project } = useProject();
  const id = project.projectId;
  const selected = TAB_BY_PATH[normalizePath(usePathname())] ?? 'users';
  const mailbox = useResource((signal) => api.getMailbox(id, signal), [id]);
  const showMailbox = mailbox.data?.enabled === true || selected === 'mailbox';

  const items = [
    { id: 'users', label: t('users'), href: routes.authUsers(id) },
    { id: 'providers', label: t('providers'), href: routes.authProviders(id) },
    { id: 'templates', label: t('templates'), href: routes.authTemplates(id) },
    ...(showMailbox ? [{ id: 'mailbox', label: t('mailbox'), href: routes.authMailbox(id) }] : []),
    { id: 'settings', label: t('settings'), href: routes.authSettings(id) },
  ];

  return (
    <AuthSectionContext value={{ mailbox }}>
      <PageHeader title={t('title')} subtitle={t('subtitle')} />
      <LinkTabs label={t('sections')} items={items} selected={selected}>
        {children}
      </LinkTabs>
    </AuthSectionContext>
  );
}
