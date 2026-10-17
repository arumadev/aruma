'use client';

import { useRouter } from 'next/navigation';
import { useState } from 'react';
import { useTranslations } from 'use-intl';
import { Banner } from '@/components/ui/Banner';
import { Button } from '@/components/ui/Button';
import { Dialog } from '@/components/ui/Dialog';
import { Icon } from '@/components/ui/Icon';
import { useSnackbar } from '@/components/ui/Snackbar';
import { TextField } from '@/components/ui/TextField';
import { ApiError } from '@/lib/api/client';
import * as api from '@/lib/api/console';
import { useErrorText } from '@/lib/errors';
import { useDebouncedValue } from '@/lib/hooks/useDebouncedValue';
import { useResource } from '@/lib/hooks/useResource';
import { routes } from '@/lib/routes';
import { DISPLAY_NAME_MAX, PROJECT_ID } from '@/lib/validation';
import styles from './NewProjectDialog.module.css';

type IdStatus = 'empty' | 'invalid' | 'checking' | 'available' | 'taken';

/**
 * D-04: name the project, accept or edit the suggested id (checked live), create. The id
 * can never change afterwards, which the dialog says plainly.
 */
export function NewProjectDialog({ onClose }: { onClose: () => void }) {
  const t = useTranslations('newProject');
  const tErrors = useTranslations('errors');
  const errorText = useErrorText();
  const router = useRouter();
  const notify = useSnackbar();
  const [displayName, setDisplayName] = useState('');
  const [typedId, setTypedId] = useState<string | null>(null);
  const [submitted, setSubmitted] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);

  // Until the user edits the id, it follows the server's suggestion for the name.
  const debouncedName = useDebouncedValue(displayName.trim(), 400);
  const suggestion = useResource((signal) => api.suggestProjectId(debouncedName, signal), [debouncedName], typedId === null && debouncedName !== '');
  const projectId = typedId ?? (debouncedName ? (suggestion.data?.projectId ?? '') : '');

  const debouncedId = useDebouncedValue(projectId, 400);
  const check = useResource((signal) => api.checkProjectId(debouncedId, signal), [debouncedId], PROJECT_ID.test(debouncedId));

  const status: IdStatus = !projectId
    ? 'empty'
    : !PROJECT_ID.test(projectId)
      ? 'invalid'
      : debouncedId !== projectId || check.loading || !check.data
        ? 'checking'
        : !check.data.valid
          ? 'invalid'
          : check.data.available
            ? 'available'
            : 'taken';

  const taken = status === 'taken' || (error instanceof ApiError && error.status === 'ALREADY_EXISTS');
  const nameError = submitted && !displayName.trim() ? t('nameRequired') : errorText.field(error, 'displayName');
  const idError =
    submitted && status === 'empty'
      ? t('idRequired')
      : taken
        ? t('idTaken')
        : status === 'invalid' && projectId.length >= 6
          ? tErrors('field.projectId')
          : errorText.field(error, 'projectId');
  const formError = error && !taken && !errorText.field(error, 'projectId') && !errorText.field(error, 'displayName') ? errorText.message(error) : null;

  async function create() {
    setSubmitted(true);
    if (!displayName.trim() || !PROJECT_ID.test(projectId) || status === 'taken') return;
    setPending(true);
    setError(null);
    try {
      const project = await api.createProject({ projectId, displayName: displayName.trim() });
      notify(t('created', { name: project.displayName }));
      router.push(routes.project(project.projectId));
    } catch (err) {
      setError(err);
      setPending(false);
    }
  }

  return (
    <Dialog
      isOpen
      onOpenChange={(open) => !open && onClose()}
      title={t('title')}
      isBusy={pending}
      onSubmit={create}
      actions={
        <>
          <Button variant="text" onPress={onClose} isDisabled={pending}>
            {t('cancel')}
          </Button>
          <Button type="submit" isPending={pending}>
            {t('create')}
          </Button>
        </>
      }
    >
      {formError && <Banner tone="error">{formError}</Banner>}
      <TextField
        label={t('name')}
        value={displayName}
        onChange={setDisplayName}
        maxLength={DISPLAY_NAME_MAX}
        isRequired
        autoFocus
        description={t('nameHint')}
        errorMessage={nameError}
      />
      <TextField
        label={t('id')}
        value={projectId}
        onChange={(value) => setTypedId(value.toLowerCase())}
        ltr
        isRequired
        maxLength={30}
        description={t('idRules')}
        errorMessage={idError}
        trailing={<IdStatusIndicator status={status} />}
      />
      <p className={styles.permanent}>
        <Icon name="lock" size={18} />
        <span>{t('idPermanent')}</span>
      </p>
    </Dialog>
  );
}

/** Announces the availability check politely as it changes. */
function IdStatusIndicator({ status }: { status: IdStatus }) {
  const t = useTranslations('newProject');
  return (
    <span role="status" className={[styles.status, status === 'available' && styles.available].filter(Boolean).join(' ')}>
      {status === 'checking' && t('checking')}
      {status === 'available' && (
        <>
          <Icon name="checkCircle" size={18} />
          {t('available')}
        </>
      )}
    </span>
  );
}
