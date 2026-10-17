'use client';

import { useState } from 'react';
import { useTranslations } from 'use-intl';
import { Banner } from '@/components/ui/Banner';
import { Button } from '@/components/ui/Button';
import { Dialog } from '@/components/ui/Dialog';
import { useSnackbar } from '@/components/ui/Snackbar';
import { TextField } from '@/components/ui/TextField';
import { ApiError } from '@/lib/api/client';
import * as api from '@/lib/api/console';
import type { ServiceAccount } from '@/lib/api/types';
import { useErrorText } from '@/lib/errors';
import { DISPLAY_NAME_MAX, SERVICE_ACCOUNT_ID } from '@/lib/validation';

export function CreateServiceAccountDialog({
  projectId,
  onCreated,
  onClose,
}: {
  projectId: string;
  onCreated: (account: ServiceAccount) => void;
  onClose: () => void;
}) {
  const t = useTranslations('serviceAccounts');
  const tCommon = useTranslations('common');
  const tErrors = useTranslations('errors');
  const errorText = useErrorText();
  const notify = useSnackbar();
  const [accountId, setAccountId] = useState('');
  const [displayName, setDisplayName] = useState('');
  const [attempted, setAttempted] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);

  const taken = error instanceof ApiError && error.status === 'ALREADY_EXISTS';
  const idError =
    attempted && !SERVICE_ACCOUNT_ID.test(accountId) ? tErrors('field.accountId') : taken ? t('idTaken') : errorText.field(error, 'accountId');
  const formError = error && !taken && !errorText.field(error, 'accountId') && !errorText.field(error, 'displayName') ? errorText.message(error) : null;

  async function create() {
    setAttempted(true);
    if (!SERVICE_ACCOUNT_ID.test(accountId)) return;
    setPending(true);
    setError(null);
    try {
      const sa = await api.createServiceAccount(projectId, { accountId, displayName: displayName.trim() || undefined });
      onCreated(sa);
      notify(t('created'));
      onClose();
    } catch (err) {
      setError(err);
      setPending(false);
    }
  }

  return (
    <Dialog
      isOpen
      onOpenChange={(open) => !open && onClose()}
      title={t('createTitle')}
      isBusy={pending}
      onSubmit={create}
      actions={
        <>
          <Button variant="text" onPress={onClose} isDisabled={pending}>
            {tCommon('cancel')}
          </Button>
          <Button type="submit" isPending={pending}>
            {t('create')}
          </Button>
        </>
      }
    >
      {formError && <Banner tone="error">{formError}</Banner>}
      <TextField
        label={t('accountId')}
        value={accountId}
        onChange={(value) => {
          setAccountId(value.toLowerCase());
          setError(null);
        }}
        ltr
        isRequired
        autoFocus
        maxLength={30}
        description={t('accountIdHint')}
        errorMessage={idError}
      />
      <TextField
        label={t('displayName')}
        value={displayName}
        onChange={setDisplayName}
        maxLength={DISPLAY_NAME_MAX}
        description={tCommon('optional')}
        errorMessage={errorText.field(error, 'displayName')}
      />
    </Dialog>
  );
}
