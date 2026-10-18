'use client';

import { useState } from 'react';
import { useTranslations } from 'use-intl';
import { Banner } from '@/components/ui/Banner';
import { Button } from '@/components/ui/Button';
import { Dialog } from '@/components/ui/Dialog';
import { useSnackbar } from '@/components/ui/Snackbar';
import { TextField } from '@/components/ui/TextField';
import * as api from '@/lib/api/console';
import { authCode, useErrorText } from '@/lib/errors';

/** Adds an email and password user, as an operator would for a test or a support case. */
export function CreateUserDialog({ projectId, onCreated, onClose }: { projectId: string; onCreated: () => void; onClose: () => void }) {
  const t = useTranslations('auth.create');
  const tCommon = useTranslations('common');
  const errorText = useErrorText();
  const notify = useSnackbar();
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [displayName, setDisplayName] = useState('');
  const [uid, setUid] = useState('');
  const [attempted, setAttempted] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);

  async function save() {
    setAttempted(true);
    if (!email.trim() || !password) return;
    setPending(true);
    setError(null);
    try {
      await api.createAuthUser(projectId, {
        email: email.trim(),
        password,
        ...(displayName.trim() && { displayName: displayName.trim() }),
        ...(uid.trim() && { uid: uid.trim() }),
      });
      onCreated();
      notify(t('created'));
      onClose();
    } catch (err) {
      setError(err);
      setPending(false);
    }
  }

  // Problems with one field show on it; anything else above the form.
  const code = authCode(error);
  const onEmail = code === 'EMAIL_EXISTS' || code === 'INVALID_EMAIL';
  const onPassword = code === 'WEAK_PASSWORD' || code === 'PASSWORD_DOES_NOT_MEET_REQUIREMENTS';
  const onUid = code === 'DUPLICATE_LOCAL_ID' || code === 'INVALID_LOCAL_ID';
  const fieldError = (show: boolean) => (show ? errorText.message(error) : undefined);

  return (
    <Dialog
      isOpen
      onOpenChange={(open) => !open && onClose()}
      title={t('title')}
      isBusy={pending}
      onSubmit={save}
      actions={
        <>
          <Button variant="text" onPress={onClose} isDisabled={pending}>
            {tCommon('cancel')}
          </Button>
          <Button type="submit" isPending={pending}>
            {t('submit')}
          </Button>
        </>
      }
    >
      {Boolean(error) && !onEmail && !onPassword && !onUid && <Banner tone="error">{errorText.message(error)}</Banner>}
      <TextField
        label={t('email')}
        type="email"
        ltr
        autoFocus
        isRequired
        value={email}
        onChange={setEmail}
        errorMessage={attempted && !email.trim() ? t('emailRequired') : fieldError(onEmail)}
      />
      <TextField
        label={t('password')}
        type="password"
        autoComplete="new-password"
        isRequired
        value={password}
        onChange={setPassword}
        description={t('passwordHint')}
        errorMessage={attempted && !password ? t('passwordRequired') : fieldError(onPassword)}
      />
      <TextField label={t('displayName')} value={displayName} onChange={setDisplayName} maxLength={256} description={tCommon('optional')} />
      <TextField label={t('uid')} ltr value={uid} onChange={setUid} maxLength={128} description={t('uidHint')} errorMessage={fieldError(onUid)} />
    </Dialog>
  );
}
