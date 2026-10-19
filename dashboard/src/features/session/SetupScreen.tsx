'use client';

import { useRouter } from 'next/navigation';
import { useEffect, useState, type FormEvent } from 'react';
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

/** D-01: first boot. Creates the first account with the one-time code from the server log. */
export function SetupScreen() {
  const t = useTranslations('setup');
  const tLogin = useTranslations('login');
  const tSignup = useTranslations('signup');
  const errorText = useErrorText();
  const router = useRouter();
  const { locale } = useLocale();
  const { signedIn } = useSession();
  const status = useResource((signal) => api.getStatus(signal), []);
  const [code, setCode] = useState('');
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [displayName, setDisplayName] = useState('');
  const [submitted, setSubmitted] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);
  useDocumentTitle(t('title'));

  // Once an account exists the page has no purpose.
  const done = status.data && !status.data.setupRequired && !pending;
  useEffect(() => {
    if (done) router.replace(routes.home);
  }, [done, router]);

  if (status.loading || done) return <Skeleton lines={5} />;
  if (status.error) return <Banner tone="error">{errorText.message(status.error)}</Banner>;

  const codeRejected = error instanceof ApiError && error.status === 'PERMISSION_DENIED';
  const formError =
    error && !codeRejected && !['email', 'password', 'displayName'].some((f) => errorText.field(error, f)) ? errorText.message(error) : null;

  async function submit(event: FormEvent) {
    event.preventDefault();
    setSubmitted(true);
    if (!code.trim() || !email.trim() || password.length < PASSWORD_MIN) return;
    setPending(true);
    setError(null);
    try {
      signedIn(
        await api.setup({ setupCode: code.trim(), email: email.trim(), password, displayName: displayName.trim() || undefined, locale }),
      );
      router.replace(routes.projects);
    } catch (err) {
      setError(err);
      setPending(false);
    }
  }

  return (
    <form className={styles.form} onSubmit={submit} noValidate>
      <div className={styles.heading}>
        <h1 className={styles.title}>{t('title')}</h1>
        <p className={styles.lead}>{t('lead')}</p>
      </div>
      {formError && <Banner tone="error">{formError}</Banner>}
      <TextField
        label={t('code')}
        autoComplete="one-time-code"
        ltr
        value={code}
        onChange={setCode}
        isRequired
        description={t('codeHint')}
        errorMessage={submitted && !code.trim() ? t('codeRequired') : codeRejected ? t('codeRejected') : undefined}
      />
      <TextField
        label={tLogin('email')}
        type="email"
        autoComplete="email"
        ltr
        value={email}
        onChange={setEmail}
        isRequired
        errorMessage={submitted && !email.trim() ? tLogin('emailRequired') : errorText.field(error, 'email')}
      />
      <TextField
        label={tLogin('password')}
        type="password"
        autoComplete="new-password"
        value={password}
        onChange={setPassword}
        isRequired
        description={tSignup('passwordHint', { min: PASSWORD_MIN })}
        errorMessage={
          submitted && password.length < PASSWORD_MIN ? tSignup('passwordHint', { min: PASSWORD_MIN }) : errorText.field(error, 'password')
        }
      />
      <TextField
        label={tSignup('displayName')}
        autoComplete="name"
        value={displayName}
        onChange={setDisplayName}
        description={tSignup('optional')}
        errorMessage={errorText.field(error, 'displayName')}
      />
      <Button type="submit" className={styles.submit} isPending={pending}>
        {t('submit')}
      </Button>
    </form>
  );
}
