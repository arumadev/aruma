'use client';

import { useState, type FormEvent } from 'react';
import { useTranslations } from 'use-intl';
import { Banner } from '@/components/ui/Banner';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { useSnackbar } from '@/components/ui/Snackbar';
import { TextField } from '@/components/ui/TextField';
import * as api from '@/lib/api/console';
import { useErrorText } from '@/lib/errors';
import { PASSWORD_MIN } from '@/lib/validation';
import styles from './AccountScreen.module.css';

/** Changes the password. The server signs out every other session. */
export function PasswordCard({ onChanged }: { onChanged: () => void }) {
  const t = useTranslations('account');
  const tSignup = useTranslations('signup');
  const errorText = useErrorText();
  const notify = useSnackbar();
  const [current, setCurrent] = useState('');
  const [next, setNext] = useState('');
  const [repeat, setRepeat] = useState('');
  const [attempted, setAttempted] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);

  const tooShort = next.length < PASSWORD_MIN;
  const mismatch = next !== repeat;

  async function submit(event: FormEvent) {
    event.preventDefault();
    setAttempted(true);
    if (!current || tooShort || mismatch) return;
    setPending(true);
    setError(null);
    try {
      await api.changePassword({ currentPassword: current, newPassword: next });
      setCurrent('');
      setNext('');
      setRepeat('');
      setAttempted(false);
      notify(t('passwordChanged'));
      onChanged();
    } catch (err) {
      setError(err);
    } finally {
      setPending(false);
    }
  }

  const fieldErrors = { current: errorText.field(error, 'currentPassword'), next: errorText.field(error, 'newPassword') };
  return (
    <Card title={t('password')} description={t('passwordHint')}>
      <form className={styles.form} onSubmit={submit} noValidate>
        {Boolean(error) && !fieldErrors.current && !fieldErrors.next && <Banner tone="error">{errorText.message(error)}</Banner>}
        <TextField
          label={t('currentPassword')}
          type="password"
          autoComplete="current-password"
          value={current}
          onChange={setCurrent}
          isRequired
          errorMessage={attempted && !current ? t('currentRequired') : fieldErrors.current}
        />
        <TextField
          label={t('newPassword')}
          type="password"
          autoComplete="new-password"
          value={next}
          onChange={setNext}
          isRequired
          description={tSignup('passwordHint', { min: PASSWORD_MIN })}
          errorMessage={attempted && tooShort ? tSignup('passwordHint', { min: PASSWORD_MIN }) : fieldErrors.next}
        />
        <TextField
          label={t('repeatPassword')}
          type="password"
          autoComplete="new-password"
          value={repeat}
          onChange={setRepeat}
          isRequired
          errorMessage={attempted && mismatch ? t('mismatch') : undefined}
        />
        <div>
          <Button type="submit" isPending={pending}>
            {t('changePassword')}
          </Button>
        </div>
      </form>
    </Card>
  );
}
