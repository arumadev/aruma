'use client';

import { usePathname } from 'next/navigation';
import { createContext, useContext, type ReactNode } from 'react';
import { useTranslations } from 'use-intl';
import { Banner } from '@/components/ui/Banner';
import { Button } from '@/components/ui/Button';
import { Ltr } from '@/components/ui/Code';
import { PageHeader } from '@/components/ui/PageHeader';
import { Skeleton } from '@/components/ui/Skeleton';
import { LinkTabs } from '@/components/ui/Tabs';
import { normalizePath } from '@/components/shell/navigation';
import { useProject } from '@/features/project/ProjectContext';
import { useErrorText } from '@/lib/errors';
import { useResource, type Resource } from '@/lib/hooks/useResource';
import { routes } from '@/lib/routes';
import * as api from './api';
import styles from './DocumentsSection.module.css';

type Tab = 'data' | 'rules' | 'indexes' | 'usage';

const TAB_BY_PATH: Record<string, Tab> = {
  '/project/documents/': 'data',
  '/project/documents/rules/': 'rules',
  '/project/documents/indexes/': 'indexes',
  '/project/documents/usage/': 'usage',
};

interface DocumentsSectionValue {
  info: Resource<api.DocumentsInfo>;
  /** projects/{p}/databases/(default): references are named under it. */
  database: string;
  limits: api.DocumentsLimits | undefined;
}

const DocumentsSectionContext = createContext<DocumentsSectionValue | null>(null);

export function useDocumentsSection(): DocumentsSectionValue {
  const value = useContext(DocumentsSectionContext);
  if (!value) throw new Error('useDocumentsSection must be used inside DocumentsSection');
  return value;
}

/**
 * The project's Document Database pages (Firestore-compatible): its database, then a tab for the
 * data, the rules, the indexes and the usage. A server without the service says so instead.
 */
export function DocumentsSection({ children }: { children: ReactNode }) {
  const t = useTranslations('documents.section');
  const tCommon = useTranslations('common');
  const errorText = useErrorText();
  const { project } = useProject();
  const id = project.projectId;
  const selected = TAB_BY_PATH[normalizePath(usePathname())] ?? 'data';
  const info = useResource((signal) => api.getInfo(id, signal), [id]);

  const items = [
    { id: 'data', label: t('data'), href: routes.documents(id) },
    { id: 'rules', label: t('rules'), href: routes.documentsRules(id) },
    { id: 'indexes', label: t('indexes'), href: routes.documentsIndexes(id) },
    { id: 'usage', label: t('usage'), href: routes.documentsUsage(id) },
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
      <LinkTabs label={t('sections')} items={items} selected={selected}>
        {children}
      </LinkTabs>
    );
  }

  const database = info.data?.name ?? `projects/${id}/databases/(default)`;
  return (
    <DocumentsSectionContext value={{ info, database, limits: info.data?.limits }}>
      <PageHeader title={t('title')} subtitle={t('subtitle')} />
      {info.data?.enabled && (
        <div className={styles.database}>
          <span className={styles.label}>{t('database')}</span>
          <span className={styles.value}>
            <Ltr>{info.data.databaseId}</Ltr>
          </span>
        </div>
      )}
      {body}
    </DocumentsSectionContext>
  );
}
