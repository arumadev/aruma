'use client';

import { useRouter } from 'next/navigation';
import { useState, type FormEvent } from 'react';
import { Link } from 'react-aria-components';
import { useTranslations } from 'use-intl';
import { Banner } from '@/components/ui/Banner';
import { Button } from '@/components/ui/Button';
import { Skeleton } from '@/components/ui/Skeleton';
import { TextField } from '@/components/ui/TextField';
import { ApiError } from '@/lib/api/client';
import * as api from '@/lib/api/console';
import { useErrorText } from '@/lib/errors';
import { useDocumentTitle } from '@/lib/hooks/useDocumentTitle';
import { useResource } from '@/lib/hooks/useResource';
import { useLocale } from '@/lib/i18n/LocaleProvider';
import { routes } from '@/lib/routes';
import { useSession } from '@/lib/session/SessionProvider';
import { PASSWORD_MIN } from '@/lib/validation';
import styles from './AuthForm.module.css';

/** Sign-up, offered only when the operator opened it. */
export function SignupScreen() {
  const t = useTranslations('signup');
  const tLogin = useTranslations('login');
  const errorText = useErrorText();
  const router = useRouter();
  const { locale } = useLocale();
  const { signedIn } = useSession();
  const status = useResource((signal) => api.getStatus(signal), []);
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [displayName, setDisplayName] = useState('');
  const [submitted, setSubmitted] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);
  useDocumentTitle(t('title'));

  if (status.loading) return <Skeleton lines={4} />;
  if (status.error) return <Banner tone="error">{errorText.message(status.error)}</Banner>;
  if (!status.data?.signupOpen) {
    return (
      <div className={styles.form}>
        <div className={styles.heading}>
          <h1 className={styles.title}>{t('title')}</h1>
        </div>
        <Banner tone="info">{t('closed')}</Banner>
        <p className={styles.alt}>
          <Link href={routes.login}>{t('backToLogin')}</Link>
        </p>
      </div>
    );
  }

  const shortPassword = submitted && password.length < PASSWORD_MIN;
  const missingEmail = submitted && !email.trim();
  const emailTaken = error instanceof ApiError && error.status === 'ALREADY_EXISTS';

  async function submit(event: FormEvent) {
    event.preventDefault();
    setSubmitted(true);
    if (!email.trim() || password.length < PASSWORD_MIN) return;
    setPending(true);
    setError(null);
    try {
      signedIn(await api.signup({ email: email.trim(), password, displayName: displayName.trim() || undefined, locale }));
      router.replace(routes.projects);
    } catch (err) {
      setError(err);
      setPending(false);
    }
  }

  const formError = error && !emailTaken && !errorText.field(error, 'email') && !errorText.field(error, 'password') ? errorText.message(error) : null;

  return (
    <form className={styles.form} onSubmit={submit} noValidate>
      <div className={styles.heading}>
        <h1 className={styles.title}>{t('title')}</h1>
        <p className={styles.lead}>{t('lead')}</p>
      </div>
      {formError && <Banner tone="error">{formError}</Banner>}
      <TextField
        label={tLogin('email')}
        type="email"
        autoComplete="email"
        ltr
        value={email}
        onChange={setEmail}
        isRequired
        errorMessage={
          missingEmail ? tLogin('emailRequired') : emailTaken ? t('emailTaken') : errorText.field(error, 'email')
        }
      />
      <TextField
        label={tLogin('password')}
        type="password"
        autoComplete="new-password"
        value={password}
        onChange={setPassword}
        isRequired
        description={t('passwordHint', { min: PASSWORD_MIN })}
        errorMessage={shortPassword ? t('passwordHint', { min: PASSWORD_MIN }) : errorText.field(error, 'password')}
      />
      <TextField
        label={t('displayName')}
        autoComplete="name"
        value={displayName}
        onChange={setDisplayName}
        description={t('optional')}
        errorMessage={errorText.field(error, 'displayName')}
      />
      <Button type="submit" className={styles.submit} isPending={pending}>
        {t('submit')}
      </Button>
      <p className={styles.alt}>
        {t.rich('loginPrompt', { link: (chunks) => <Link href={routes.login}>{chunks}</Link> })}
      </p>
    </form>
  );
}
