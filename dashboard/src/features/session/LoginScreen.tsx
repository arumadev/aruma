'use client';

import { useRouter, useSearchParams } from 'next/navigation';
import { useEffect, useState, type FormEvent } from 'react';
import { Link } from 'react-aria-components';
import { useTranslations } from 'use-intl';
import { Banner } from '@/components/ui/Banner';
import { Button } from '@/components/ui/Button';
import { TextField } from '@/components/ui/TextField';
import { ApiError } from '@/lib/api/client';
import * as api from '@/lib/api/console';
import { useErrorText } from '@/lib/errors';
import { useCountdown } from '@/lib/hooks/useCountdown';
import { useDocumentTitle } from '@/lib/hooks/useDocumentTitle';
import { useResource } from '@/lib/hooks/useResource';
import { routes } from '@/lib/routes';
import { useSession } from '@/lib/session/SessionProvider';
import { safeNextPath } from '@/lib/validation';
import styles from './AuthForm.module.css';

/** D-02: sign in. Every failed attempt gets the same message, so it never reveals an account. */
export function LoginScreen() {
  const t = useTranslations('login');
  const errorText = useErrorText();
  const router = useRouter();
  const next = safeNextPath(useSearchParams().get('next'));
  const { signedIn } = useSession();
  const status = useResource((signal) => api.getStatus(signal), []);
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [submitted, setSubmitted] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [retryIn, startRetry] = useCountdown();
  useDocumentTitle(t('title'));

  const missingEmail = submitted && !email.trim();
  const missingPassword = submitted && !password;

  async function submit(event: FormEvent) {
    event.preventDefault();
    setSubmitted(true);
    if (!email.trim() || !password) return;
    setPending(true);
    setError(null);
    try {
      signedIn(await api.login({ email: email.trim(), password }));
      router.replace(next ?? routes.projects);
    } catch (err) {
      if (err instanceof ApiError && err.status === 'RESOURCE_EXHAUSTED') {
        startRetry(err.retryAfter ?? 60);
      } else if (err instanceof ApiError && err.code !== 0 && err.code < 500) {
        setError(t('failed'));
      } else {
        setError(errorText.message(err));
      }
      setPending(false);
    }
  }

  const setupRequired = status.data?.setupRequired;
  useEffect(() => {
    if (setupRequired) router.replace(routes.setup);
  }, [setupRequired, router]);

  return (
    <form className={styles.form} onSubmit={submit} noValidate>
      <div className={styles.heading}>
        <h1 className={styles.title}>{t('title')}</h1>
        <p className={styles.lead}>{t('lead')}</p>
      </div>
      {retryIn > 0 && <Banner tone="error">{t('throttled', { seconds: retryIn })}</Banner>}
      {error && <Banner tone="error">{error}</Banner>}
      <TextField
        label={t('email')}
        type="email"
        name="email"
        autoComplete="username"
        ltr
        value={email}
        onChange={setEmail}
        isRequired
        errorMessage={missingEmail ? t('emailRequired') : undefined}
      />
      <TextField
        label={t('password')}
        type="password"
        name="password"
        autoComplete="current-password"
        value={password}
        onChange={setPassword}
        isRequired
        errorMessage={missingPassword ? t('passwordRequired') : undefined}
      />
      <Button type="submit" className={styles.submit} isPending={pending} isDisabled={retryIn > 0}>
        {t('submit')}
      </Button>
      {status.data?.signupOpen && (
        <p className={styles.alt}>
          {t.rich('signupPrompt', { link: (chunks) => <Link href={routes.signup}>{chunks}</Link> })}
        </p>
      )}
    </form>
  );
}
