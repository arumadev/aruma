'use client';

import { useState } from 'react';
import { useTranslations } from 'use-intl';
import { Banner } from '@/components/ui/Banner';
import { Button } from '@/components/ui/Button';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { EmptyState } from '@/components/ui/EmptyState';
import { PageHeader } from '@/components/ui/PageHeader';
import { Skeleton } from '@/components/ui/Skeleton';
import { useSnackbar } from '@/components/ui/Snackbar';
import { useProject } from '@/features/project/ProjectContext';
import * as api from '@/lib/api/console';
import type { ServiceAccount } from '@/lib/api/types';
import { useErrorText } from '@/lib/errors';
import { useDocumentTitle } from '@/lib/hooks/useDocumentTitle';
import { useResource } from '@/lib/hooks/useResource';
import { CreateServiceAccountDialog } from './CreateServiceAccountDialog';
import { ServiceAccountCard } from './ServiceAccountCard';
import styles from './ServiceAccountsScreen.module.css';

/** D-09: service accounts let servers use the Admin SDK; their keys are shown once. */
export function ServiceAccountsScreen() {
  const t = useTranslations('serviceAccounts');
  const tCommon = useTranslations('common');
  const errorText = useErrorText();
  const notify = useSnackbar();
  const { project } = useProject();
  const accounts = useResource((signal) => api.listServiceAccounts(project.projectId, signal), [project.projectId]);
  const [creating, setCreating] = useState(false);
  const [deleting, setDeleting] = useState<ServiceAccount | null>(null);
  const editable = project.state === 'ACTIVE';
  useDocumentTitle(t('title'));

  const list = accounts.data?.serviceAccounts ?? [];
  return (
    <>
      <PageHeader
        title={t('title')}
        subtitle={t('subtitle')}
        actions={
          editable && (
            <Button icon="add" onPress={() => setCreating(true)}>
              {t('create')}
            </Button>
          )
        }
      />
      {accounts.error ? (
        <Banner tone="error" action={<Button variant="text" onPress={accounts.reload}>{tCommon('retry')}</Button>}>
          {errorText.message(accounts.error)}
        </Banner>
      ) : accounts.loading && !accounts.data ? (
        <Skeleton lines={2} blocks={2} />
      ) : list.length === 0 ? (
        <EmptyState
          icon="team"
          title={t('emptyTitle')}
          action={
            editable && (
              <Button icon="add" onPress={() => setCreating(true)}>
                {t('create')}
              </Button>
            )
          }
        >
          {t('emptyBody')}
        </EmptyState>
      ) : (
        <ul className={styles.list}>
          {list.map((sa) => (
            <ServiceAccountCard key={sa.id} projectId={project.projectId} account={sa} editable={editable} onDelete={() => setDeleting(sa)} />
          ))}
        </ul>
      )}
      {creating && (
        <CreateServiceAccountDialog
          projectId={project.projectId}
          onCreated={(sa) => accounts.setData((current) => ({ serviceAccounts: [...(current?.serviceAccounts ?? []), sa] }))}
          onClose={() => setCreating(false)}
        />
      )}
      {deleting && (
        <ConfirmDialog
          title={t('deleteTitle')}
          confirmLabel={t('delete')}
          danger
          onClose={() => setDeleting(null)}
          onConfirm={async () => {
            await api.deleteServiceAccount(project.projectId, deleting.id);
            accounts.setData((current) => ({ serviceAccounts: (current?.serviceAccounts ?? []).filter((s) => s.id !== deleting.id) }));
            notify(t('deleted'));
          }}
        >
          <p>{t('deleteBody', { email: deleting.email })}</p>
        </ConfirmDialog>
      )}
    </>
  );
}
