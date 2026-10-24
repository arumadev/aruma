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
import { useErrorText } from '@/lib/errors';
import { useResource, type Resource } from '@/lib/hooks/useResource';
import { routes } from '@/lib/routes';
import * as api from './api';
import styles from './StorageSection.module.css';

type Tab = 'files' | 'rules' | 'usage' | 'cors';

const TAB_BY_PATH: Record<string, Tab> = {
  '/project/storage/': 'files',
  '/project/storage/object/': 'files',
  '/project/storage/rules/': 'rules',
  '/project/storage/usage/': 'usage',
  '/project/storage/cors/': 'cors',
};

interface StorageSectionValue {
  info: Resource<api.StorageInfo>;
  bucket: string;
  limits: api.StorageLimits | undefined;
}

const StorageSectionContext = createContext<StorageSectionValue | null>(null);

export function useStorageSection(): StorageSectionValue {
  const value = useContext(StorageSectionContext);
  if (!value) throw new Error('useStorageSection must be used inside StorageSection');
  return value;
}

/**
 * The project's Cloud Storage pages: the bucket name apps carry, then a tab for the files, the
 * rules, the usage and the CORS rules. A server without Storage says so instead.
 */
export function StorageSection({ children }: { children: ReactNode }) {
  const t = useTranslations('storage.section');
  const tCommon = useTranslations('common');
  const errorText = useErrorText();
  const { project } = useProject();
  const id = project.projectId;
  const selected = TAB_BY_PATH[normalizePath(usePathname())] ?? 'files';
  const info = useResource((signal) => api.getStorage(id, signal), [id]);

  const items = [
    { id: 'files', label: t('files'), href: routes.storage(id) },
    { id: 'rules', label: t('rules'), href: routes.storageRules(id) },
    { id: 'usage', label: t('usage'), href: routes.storageUsage(id) },
    { id: 'cors', label: t('cors'), href: routes.storageCors(id) },
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

  const bucket = info.data?.bucket ?? project.storageBucket;
  return (
    <StorageSectionContext value={{ info, bucket, limits: info.data?.limits }}>
      <PageHeader title={t('title')} subtitle={t('subtitle')} />
      {info.data && (
        <div className={styles.bucket}>
          <span className={styles.bucketLabel}>{t('bucket')}</span>
          <span className={styles.bucketValue}>
            <Ltr>{`gs://${bucket}`}</Ltr>
            <CopyButton value={`gs://${bucket}`} label={t('copyBucket')} />
          </span>
        </div>
      )}
      {body}
    </StorageSectionContext>
  );
}
