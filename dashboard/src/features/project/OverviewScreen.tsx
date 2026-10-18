'use client';

import { Link } from 'react-aria-components';
import { useTranslations } from 'use-intl';
import { Badge } from '@/components/ui/Badge';
import { Banner } from '@/components/ui/Banner';
import { Button, LinkButton } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { Ltr } from '@/components/ui/Code';
import { Detail, DetailList } from '@/components/ui/DetailList';
import { EmptyState } from '@/components/ui/EmptyState';
import { Icon, type IconName } from '@/components/ui/Icon';
import { PageHeader } from '@/components/ui/PageHeader';
import { Skeleton } from '@/components/ui/Skeleton';
import { PLATFORMS } from '@/features/apps/platforms';
import * as api from '@/lib/api/console';
import type { App } from '@/lib/api/types';
import { useErrorText } from '@/lib/errors';
import { useDocumentTitle } from '@/lib/hooks/useDocumentTitle';
import { useResource } from '@/lib/hooks/useResource';
import { routes } from '@/lib/routes';
import { FlutterCard } from './FlutterCard';
import styles from './OverviewScreen.module.css';
import { useProject } from './ProjectContext';

const SERVICES: { id: 'auth' | 'firestore' | 'database' | 'storage' | 'functions'; icon: IconName }[] = [
  { id: 'auth', icon: 'shield' },
  { id: 'firestore', icon: 'description' },
  { id: 'database', icon: 'bolt' },
  { id: 'storage', icon: 'cloud' },
  { id: 'functions', icon: 'code' },
];

/** D-05: the project's identifiers, its apps, Flutter options and the services to come. */
export function OverviewScreen() {
  const t = useTranslations('overview');
  const tCommon = useTranslations('common');
  const errorText = useErrorText();
  const { project } = useProject();
  const apps = useResource((signal) => api.listApps(project.projectId, { pageSize: 1000, showDeleted: true }, signal), [project.projectId]);
  const active = project.state === 'ACTIVE';
  useDocumentTitle(project.displayName);

  const list = apps.data?.apps ?? [];
  const sorted = [...list].sort((a, b) => Number(a.state === 'DELETED') - Number(b.state === 'DELETED'));

  return (
    <>
      <PageHeader title={project.displayName} subtitle={t('subtitle')} />
      <Card title={t('identifiers')}>
        <DetailList>
          <Detail label={t('projectId')} value={project.projectId} copyLabel={t('copyProjectId')} />
          <Detail label={t('projectNumber')} value={project.projectNumber} copyLabel={t('copyProjectNumber')} />
        </DetailList>
      </Card>
      <Card
        title={t('apps')}
        description={t('appsHint')}
        actions={
          active && (
            <LinkButton icon="add" href={routes.newApp(project.projectId)}>
              {t('addApp')}
            </LinkButton>
          )
        }
      >
        {apps.error ? (
          <Banner tone="error" action={<Button variant="text" onPress={apps.reload}>{tCommon('retry')}</Button>}>
            {errorText.message(apps.error)}
          </Banner>
        ) : apps.loading && !apps.data ? (
          <Skeleton lines={2} />
        ) : list.length === 0 ? (
          <EmptyState icon="apps" title={t('noApps')}>
            {t('noAppsBody')}
          </EmptyState>
        ) : (
          <ul className={styles.apps}>
            {sorted.map((app) => (
              <AppRow key={app.appId} projectId={project.projectId} app={app} />
            ))}
          </ul>
        )}
      </Card>
      {list.some((a) => a.state === 'ACTIVE') && <FlutterCard projectId={project.projectId} apps={list.filter((a) => a.state === 'ACTIVE')} />}
      <Card title={t('services')} description={t('servicesHint')}>
        <ul className={styles.services}>
          {SERVICES.map((service) =>
            service.id === 'auth' ? (
              <li key={service.id}>
                <Link href={routes.authUsers(project.projectId)} className={`${styles.service} ${styles.available}`}>
                  <Icon name={service.icon} />
                  <span className={styles.serviceName}>{t(`service.${service.id}`)}</span>
                  <Icon name="chevronForward" />
                </Link>
              </li>
            ) : (
              <li key={service.id} className={styles.service}>
                <Icon name={service.icon} />
                <span className={styles.serviceName}>{t(`service.${service.id}`)}</span>
                <Badge>{t('comingSoon')}</Badge>
              </li>
            ),
          )}
        </ul>
      </Card>
    </>
  );
}

function AppRow({ projectId, app }: { projectId: string; app: App }) {
  const t = useTranslations('overview');
  const tPlatform = useTranslations('platform');
  const info = PLATFORMS[app.platform];
  return (
    <li>
      <Link href={routes.app(projectId, app.appId)} className={styles.app}>
        <span className={styles.appIcon}>
          <Icon name={info.icon} />
        </span>
        <span className={styles.appText}>
          <span className={styles.appName}>
            <bdi>{app.displayName || tPlatform(info.label)}</bdi>
            {app.state === 'DELETED' && <Badge tone="error">{t('deleted')}</Badge>}
          </span>
          <span className={styles.appMeta}>
            <span>{tPlatform(info.label)}</span>
            <Ltr>{app.appId}</Ltr>
          </span>
        </span>
        <Icon name="chevronForward" />
      </Link>
    </li>
  );
}
