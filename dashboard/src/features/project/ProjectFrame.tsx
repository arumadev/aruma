'use client';

import { useSearchParams } from 'next/navigation';
import { useMemo, useState, type ReactNode } from 'react';
import { useFormatter, useTranslations } from 'use-intl';
import { Banner } from '@/components/ui/Banner';
import { Button, LinkButton } from '@/components/ui/Button';
import { EmptyState } from '@/components/ui/EmptyState';
import { Skeleton } from '@/components/ui/Skeleton';
import { useSnackbar } from '@/components/ui/Snackbar';
import * as api from '@/lib/api/console';
import { useErrorText } from '@/lib/errors';
import { useResource } from '@/lib/hooks/useResource';
import { routes } from '@/lib/routes';
import { ProjectContext, type ProjectContextValue } from './ProjectContext';
import styles from './ProjectFrame.module.css';
import { notifyProjectChanged } from './projectEvents';

/** Loads the project named by `?id=` once for all of its pages. */
export function ProjectFrame({ children }: { children: ReactNode }) {
  const t = useTranslations('project');
  const errorText = useErrorText();
  const projectId = useSearchParams().get('id') ?? '';
  const resource = useResource((signal) => api.getProject(projectId, signal), [projectId], projectId !== '');
  const { data: project, setData } = resource;
  const value = useMemo<ProjectContextValue | null>(
    () =>
      project
        ? {
            project,
            setProject: (updated) => {
              setData(updated);
              notifyProjectChanged();
            },
          }
        : null,
    [project, setData],
  );

  if (!projectId) {
    return (
      <EmptyState icon="folder" title={t('missing')} action={<LinkButton href={routes.projects}>{t('allProjects')}</LinkButton>} />
    );
  }
  if (resource.error && !project) {
    return (
      <Banner tone="error" action={<LinkButton variant="text" href={routes.projects}>{t('allProjects')}</LinkButton>}>
        {errorText.message(resource.error)}
      </Banner>
    );
  }
  if (!value) return <Skeleton lines={3} blocks={2} />;

  return (
    <ProjectContext value={value}>
      <div className={styles.frame}>
        {value.project.state === 'DELETED' && <DeletedBanner {...value} />}
        {children}
      </div>
    </ProjectContext>
  );
}

function DeletedBanner({ project, setProject }: ProjectContextValue) {
  const t = useTranslations('project');
  const format = useFormatter();
  const errorText = useErrorText();
  const notify = useSnackbar();
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);

  async function restore() {
    setPending(true);
    setError(null);
    try {
      setProject(await api.restoreProject(project.projectId));
      notify(t('restored'));
    } catch (err) {
      setError(err);
    } finally {
      setPending(false);
    }
  }

  const expires = project.expireTime ? format.dateTime(new Date(project.expireTime), { dateStyle: 'long' }) : '';
  return (
    <Banner
      tone="warning"
      title={t('deletedTitle')}
      action={
        <Button variant="tonal" icon="undo" isPending={pending} onPress={restore}>
          {t('restore')}
        </Button>
      }
    >
      <p>{t('deletedBody', { date: expires })}</p>
      {Boolean(error) && <p>{errorText.message(error)}</p>}
    </Banner>
  );
}
