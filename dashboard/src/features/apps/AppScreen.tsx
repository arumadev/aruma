'use client';

import { useSearchParams } from 'next/navigation';
import { useState } from 'react';
import { useFormatter, useTranslations } from 'use-intl';
import { Banner } from '@/components/ui/Banner';
import { Button, LinkButton } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { Detail, DetailList } from '@/components/ui/DetailList';
import { Dialog } from '@/components/ui/Dialog';
import { PageHeader } from '@/components/ui/PageHeader';
import { Skeleton } from '@/components/ui/Skeleton';
import { useSnackbar } from '@/components/ui/Snackbar';
import { TextField } from '@/components/ui/TextField';
import { Time } from '@/components/ui/Time';
import { useProject } from '@/features/project/ProjectContext';
import * as api from '@/lib/api/console';
import type { App } from '@/lib/api/types';
import { useErrorText } from '@/lib/errors';
import { useDocumentTitle } from '@/lib/hooks/useDocumentTitle';
import { useResource } from '@/lib/hooks/useResource';
import { useServerStatus } from '@/lib/hooks/useServerStatus';
import { routes } from '@/lib/routes';
import { DISPLAY_NAME_MAX } from '@/lib/validation';
import { ConfigDownload } from './ConfigDownload';
import { PLATFORMS } from './platforms';
import { ShaCertificates } from './ShaCertificates';

/** D-07: one app: its identifiers, config file, SHA fingerprints (Android), rename, delete and restore. */
export function AppScreen() {
  const t = useTranslations('app');
  const tPlatform = useTranslations('platform');
  const errorText = useErrorText();
  const { project } = useProject();
  const appId = useSearchParams().get('app') ?? '';
  const app = useResource((signal) => api.getApp(project.projectId, appId, signal), [project.projectId, appId], appId !== '');
  const [dialog, setDialog] = useState<'rename' | 'delete' | null>(null);
  useDocumentTitle(app.data ? app.data.displayName || app.data.appId : t('title'));

  if (app.error && !app.data) {
    return (
      <Banner tone="error" action={<LinkButton variant="text" href={routes.project(project.projectId)}>{t('back')}</LinkButton>}>
        {errorText.message(app.error)}
      </Banner>
    );
  }
  if (!app.data) return <Skeleton lines={3} blocks={2} />;

  const current = app.data;
  const info = PLATFORMS[current.platform];
  const deleted = current.state === 'DELETED';
  const editable = !deleted && project.state === 'ACTIVE';

  return (
    <>
      <PageHeader
        title={current.displayName || current.appId}
        subtitle={tPlatform(info.label)}
        actions={
          editable && (
            <>
              <Button variant="outlined" icon="edit" onPress={() => setDialog('rename')}>
                {t('rename')}
              </Button>
              <Button variant="outlined" icon="delete" onPress={() => setDialog('delete')}>
                {t('delete')}
              </Button>
            </>
          )
        }
      />
      {deleted && <DeletedAppBanner projectId={project.projectId} app={current} onRestored={app.setData} />}
      <Card title={t('identifiers')}>
        <DetailList>
          <Detail label={t('appId')} value={current.appId} copyLabel={t('copyAppId')} />
          {current.packageName && <Detail label={t('packageName')} value={current.packageName} copyLabel={t('copyPackageName')} />}
          {current.bundleId && <Detail label={t('bundleId')} value={current.bundleId} copyLabel={t('copyBundleId')} />}
          <Detail label={t('created')}>
            <Time value={current.createTime} />
          </Detail>
        </DetailList>
      </Card>
      {!deleted && (
        <Card title={t('config')} description={t('configHint')}>
          <ConfigDownload projectId={project.projectId} app={current} />
        </Card>
      )}
      {current.platform === 'ANDROID' && !deleted && <ShaCertificates projectId={project.projectId} appId={current.appId} editable={editable} />}
      {dialog === 'rename' && <RenameDialog projectId={project.projectId} app={current} onSaved={app.setData} onClose={() => setDialog(null)} />}
      {dialog === 'delete' && <DeleteAppDialog projectId={project.projectId} app={current} onDeleted={app.setData} onClose={() => setDialog(null)} />}
    </>
  );
}

function DeletedAppBanner({ projectId, app, onRestored }: { projectId: string; app: App; onRestored: (app: App) => void }) {
  const t = useTranslations('app');
  const format = useFormatter();
  const errorText = useErrorText();
  const notify = useSnackbar();
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);

  async function restore() {
    setPending(true);
    setError(null);
    try {
      onRestored(await api.undeleteApp(projectId, app.appId));
      notify(t('restored'));
    } catch (err) {
      setError(err);
    } finally {
      setPending(false);
    }
  }

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
      {app.expireTime && <p>{t('deletedBody', { date: format.dateTime(new Date(app.expireTime), { dateStyle: 'long' }) })}</p>}
      {Boolean(error) && <p>{errorText.message(error)}</p>}
    </Banner>
  );
}

function RenameDialog({ projectId, app, onSaved, onClose }: { projectId: string; app: App; onSaved: (app: App) => void; onClose: () => void }) {
  const t = useTranslations('app');
  const tCommon = useTranslations('common');
  const errorText = useErrorText();
  const notify = useSnackbar();
  const [name, setName] = useState(app.displayName);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);

  async function save() {
    setPending(true);
    setError(null);
    try {
      onSaved(await api.updateApp(projectId, app.appId, { displayName: name.trim(), etag: app.etag }));
      notify(t('renamed'));
      onClose();
    } catch (err) {
      setError(err);
      setPending(false);
    }
  }

  const fieldError = errorText.field(error, 'displayName');
  return (
    <Dialog
      isOpen
      onOpenChange={(open) => !open && onClose()}
      title={t('renameTitle')}
      isBusy={pending}
      onSubmit={save}
      actions={
        <>
          <Button variant="text" onPress={onClose} isDisabled={pending}>
            {tCommon('cancel')}
          </Button>
          <Button type="submit" isPending={pending}>
            {tCommon('save')}
          </Button>
        </>
      }
    >
      {Boolean(error) && !fieldError && <Banner tone="error">{errorText.message(error)}</Banner>}
      <TextField label={t('nickname')} value={name} onChange={setName} maxLength={DISPLAY_NAME_MAX} autoFocus errorMessage={fieldError} />
    </Dialog>
  );
}

function DeleteAppDialog({ projectId, app, onDeleted, onClose }: { projectId: string; app: App; onDeleted: (app: App) => void; onClose: () => void }) {
  const t = useTranslations('app');
  const notify = useSnackbar();
  const status = useServerStatus();
  const days = status.data?.limits.deletedRetentionDays ?? 30;
  return (
    <ConfirmDialog
      title={t('deleteTitle')}
      confirmLabel={t('delete')}
      danger
      onClose={onClose}
      onConfirm={async () => {
        onDeleted(await api.removeApp(projectId, app.appId, app.etag));
        notify(t('deleted'));
      }}
    >
      <p>{t('deleteBody', { days })}</p>
    </ConfirmDialog>
  );
}
