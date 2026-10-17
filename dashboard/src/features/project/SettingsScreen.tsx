'use client';

import { useState, type FormEvent } from 'react';
import { useFormatter, useTranslations } from 'use-intl';
import { Banner } from '@/components/ui/Banner';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { Ltr } from '@/components/ui/Code';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { Detail, DetailList } from '@/components/ui/DetailList';
import { PageHeader } from '@/components/ui/PageHeader';
import { useSnackbar } from '@/components/ui/Snackbar';
import { TextField } from '@/components/ui/TextField';
import { Time } from '@/components/ui/Time';
import * as api from '@/lib/api/console';
import { useErrorText } from '@/lib/errors';
import { useDocumentTitle } from '@/lib/hooks/useDocumentTitle';
import { useServerStatus } from '@/lib/hooks/useServerStatus';
import { DISPLAY_NAME_MAX } from '@/lib/validation';
import { useProject } from './ProjectContext';
import styles from './SettingsScreen.module.css';

const DAY_MS = 24 * 60 * 60 * 1000;

/** D-10: rename, auth domain, and deletion with a typed confirmation. */
export function SettingsScreen() {
  const t = useTranslations('settings');
  const { project } = useProject();
  const [deleting, setDeleting] = useState(false);
  const active = project.state === 'ACTIVE';
  useDocumentTitle(t('title'));

  return (
    <>
      <PageHeader title={t('title')} />
      {active && <GeneralForm key={project.etag} />}
      <Card title={t('details')}>
        <DetailList>
          <Detail label={t('projectId')} value={project.projectId} copyLabel={t('copyProjectId')} />
          <Detail label={t('projectNumber')} value={project.projectNumber} copyLabel={t('copyProjectNumber')} />
          <Detail label={t('storageBucket')} value={project.storageBucket} copyLabel={t('copyStorageBucket')} />
          {!active && <Detail label={t('authDomain')} value={project.authDomain} />}
          <Detail label={t('created')}>
            <Time value={project.createTime} />
          </Detail>
        </DetailList>
      </Card>
      {active && (
        <Card
          tone="danger"
          title={t('dangerTitle')}
          description={t('dangerBody')}
          actions={
            <Button variant="danger" icon="delete" onPress={() => setDeleting(true)}>
              {t('delete')}
            </Button>
          }
        />
      )}
      {deleting && <DeleteProjectDialog onClose={() => setDeleting(false)} />}
    </>
  );
}

function GeneralForm() {
  const t = useTranslations('settings');
  const tCommon = useTranslations('common');
  const errorText = useErrorText();
  const notify = useSnackbar();
  const { project, setProject } = useProject();
  const [displayName, setDisplayName] = useState(project.displayName);
  const [authDomain, setAuthDomain] = useState(project.authDomain);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const changed = displayName.trim() !== project.displayName || authDomain.trim() !== project.authDomain;

  async function save(event: FormEvent) {
    event.preventDefault();
    if (!changed) return;
    setPending(true);
    setError(null);
    try {
      const updated = await api.updateProject(project.projectId, {
        ...(displayName.trim() !== project.displayName && { displayName: displayName.trim() }),
        ...(authDomain.trim() !== project.authDomain && { authDomain: authDomain.trim() }),
        etag: project.etag,
      });
      setProject(updated);
      notify(t('saved'));
    } catch (err) {
      setError(err);
    } finally {
      setPending(false);
    }
  }

  const fieldErrors = { displayName: errorText.field(error, 'displayName'), authDomain: errorText.field(error, 'authDomain') };
  return (
    <Card title={t('general')}>
      <form className={styles.form} onSubmit={save} noValidate>
        {Boolean(error) && !fieldErrors.displayName && !fieldErrors.authDomain && <Banner tone="error">{errorText.message(error)}</Banner>}
        <TextField label={t('name')} value={displayName} onChange={setDisplayName} maxLength={DISPLAY_NAME_MAX} isRequired errorMessage={fieldErrors.displayName} />
        <TextField
          label={t('authDomain')}
          value={authDomain}
          onChange={setAuthDomain}
          ltr
          description={t('authDomainHint')}
          errorMessage={fieldErrors.authDomain}
        />
        <div>
          <Button type="submit" isPending={pending} isDisabled={!changed || !displayName.trim()}>
            {tCommon('save')}
          </Button>
        </div>
      </form>
    </Card>
  );
}

function DeleteProjectDialog({ onClose }: { onClose: () => void }) {
  const t = useTranslations('settings');
  const format = useFormatter();
  const notify = useSnackbar();
  const status = useServerStatus();
  const { project, setProject } = useProject();
  const [typed, setTyped] = useState('');
  const [openedAt] = useState(() => Date.now());
  const days = status.data?.limits.deletedRetentionDays ?? 30;
  const until = format.dateTime(new Date(openedAt + days * DAY_MS), { dateStyle: 'long' });

  return (
    <ConfirmDialog
      title={t('deleteTitle')}
      confirmLabel={t('delete')}
      danger
      canConfirm={typed === project.projectId}
      onClose={onClose}
      onConfirm={async () => {
        setProject(await api.deleteProject(project.projectId, typed));
        notify(t('deleted'));
      }}
    >
      <p>{t('deleteBody', { days, date: until })}</p>
      <p>{t('deleteIdReserved')}</p>
      <TextField
        label={t('confirmLabel')}
        value={typed}
        onChange={setTyped}
        ltr
        autoFocus
        description={t.rich('confirmHint', { id: () => <Ltr>{project.projectId}</Ltr> })}
      />
    </ConfirmDialog>
  );
}
