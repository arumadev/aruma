'use client';

import { usePathname, useRouter } from 'next/navigation';
import { useTranslations } from 'use-intl';
import { Select } from '@/components/ui/Select';
import * as api from '@/lib/api/console';
import { useResource } from '@/lib/hooks/useResource';
import { routes } from '@/lib/routes';
import { normalizePath } from './navigation';
import styles from './TopAppBar.module.css';

// Pages tied to one app make no sense in another project, so switching lands on its overview.
const APP_PAGES = new Set(['/project/app/', '/project/apps/new/']);

/** Picks another project and stays on the same kind of page. */
export function ProjectSwitcher({ projectId }: { projectId: string }) {
  const t = useTranslations('shell');
  const router = useRouter();
  const pathname = normalizePath(usePathname());
  const projects = useResource((signal) => api.listProjects({ pageSize: 1000 }, signal), []);

  const options = (projects.data?.projects ?? [])
    .filter((p) => p.state === 'ACTIVE' || p.projectId === projectId)
    .map((p) => ({ id: p.projectId, label: p.displayName, detail: p.projectId }));
  if (!options.some((o) => o.id === projectId)) options.unshift({ id: projectId, label: projectId, detail: projectId });

  return (
    <Select
      className={styles.switcher}
      label={t('project')}
      hideLabel
      options={options}
      value={projectId}
      onChange={(id) => {
        if (id === projectId) return;
        const target = APP_PAGES.has(pathname) ? routes.project(id) : `${pathname}?${new URLSearchParams({ id }).toString()}`;
        router.push(target);
      }}
    />
  );
}
