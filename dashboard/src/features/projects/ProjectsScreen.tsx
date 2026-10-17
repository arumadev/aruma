'use client';

import { useState } from 'react';
import { Link } from 'react-aria-components';
import { useTranslations } from 'use-intl';
import { Badge } from '@/components/ui/Badge';
import { Banner } from '@/components/ui/Banner';
import { Button } from '@/components/ui/Button';
import { Ltr } from '@/components/ui/Code';
import { EmptyState } from '@/components/ui/EmptyState';
import { PageHeader } from '@/components/ui/PageHeader';
import { Skeleton } from '@/components/ui/Skeleton';
import { Switch } from '@/components/ui/Switch';
import { TextField } from '@/components/ui/TextField';
import * as api from '@/lib/api/console';
import type { Project } from '@/lib/api/types';
import { useErrorText } from '@/lib/errors';
import { useDocumentTitle } from '@/lib/hooks/useDocumentTitle';
import { useResource } from '@/lib/hooks/useResource';
import { routes } from '@/lib/routes';
import { NewProjectDialog } from './NewProjectDialog';
import styles from './ProjectsScreen.module.css';

const PAGE_SIZE = 100;

/** D-03: the signed-in owner's projects, filterable, with "New project". */
export function ProjectsScreen() {
  const t = useTranslations('projects');
  const tCommon = useTranslations('common');
  const errorText = useErrorText();
  const [showDeleted, setShowDeleted] = useState(false);
  const [query, setQuery] = useState('');
  const [creating, setCreating] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);
  const [moreError, setMoreError] = useState<unknown>(null);
  const list = useResource((signal) => api.listProjects({ pageSize: PAGE_SIZE, showDeleted }, signal), [showDeleted]);
  useDocumentTitle(t('title'));

  async function loadMore() {
    const token = list.data?.nextPageToken;
    if (!token) return;
    setLoadingMore(true);
    setMoreError(null);
    try {
      const page = await api.listProjects({ pageSize: PAGE_SIZE, showDeleted, pageToken: token });
      list.setData((current) => ({ projects: [...(current?.projects ?? []), ...page.projects], nextPageToken: page.nextPageToken }));
    } catch (err) {
      setMoreError(err);
    } finally {
      setLoadingMore(false);
    }
  }

  const projects = list.data?.projects ?? [];
  const needle = query.trim().toLowerCase();
  const visible = needle
    ? projects.filter((p) => p.displayName.toLowerCase().includes(needle) || p.projectId.includes(needle) || p.projectNumber.includes(needle))
    : projects;

  return (
    <div className={styles.page}>
      <PageHeader
        title={t('title')}
        subtitle={t('subtitle')}
        actions={
          <Button icon="add" onPress={() => setCreating(true)}>
            {t('new')}
          </Button>
        }
      />
      <div className={styles.toolbar}>
        <TextField className={styles.search} label={t('filter')} type="search" value={query} onChange={setQuery} placeholder={t('filterHint')} />
        <Switch isSelected={showDeleted} onChange={setShowDeleted}>
          {t('showDeleted')}
        </Switch>
      </div>
      {list.error ? (
        <Banner tone="error" action={<Button variant="text" onPress={list.reload}>{tCommon('retry')}</Button>}>
          {errorText.message(list.error)}
        </Banner>
      ) : list.loading && !list.data ? (
        <Skeleton lines={0} blocks={3} />
      ) : projects.length === 0 ? (
        <EmptyState
          icon="folder"
          title={t('emptyTitle')}
          action={
            <Button icon="add" onPress={() => setCreating(true)}>
              {t('new')}
            </Button>
          }
        >
          {t('emptyBody')}
        </EmptyState>
      ) : visible.length === 0 ? (
        <EmptyState icon="search" title={t('noMatchTitle')}>
          {t('noMatchBody')}
        </EmptyState>
      ) : (
        <ul className={styles.grid} aria-label={t('title')}>
          {visible.map((project) => (
            <ProjectCard key={project.projectId} project={project} />
          ))}
        </ul>
      )}
      {list.data?.nextPageToken && (
        <div className={styles.more}>
          {Boolean(moreError) && <Banner tone="error">{errorText.message(moreError)}</Banner>}
          <Button variant="outlined" isPending={loadingMore} onPress={loadMore}>
            {t('loadMore')}
          </Button>
        </div>
      )}
      {creating && <NewProjectDialog onClose={() => setCreating(false)} />}
    </div>
  );
}

function ProjectCard({ project }: { project: Project }) {
  const t = useTranslations('projects');
  return (
    <li>
      <Link href={routes.project(project.projectId)} className={styles.card}>
        <span className={styles.cardTop}>
          <span className={styles.name}>{project.displayName}</span>
          {project.state === 'DELETED' && <Badge tone="error">{t('deleted')}</Badge>}
        </span>
        <span className={styles.meta}>
          <span className={styles.metaLabel}>{t('id')}</span>
          <Ltr>{project.projectId}</Ltr>
        </span>
        <span className={styles.meta}>
          <span className={styles.metaLabel}>{t('number')}</span>
          <Ltr>{project.projectNumber}</Ltr>
        </span>
      </Link>
    </li>
  );
}
