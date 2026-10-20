'use client';

import { usePathname } from 'next/navigation';
import { createContext, useContext, type ReactNode } from 'react';
import { useTranslations } from 'use-intl';
import { Banner } from '@/components/ui/Banner';
import { Button } from '@/components/ui/Button';
import { CopyButton, Ltr } from '@/components/ui/Code';
import { PageHeader } from '@/components/ui/PageHeader';
import { Skeleton } from '@/components/ui/Skeleton';
import { LinkTabs } from '@/components/ui/Tabs';
import { normalizePath } from '@/components/shell/navigation';
import { useProject } from '@/features/project/ProjectContext';
import * as api from '@/lib/api/console';
import type { DatabaseInfo, DatabaseLimits } from '@/lib/api/types';
import { useErrorText } from '@/lib/errors';
import { useResource, type Resource } from '@/lib/hooks/useResource';
import { routes } from '@/lib/routes';
import styles from './DatabaseSection.module.css';
import { DEFAULT_LIMITS } from './validation';

type Tab = 'data' | 'rules';

const TAB_BY_PATH: Record<string, Tab> = {
  '/project/database/': 'data',
  '/project/database/rules/': 'rules',
};

interface DatabaseSectionValue {
  info: Resource<DatabaseInfo>;
  /** The limits a write must keep: the server's once loaded. */
  limits: DatabaseLimits;
}

const DatabaseSectionContext = createContext<DatabaseSectionValue | null>(null);

export function useDatabaseSection(): DatabaseSectionValue {
  const value = useContext(DatabaseSectionContext);
  if (!value) throw new Error('useDatabaseSection must be used inside DatabaseSection');
  return value;
}

/**
 * The project's Realtime Database pages: the database URL to put in apps, then a tab for the
 * data and one for the rules. A server without the database says so instead.
 */
export function DatabaseSection({ children }: { children: ReactNode }) {
  const t = useTranslations('database.section');
  const tCommon = useTranslations('common');
  const errorText = useErrorText();
  const { project } = useProject();
  const id = project.projectId;
  const selected = TAB_BY_PATH[normalizePath(usePathname())] ?? 'data';
  const info = useResource((signal) => api.getDatabase(id, signal), [id]);

  const items = [
    { id: 'data', label: t('data'), href: routes.database(id) },
    { id: 'rules', label: t('rules'), href: routes.databaseRules(id) },
  ];

  let body: ReactNode;
  if (info.error) {
    body = (
      <Banner tone="error" action={<Button variant="text" onPress={info.reload}>{tCommon('retry')}</Button>}>
        {errorText.message(info.error)}
      </Banner>
    );
  } else if (!info.data) {
    body = <Skeleton lines={4} />;
  } else if (!info.data.enabled) {
    body = (
      <Banner tone="info" title={t('disabledTitle')}>
        {t.rich('disabledBody', { code: (c) => <code dir="ltr">{c}</code> })}
      </Banner>
    );
  } else {
    body = (
      <>
        {info.data.devOpenRules && (
          <Banner tone="warning" title={t('devOpenTitle')}>
            {t.rich('devOpenBody', { code: (c) => <code dir="ltr">{c}</code> })}
          </Banner>
        )}
        <LinkTabs label={t('sections')} items={items} selected={selected}>
          {children}
        </LinkTabs>
      </>
    );
  }

  return (
    <DatabaseSectionContext value={{ info, limits: info.data?.limits ?? DEFAULT_LIMITS }}>
      <PageHeader title={t('title')} subtitle={t('subtitle')} />
      {info.data && (
        <div className={styles.url}>
          <span className={styles.urlLabel}>{t('databaseUrl')}</span>
          <span className={styles.urlValue}>
            <Ltr>{info.data.databaseURL}</Ltr>
            <CopyButton value={info.data.databaseURL} label={t('copyUrl')} />
          </span>
        </div>
      )}
      {body}
    </DatabaseSectionContext>
  );
}
